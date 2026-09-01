package sshengine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
	"shelve/internal/sshx"
	"shelve/internal/sshx/args"
)

// Batching / backpressure parameters (master plan §2 A6, §5).
const (
	// batchInterval: flush pending output on this tick.
	batchInterval = 50 * time.Millisecond
	// batchBytes: flush pending output once it reaches this size.
	batchBytes = 16 * 1024
	// maxPendingBytes: backpressure limit — while the pending buffer
	// exceeds 1 MiB, reads pause and the buffer is drained.
	maxPendingBytes = 1 << 20
	// readBufSize: one SSH channel read.
	readBufSize = 32 * 1024
	// defaultPort: fallback for a 0/absent port (sessions are
	// validated 1-65535; defensive only).
	defaultPort = 22
)

// tabState is the record state machine: connecting → ready | error,
// ready → closed. The record is kept after error and closed
// (master plan A4: UI renders Retry/Close; Reconnect re-dials).
type tabState int

const (
	stateConnecting tabState = iota
	stateReady
	stateError
	stateClosed
)

func (s tabState) String() string {
	switch s {
	case stateReady:
		return StateReady
	case stateError:
		return StateError
	case stateClosed:
		return StateClosed
	default:
		return StateConnecting
	}
}

// hop is one dial step of the chain: structured jump hosts (in order),
// then the Extra Args ProxyJump (if present), then the target.
type hop struct {
	user     string
	host     string
	port     int
	auth     model.Auth
	isTarget bool
}

// liveConn is one tab's full connection state. One run goroutine per
// tab (dial → pump → finish); public methods reach its mutable fields
// only through Manager.mu. pty, ptyIn and ptyOut are published under
// the lock at ready and read-only afterwards (the pump goroutine and
// Write read them directly).
type liveConn struct {
	m       *Manager
	tabID   string
	connID  string        // = tabID for Connect; Phase 3d uses an ephemeral ID
	session model.Session // stored for Reconnect (master plan A4)

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the run goroutine exits

	// mutable under m.mu:
	state        tabState
	message      string
	disconnected bool
	conns        []net.Conn         // raw TCP conns of the dial chain
	clients      []*ssh.Client      // per-hop SSH clients (kept alive)
	forwards     []*forwardListener // live -L/-D port forwards (phase 3d)
	pty          *ssh.Session       // the target pty+shell session
	ptyIn        io.WriteCloser     // pty stdin pipe (StdinPipe)
	ptyOut       io.Reader          // pty stdout pipe (StdoutPipe; stderr merged)

	teardownOnce sync.Once
	exitCh       chan int // buffered(1): shell exit status, -1 when unknown
}

// run is the per-tab goroutine: dial the chain, then run the read pump
// until the shell exits or the tab is disconnected. On dial failure
// the record stays in "error" (master plan A4); on shell exit it lands
// in "closed" (record kept for Retry/Close).
func (l *liveConn) run() {
	defer close(l.done)
	defer l.m.teardown(l)

	err := l.dial()
	if err == nil {
		l.pump() // returns on shell exit or disconnect
		return
	}
	if errors.Is(err, context.Canceled) {
		return // the tab was closed while dialing: no error state
	}
	l.fail(err)
}

