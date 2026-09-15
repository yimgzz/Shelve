package sshengine

// In-process sshd rig (master plan phase 3d, task 3) plus the
// capturing emitter and small helpers shared with phase 3e's
// integration suite. Everything runs inside the test process: no Docker,
// no external sshd. The rig is built on the classic x/crypto server API
// (ssh.NewServerConn) because the pinned version predates ssh.Server; it
// handles "session" channels (pty-req / shell / window-change / echo /
// exit-status) and "direct-tcpip" channels (server-side bridging) which
// -L/-D forwards rely on.

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
	"shelve/internal/sshx/knownhosts"
)

// ---------------------------------------------------------------------
// CapturingEmitter: an ordered, concurrent-safe event log with per-name
// waiters (timeout-based, no sleeps).
// ---------------------------------------------------------------------

type capturedEvent struct {
	name    string
	payload any
}

// CapturingEmitter records every Emit call in order. Tests observe the log
// either by polling it or through an armed EventWaiter (see Arm).
type CapturingEmitter struct {
	mu     sync.Mutex
	events []capturedEvent
}

// NewCapturingEmitter returns an empty capturing emitter.
func NewCapturingEmitter() *CapturingEmitter {
	return &CapturingEmitter{}
}

// Emit appends the event to the ordered log.
func (c *CapturingEmitter) Emit(event string, payload any) {
	c.mu.Lock()
	c.events = append(c.events, capturedEvent{name: event, payload: payload})
	c.mu.Unlock()
}

// Events returns a copy of the full ordered event log.
func (c *CapturingEmitter) Events() []capturedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedEvent(nil), c.events...)
}

// OfName returns the events whose name matches (ordered).
func (c *CapturingEmitter) OfName(name string) []capturedEvent {
	var out []capturedEvent
	for _, e := range c.Events() {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

// Has reports whether at least one event of name was emitted.
func (c *CapturingEmitter) Has(name string) bool {
	return len(c.OfName(name)) > 0
}

// EventWaiter is an armed wait. Arm records the current event-log position, so
// Wait observes an event emitted between Arm and Wait. A plain "wait for the
// next event" API (register when called) could not see such an event — a race
// when the event is produced synchronously by the action under test (a PTY
// echo, a keyboard-interactive round triggered by SubmitKbdintResponse, an
// exit triggered by Write). Arm before the action instead.
type EventWaiter struct {
	em   *CapturingEmitter
	name string
	from int
}

// Arm records the current event-log length for `name`.
func (c *CapturingEmitter) Arm(name string) *EventWaiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &EventWaiter{em: c, name: name, from: len(c.events)}
}

// Wait blocks until the first event named w.name emitted at or after the arm
// position, returning it. It fails the test after timeout.
func (w *EventWaiter) Wait(t *testing.T, timeout time.Duration) capturedEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		w.em.mu.Lock()
		for i := w.from; i < len(w.em.events); i++ {
			if w.em.events[i].name == w.name {
				e := w.em.events[i]
				w.em.mu.Unlock()
				return e
			}
		}
		w.em.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for event %q", w.name)
			return capturedEvent{}
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------
// In-process sshd rig
// ---------------------------------------------------------------------

// testSSHOpts configures the rig's auth surface: an optional password
// ("" disables password auth), the set of authorized public keys, and —
// for plan P009 bastion tests — an optional keyboard-interactive callback.
type testSSHOpts struct {
	user           string
	password       string
	authorizedKeys []ssh.PublicKey
	// kbdint, if non-nil, configures keyboard-interactive auth. It is
	// invoked ONCE per keyboard-interactive method attempt; multi-round
	// flows (the bastion's two rounds) are driven by calling
	// client.Challenge multiple times inside it, ending with the
	// zero-question completion round (RFC 4256 §3.3).
	kbdint func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error)
}

// testSSHServer is an in-process ssh.ServerConn-based SSH server on a
// loopback ephemeral port.
type testSSHServer struct {
	ln      net.Listener
	addr    string
	hostKey ssh.PublicKey

	// directTCP records every direct-tcpip target this server bridged
	// ("host:port"). The jump-chain regression test uses it to prove
	// later hops are routed THROUGH the previous hop instead of being
	// dialed directly from the client machine.
	mu        sync.Mutex
	directTCP []string
}