// dial walks the hop chain in order (hops after the first are tunneled
// through the previous hop's SSH client as direct-tcpip channels),
// keeps intermediate clients alive, and on the final hop requests a PTY
// (xterm-256color, 80x24) and a shell, publishing the pty session and
// reaching the ready state.
func (l *liveConn) dial() error {
	hops, timeout, parsed, err := buildSessionChain(l.session)
	if err != nil {
		return err
	}

	for i := range hops {
		h := &hops[i]
		addr := net.JoinHostPort(h.host, strconv.Itoa(h.port))

		methods, err := l.m.authenticateHop(l.ctx, l.connID, h)
		if err != nil {
			return hopErrorAt(h, i, len(hops), err)
		}

		var nc net.Conn
		if i == 0 {
			// First hop: real TCP dial from this machine.
			nc, err = net.DialTimeout("tcp", addr, timeout)
		} else {
			// Later hops ride INSIDE the previous hop's SSH connection
			// as a direct-tcpip channel — the jump-chain routing rule.
			prev := l.clientsAt(i - 1)
			if prev == nil {
				return hopErrorAt(h, i, len(hops), errors.New("previous hop client missing"))
			}
			nc, err = dialHopVia(l.ctx, prev, addr, timeout)
		}
		if err != nil {
			return hopErrorAt(h, i, len(hops), err)
		}
		if aborted := l.storeConn(nc); aborted {
			return l.ctx.Err()
		}

		cfg := &ssh.ClientConfig{
			User:            h.user,
			Auth:            methods,
			HostKeyCallback: l.m.HostKeyCallback(l.connID),
		}
		cn, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
		if err != nil {
			nc.Close()
			return hopErrorAt(h, i, len(hops), err)
		}
		if aborted := l.storeClient(ssh.NewClient(cn, chans, reqs)); aborted {
			return l.ctx.Err()
		}
	}

	last := len(hops) - 1
	targetHop := &hops[last]
	target := l.clientsAt(last)
	if target == nil {
		return hopErrorAt(targetHop, last, len(hops), errors.New("target client missing"))
	}
	sess, err := target.NewSession()
	if err != nil {
		return hopErrorAt(targetHop, last, len(hops), err)
	}
	failed := func(err error) error {
		sess.Close()
		return hopErrorAt(targetHop, last, len(hops), err)
	}
	if err := sess.RequestPty("xterm-256color", 24, 80, ptyModes()); err != nil {
		return failed(err)
	}
	// The pipes must be requested before Shell (x/crypto contract); the
	// pty merges stderr into the stdout stream, so only the stdin and
	// stdout pipes are consumed here.
	stdinW, err := sess.StdinPipe()
	if err != nil {
		return failed(err)
	}
	stdoutR, err := sess.StdoutPipe()
	if err != nil {
		return failed(err)
	}
	if err := sess.Shell(); err != nil {
		return failed(err)
	}

	if aborted := l.publishPty(sess, stdinW, stdoutR); aborted {
		sess.Close()
		return l.ctx.Err()
	}
	// Start local port forwards after shell-ready (non-fatal on bind
	// failure). The tab reaches ready regardless (master plan §5).
	if len(parsed.Forwards) > 0 {
		l.m.startForwards(l, target, parsed.Forwards)
	}
	return nil
}

// publishPty publishes the pty session and its I/O handles and starts
// the exit-status watcher; it moves the record to ready and emits
// "ready". Reports whether the tab was torn down concurrently (in
// which case no state change and no event: Disconnect owns the
// "closed" event).
func (l *liveConn) publishPty(sess *ssh.Session, stdinW io.WriteCloser, stdoutR io.Reader) bool {
	l.m.mu.Lock()
	aborted := l.disconnected
	if !aborted {
		l.pty = sess
		l.ptyIn = stdinW
		l.ptyOut = stdoutR
		l.state = stateReady
		l.message = ""
	}
	l.m.mu.Unlock()
	if aborted {
		return true
	}
	l.exitCh = make(chan int, 1)
	go func() {
		err := sess.Wait() // nil: exit status 0; *ssh.ExitError: non-zero
		st := -1           // unknown: the remote did not report an exit status
		var exitErr *ssh.ExitError
		if err == nil {
			st = 0
		} else if errors.As(err, &exitErr) {
			st = exitErr.ExitStatus()
		}
		l.exitCh <- st
	}()
	l.m.emitStatus(l.tabID, StateReady, "")
	return false
}

// buildSessionChain expands a session into its ordered hops, the
// per-hop dial timeout, and the parsed Extra Args (phase 3d consumes
// parsed.Forwards). Structured JumpHosts come first, the Extra Args
// ProxyJump (if present) is appended after them (master plan §2 D5),
// then the target. The ProxyJump shorthand carries no credentials, so
// it authenticates with the target session's auth (its user, when
// given, overrides the target's user). ConnectTimeout is in seconds;
// absent or 0 → default 10 s.
func buildSessionChain(sess model.Session) ([]hop, time.Duration, *args.Parsed, error) {
	parsed, err := args.Parse(sess.ExtraArgs)
	if err != nil {
		return nil, 0, nil, err
	}
	hops := make([]hop, 0, len(sess.JumpHosts)+2)
	for _, j := range sess.JumpHosts {
		hops = append(hops, hop{
			user: j.User, host: j.Host, port: orDefaultPort(j.Port), auth: j.Auth,
		})
	}
	if pj := parsed.ProxyJump; pj != nil {
		user := pj.User
		if user == "" {
			user = sess.User
		}
		hops = append(hops, hop{user: user, host: pj.Host, port: orDefaultPort(pj.Port), auth: sess.Auth})
	}
	hops = append(hops, hop{
		user: sess.User, host: sess.Host, port: orDefaultPort(sess.Port),
		auth: sess.Auth, isTarget: true,
	})

	timeout := defaultDialTimeout
	if ct := parsed.Options.ConnectTimeout; ct != nil && *ct > 0 {
		timeout = time.Duration(*ct) * time.Second
	}
	return hops, timeout, parsed, nil
}

func orDefaultPort(port int) int {
	if port <= 0 {
		return defaultPort
	}
	return port
}