// newTestSSHServer starts an in-process SSH server and registers its
// teardown with t. host:port is returned via Addr.
func newTestSSHServer(t *testing.T, opts testSSHOpts) *testSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if opts.password != "" && string(password) == opts.password {
				return nil, nil
			}
			return nil, fmt.Errorf("authentication failed")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, ak := range opts.authorizedKeys {
				if bytes.Equal(ak.Marshal(), key.Marshal()) {
					return nil, nil
				}
			}
			return nil, fmt.Errorf("unknown public key")
		},
		KeyboardInteractiveCallback: opts.kbdint,
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSHServer{ln: ln, addr: ln.Addr().String(), hostKey: hostSigner.PublicKey()}
	go s.serveLoop(cfg)
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *testSSHServer) serveLoop(cfg *ssh.ServerConfig) {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(nc, cfg)
	}
}

func (s *testSSHServer) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer conn.Close()
	go func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()
	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			go handleTestSessionChannel(newChan)
		case "direct-tcpip":
			go s.handleTestDirectTCPIP(newChan)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel")
		}
	}
}

// Addr returns the server's host:port.
func (s *testSSHServer) Addr() string { return s.addr }

// port returns the server's numeric TCP port.
func (s *testSSHServer) port() int {
	_, p, _ := net.SplitHostPort(s.addr)
	n, _ := strconv.Atoi(p)
	return n
}

// recordDirectTCP appends a served direct-tcpip target.
func (s *testSSHServer) recordDirectTCP(host string, port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directTCP = append(s.directTCP, net.JoinHostPort(host, strconv.Itoa(port)))
}

// directTCPReqs returns a copy of the served direct-tcpip targets.
func (s *testSSHServer) directTCPReqs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.directTCP...)
}

func (s *testSSHServer) hostKeyB64() string { return testPubKeyB64(s.hostKey) }

func (s *testSSHServer) hostKeyFingerprint() string {
	fp, _ := knownhosts.Fingerprint(s.hostKeyB64())
	return fp
}

// handleTestSessionChannel serves one interactive "session" channel:
// accepts pty-req / window-change-req / shell, echoes each input line as
// "echo: <line>", and maps a lone "exit" line to exit-status 0.
func handleTestSessionChannel(newChan ssh.NewChannel) {
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	go func() {
		for req := range reqs {
			switch req.Type {
			case "pty-req", "shell", "window-change-req":
				if req.WantReply {
					_ = req.Reply(true, nil)
				}
			default:
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}
	}()

	br := bufio.NewReader(ch)
	for {
		line, err := br.ReadString('\n')
		if strings.TrimSpace(line) == "exit" {
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&testExitStatusMsg{Status: 0}))
			return
		}
		if len(line) > 0 {
			_, _ = fmt.Fprintf(ch, "echo: %s", line)
		}
		if err != nil {
			return
		}
	}
}

// testExitStatusMsg matches the wire format of the SSH "exit-status"
// message (a single uint32), sent before the channel closes so the client
// Session.Wait sees a zero exit status.
type testExitStatusMsg struct {
	Status uint32
}

// handleTestDirectTCPIP serves a "direct-tcpip" channel (the -L/-D
// forward tunnel AND the transport of later hop-chain hops): it dials
// the requested destination and bridges both directions with the
// channel, recording the target for the jump-chain regression test.
func (s *testSSHServer) handleTestDirectTCPIP(newChan ssh.NewChannel) {
	var msg struct {
		Raddr string
		Rport uint32
		Laddr string
		Lport uint32
	}
	if err := ssh.Unmarshal(newChan.ExtraData(), &msg); err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "bad direct-tcpip request")
		return
	}
	s.recordDirectTCP(msg.Raddr, int(msg.Rport))
	target, err := net.Dial("tcp", net.JoinHostPort(msg.Raddr, strconv.Itoa(int(msg.Rport))))
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "connect failed")
		return
	}
	defer target.Close()
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go func() {
		for range reqs {
			// forwarded channels carry no meaningful requests
		}
	}()
	// Bridge both directions. CloseWrite must run in the SAME goroutine
	// as the channel writes: x/crypto v0.53 reads/writes ch.sentEOF
	// without a lock (channel.go:248 vs :589), so a CloseWrite racing an
	// in-flight channel.Write trips -race. The main goroutine therefore
	// only READS the channel; the writer goroutine propagates EOF after
	// its copy returns.
	go func() {
		_, _ = io.Copy(ch, target)
		if cc, ok := ch.(interface{ CloseWrite() error }); ok {
			_ = cc.CloseWrite()
		}
	}()
	_, _ = io.Copy(target, ch)
}