// dialHopVia opens the next hop's transport through the previous hop's
// SSH client as a direct-tcpip channel — the jump-chain routing rule:
// the next hop's SSH handshake rides INSIDE the established SSH
// connection, so only the first hop is ever dialed from this machine
// (master plan phase 3c). The per-hop ConnectTimeout still applies; a
// channel open that outlives the timeout is dropped (its late result
// lands in the buffered channel and is discarded).
func dialHopVia(ctx context.Context, prev *ssh.Client, addr string, timeout time.Duration) (net.Conn, error) {
	type res struct {
		nc  net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		nc, err := prev.Dial("tcp", addr)
		ch <- res{nc: nc, err: err}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.nc, r.err
	case <-t.C:
		return nil, fmt.Errorf("dial %s via previous hop: timeout after %s", addr, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// hopErrorAt attributes a hop failure to its position in the chain:
// "jump host 1/2 (user@host:port): …" for jumps (1-based position over
// the total hop count), "target host:port: …" for the target
// (master plan phase 3c).
func hopErrorAt(h *hop, idx, total int, err error) error {
	if h.isTarget {
		return fmt.Errorf("target %s:%d: %v", h.host, h.port, err)
	}
	return fmt.Errorf("jump host %d/%d (%s@%s:%d): %v", idx+1, total, h.user, h.host, h.port, err)
}

// authenticateHop builds a hop's SSH auth methods, running the
// key-passphrase prompt (vault:key-prompt) when the key file is
// encrypted and its passphrase is not yet cached (master plan A2).
// The dial suspends until SubmitKeyPassphrase, the PromptTimeout
// failure, or ctx completion. On a submitted passphrase the auth is
// retried exactly once; ErrKeyPassphraseWrong fails the dial. The
// passphrase is cached only after the retry succeeds, so a wrong
// passphrase can never shadow a later corrected prompt.
func (m *Manager) authenticateHop(ctx context.Context, connID string, h *hop) ([]ssh.AuthMethod, error) {
	methods, err := sshx.AuthMethods(h.auth, m.passphraseFor(h.auth))
	var need *sshx.ErrKeyPassphraseRequired
	if !errors.As(err, &need) {
		return methods, err
	}
	ch, slot := m.beginKeyPrompt(connID, need.KeyPath)
	defer func() {
		// The success path removes the slot via SubmitKeyPassphrase;
		// the timeout/abort paths remove it here.
		m.discardPrompt(connID, slot)
	}()
	select {
	case pw := <-ch:
		retried, err := sshx.AuthMethods(h.auth, &pw)
		if errors.Is(err, sshx.ErrKeyPassphraseWrong) {
			return nil, sshx.ErrKeyPassphraseWrong
		}
		if err != nil {
			return nil, err
		}
		m.cachePassphrase(need.KeyPath, pw)
		return retried, nil
	case <-time.After(m.promptTimeout()):
		return nil, errors.New("key passphrase prompt timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// storeConn records a raw chain conn for teardown and reports whether
// the tab was torn down concurrently.
func (l *liveConn) storeConn(nc net.Conn) bool {
	l.m.mu.Lock()
	l.conns = append(l.conns, nc)
	aborted := l.disconnected
	l.m.mu.Unlock()
	if aborted {
		nc.Close()
	}
	return aborted
}

// storeClient records a per-hop client for teardown and reports whether
// the tab was torn down concurrently.
func (l *liveConn) storeClient(c *ssh.Client) bool {
	l.m.mu.Lock()
	l.clients = append(l.clients, c)
	aborted := l.disconnected
	l.m.mu.Unlock()
	if aborted {
		c.Close()
	}
	return aborted
}

func (l *liveConn) clientsAt(i int) *ssh.Client {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	if i < 0 || i >= len(l.clients) {
		return nil
	}
	return l.clients[i]
}

// fail moves the record to error + message (master plan A4: the record
// is kept so the UI renders Retry/Close and Reconnect re-dials with the
// same tabID). No event when the tab was disconnected in the race.
func (l *liveConn) fail(err error) {
	l.m.mu.Lock()
	l.state = stateError
	l.message = err.Error()
	aborted := l.disconnected
	l.m.mu.Unlock()
	l.m.dropHostKeyPrompt(l.connID)
	if !aborted {
		l.m.emitStatus(l.tabID, StateError, err.Error())
	}
}

// pump is the per-ready-tab read pump (master plan §2 A6): a reader
// goroutine fills the pending buffer and the pump emits terminal:data
// on the 50 ms tick or once ≥16 KB is pending, and the final flush on
// close. Backpressure (master plan §5): while pending exceeds 1 MiB
// the reader blocks on its signal channel until the buffer is drained.
func (l *liveConn) pump() {
	var (
		mu           sync.Mutex
		pending      []byte
		burstStarted bool
	)
	changed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, readBufSize)
		for {
			n, err := l.ptyOut.Read(buf)
			mu.Lock()
			if n > 0 {
				if len(pending) == 0 {
					// A new output burst begins: mark it so the pump can
					// flush immediately (interactive fast path) instead
					// of waiting out the batching tick.
					burstStarted = true
				}
				pending = append(pending, buf[:n]...)
			}
			mu.Unlock()
			select {
			case changed <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	flush := func() {
		mu.Lock()
		if len(pending) == 0 {
			mu.Unlock()
			return
		}
		data := pending
		pending = nil
		mu.Unlock()
		l.m.emit.Emit(EventTerminalData, TerminalDataPayload{
			TabID: l.tabID,
			Data:  base64.StdEncoding.EncodeToString(data),
		})
	}

	for {
		mu.Lock()
		over := len(pending) > maxPendingBytes
		big := len(pending) >= batchBytes
		start := burstStarted
		burstStarted = false
		mu.Unlock()
		switch {
		case over:
			// Paused until drained: flush empties the buffer, after
			// which the reader's blocked send unblocks for more data
			// (the TCP window applies remote throttling meanwhile).
			flush()
			continue
		case big:
			flush()
			continue
		case start:
			// Interactive fast path: the chunk that just arrived began a
			// NEW burst, so flush at once instead of waiting for the
			// 50 ms tick. Sustained streams (pending never empties
			// between reads) still coalesce on the tick / 16 KB
			// threshold, preserving the throughput contract (A6).
			flush()
			continue
		}
		select {
		case <-done:
			flush()
			l.finishExit()
			return
		case <-ticker.C:
			flush()
		case <-changed:
			// start was false (continuation of an ongoing stream): loop
			// around and re-check burstStarted; a brand-new burst that
			// arrived during the select is picked up on the next pass.
		}
	}
}

// finishExit drives the shell-exit transition: terminal:exit (with the
// exit status when the remote reported one) followed by state closed
// and the "closed" status event. The record stays for Retry/Close
// (master plan A4). Skipped when the tab was disconnected: Disconnect
// owns its "closed" event and the record removal.
func (l *liveConn) finishExit() {
	l.m.mu.Lock()
	if l.disconnected {
		l.m.mu.Unlock()
		return
	}
	l.disconnected = true
	l.state = stateClosed
	l.message = ""
	l.m.mu.Unlock()

	st := <-l.exitCh // the wait goroutine always delivers (buffered)
	var payload TerminalExitPayload
	payload.TabID = l.tabID
	if st >= 0 {
		s := st
		payload.ExitStatus = &s
	}
	l.m.emit.Emit(EventTerminalExit, payload)
	l.m.emitStatus(l.tabID, StateClosed, "")
}

// teardown closes the tab's resources in master plan §5 order: port
// forwards (they ride on the target client), the pty session, then
// chain clients last→first, then raw conns last→first. It emits
// ssh:forward{state:"closed"} for each live forward. Idempotent. Must
// not be called with m.mu held.
func (m *Manager) teardown(l *liveConn) {
	l.teardownOnce.Do(func() {
		m.mu.Lock()
		pty := l.pty
		clients := l.clients
		conns := l.conns
		forwards := l.forwards
		m.mu.Unlock()

		// Closing the listener unblocks each accept loop; accepted conns
		// are closed so their relays (and SOCKS handlers) unwind.
		for _, f := range forwards {
			f.close()
		}
		if pty != nil {
			pty.Close()
		}
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		for i := len(conns) - 1; i >= 0; i-- {
			conns[i].Close()
		}
		for _, f := range forwards {
			<-f.done // accept loop exited (listener closed)
			m.emitForward(l.tabID, f.spec, ForwardClosed, "", "")
		}

		// Invoke the optional tab-closed hook last: by now the final-hop
		// SSH client is closed, so a co-resident SFTP client riding on it
		// can be released too. Never holds m.mu while the hook runs.
		m.mu.Lock()
		hook := m.onTabClosed
		m.mu.Unlock()
		if hook != nil {
			hook(l.tabID)
		}
	})
}

// ptyModes is the standard OpenSSH default mode set for an interactive
// shell.
func ptyModes() ssh.TerminalModes {
	return ssh.TerminalModes{
		ssh.VEOF:    0x04,
		ssh.VINTR:   0x03,
		ssh.VQUIT:   0x1c,
		ssh.VERASE:  0x7f,
		ssh.VKILL:   0x15,
		ssh.VSUSP:   0x1a,
		ssh.ICANON:  1,
		ssh.ISIG:    1,
		ssh.IEXTEN:  1,
		ssh.ECHO:    1,
		ssh.ECHOE:   1,
		ssh.ECHOK:   0,
		ssh.ECHOCTL: 1,
	}
}