// ---------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------

// waitForState polls m until the tab reaches the given state.
func waitForState(t *testing.T, m *Manager, tabID string, want tabState) {
	t.Helper()
	waitFor(t, func() bool {
		st, _ := m.tabState(tabID)
		return st == want
	}, "state "+want.String())
}

// newManagerAtPath builds a Manager over a fresh known_hosts file at the
// given path (so tests can inspect the file after approval).
func newManagerAtPath(t *testing.T, khPath string) (*Manager, *CapturingEmitter) {
	t.Helper()
	kh, err := knownhosts.New(khPath)
	if err != nil {
		t.Fatal(err)
	}
	em := NewCapturingEmitter()
	return New(em, kh), em
}

// newManagerForRig builds a Manager whose known_hosts is pre-seeded with
// the rig's host key, so connections reach auth/prompt/ready without a
// host-key prompt. Host-key prompt tests use newManagerAtPath instead.
func newManagerForRig(t *testing.T, rig *testSSHServer) (*Manager, *CapturingEmitter) {
	t.Helper()
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	kh, err := knownhosts.New(khPath)
	if err != nil {
		t.Fatal(err)
	}
	host, port := splitHostPortInt(t, rig.Addr())
	if err := kh.Add(host, port, rig.hostKey.Type(), rig.hostKeyB64()); err != nil {
		t.Fatal(err)
	}
	if err := kh.Save(); err != nil {
		t.Fatal(err)
	}
	em := NewCapturingEmitter()
	return New(em, kh), em
}

// rigPasswordSession builds a password-auth session targeting the rig.
func rigPasswordSession(addr, user, password string) *model.Session {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return &model.Session{
		Host: host, Port: port, User: user,
		Auth: model.Auth{Type: model.AuthPassword, Password: password},
	}
}

// writeEncryptedKey writes a passphrase-protected ed25519 key to a temp
// file and returns its path and public key.
func writeEncryptedKey(t *testing.T, passphrase string) (keyPath string, publicKey ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "test-key", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(block)
	keyPath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath, signer.PublicKey()
}

// writePlainKey writes an UNENCRYPTED ed25519 private key to a temp file
// and returns its path and public key (for tests that must reach auth
// without a vault:key-prompt round-trip).
func writePlainKey(t *testing.T) (keyPath string, publicKey ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath, signer.PublicKey()
}

// startEchoServer runs a raw TCP echo server and returns its listener.
func startEchoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c) // echo
			}(conn)
		}
	}()
	return ln
}

// freePort reserves a transient loopback port (closed immediately) so a
// forward can bind it; there is a tiny reuse race, acceptable in tests.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func splitHostPortInt(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return host, n
}

func testPubKeyB64(p ssh.PublicKey) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(p)))
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return line[i+1:]
	}
	return line
}

// socks5Dial performs a minimal SOCKS5 handshake (no auth) + CONNECT to
// host:port through the proxy at proxyAddr and returns the established
// connection.
func socks5Dial(t *testing.T, proxyAddr, host string, port int) (net.Conn, error) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (net.Conn, error) {
		_ = c.Close()
		return nil, e
	}
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fail(err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil {
		return fail(err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		return fail(fmt.Errorf("socks5 greeting rejected: % x", resp))
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return fail(err)
	}
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil {
		return fail(err)
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		return fail(fmt.Errorf("socks5 connect failed, code %d", rep[1]))
	}
	return c, nil
}
