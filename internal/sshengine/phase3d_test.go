package sshengine

// Phase 3d in-process tests (master plan §9 unit tests): they exercise
// the real dial/prompt/forward/lifecycle paths end-to-end against the
// in-process sshd rig in testutil_test.go — no Docker, no external sshd.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
	"shelve/internal/sshx/knownhosts"
)

// capturedStatuses returns the ordered terminal:status states for one tab.
func capturedStatuses(em *CapturingEmitter, tabID string) []string {
	var out []string
	for _, e := range em.Events() {
		if p, ok := e.payload.(TerminalStatusPayload); ok && p.TabID == tabID {
			out = append(out, p.State)
		}
	}
	return out
}

// waitReady waits until the tab's status stream reaches [connecting,
// ready] and asserts that order (the connecting event is emitted
// synchronously during Connect, so it cannot be consumed via WaitEvent).
func waitReady(t *testing.T, m *Manager, em *CapturingEmitter, tabID string) {
	t.Helper()
	waitFor(t, func() bool {
		return len(capturedStatuses(em, tabID)) >= 2
	}, "connect statuses for "+tabID)
	st := capturedStatuses(em, tabID)
	if st[0] != StateConnecting || st[1] != StateReady {
		t.Fatalf("status stream = %v, want [connecting ready]", st)
	}
}

// waitForward polls the emitter for a ssh:forward payload in the given
// state. Polling (not WaitEvent) is used because forwarding events
// emitted synchronously during Disconnect/teardown precede any post-call
// waiter registration.
func waitForward(t *testing.T, em *CapturingEmitter, state string) ForwardPayload {
	t.Helper()
	var p ForwardPayload
	waitFor(t, func() bool {
		for _, e := range em.Events() {
			if f, ok := e.payload.(ForwardPayload); ok && f.State == state {
				p = f
				return true
			}
		}
		return false
	}, "ssh:forward "+state)
	return p
}

// waitStatusState polls the emitter until the tab reports the given state.
func waitStatusState(t *testing.T, em *CapturingEmitter, tabID, state string) {
	t.Helper()
	waitFor(t, func() bool {
		for _, s := range capturedStatuses(em, tabID) {
			if s == state {
				return true
			}
		}
		return false
	}, "status "+state)
}

// TestInProcessPasswordConnectAndEcho drives a real password connection:
// connecting → ready order, PTY echo, resize and disconnect.
func TestInProcessPasswordConnectAndEcho(t *testing.T) {
	const user, password = "alice", "s3cret"
	rig := newTestSSHServer(t, testSSHOpts{user: user, password: password})
	m, em := newManagerForRig(t, rig)

	tabID, err := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, m, em, tabID)

	// PTY echo through the shell.
	if err := m.Write(tabID, base64.StdEncoding.EncodeToString([]byte("hi\n"))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ev := em.WaitEvent(t, EventTerminalData, 15*time.Second)
	data, err := base64.StdEncoding.DecodeString(ev.payload.(TerminalDataPayload).Data)
	if err != nil {
		t.Fatalf("decode terminal data: %v", err)
	}
	if !strings.Contains(string(data), "echo: hi") {
		t.Fatalf("terminal data = %q, want it to contain %q", data, "echo: hi")
	}

	// Resize is a no-op error on a ready tab.
	if err := m.Resize(tabID, 120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if err := m.Disconnect(tabID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
}

// TestInProcessRemoteExit triggers a remote shell exit and asserts the
// terminal:exit → closed transition plus post-close typed errors.
func TestInProcessRemoteExit(t *testing.T) {
	const user, password = "bob", "pw123"
	rig := newTestSSHServer(t, testSSHOpts{user: user, password: password})
	m, em := newManagerForRig(t, rig)
	tabID, _ := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	waitReady(t, m, em, tabID)

	if err := m.Write(tabID, base64.StdEncoding.EncodeToString([]byte("exit\n"))); err != nil {
		t.Fatal(err)
	}
	exitEv := em.WaitEvent(t, EventTerminalExit, 15*time.Second)
	ep := exitEv.payload.(TerminalExitPayload)
	if ep.TabID != tabID || ep.ExitStatus == nil || *ep.ExitStatus != 0 {
		t.Fatalf("exit payload = %+v, want tabID %s and exitStatus 0", ep, tabID)
	}
	waitForState(t, m, tabID, stateClosed)

	if err := m.Write(tabID, base64.StdEncoding.EncodeToString([]byte("x\n"))); !errors.Is(err, ErrTabNotReady) {
		t.Fatalf("Write after close: %v, want ErrTabNotReady", err)
	}

	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(tabID); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("second Disconnect: %v, want ErrUnknownTab", err)
	}
}

// TestInProcessDisconnectEmitsClosed asserts Disconnect emits a single
// terminal:status "closed".
func TestInProcessDisconnectEmitsClosed(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	m, em := newManagerForRig(t, rig)
	tabID, _ := m.Connect(rigPasswordSession(rig.Addr(), "u", "p"))
	waitReady(t, m, em, tabID)

	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
	waitStatusState(t, em, tabID, StateClosed)
}

// TestInProcessHostKeyApprove: fresh known_hosts → prompt, approve →
// ready, the entry is persisted, and Reconnect reuses it without a prompt.
func TestInProcessHostKeyApprove(t *testing.T) {
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	m, em := newManagerAtPath(t, khPath)
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})

	tabID, _ := m.Connect(rigPasswordSession(rig.Addr(), "u", "p"))
	promptEv := em.WaitEvent(t, EventVaultHostkeyPrompt, 15*time.Second)
	pp := promptEv.payload.(HostKeyPromptPayload)
	if pp.Host != "127.0.0.1" || pp.ConnID != tabID {
		t.Fatalf("prompt payload = %+v", pp)
	}
	// The connect is suspended on the prompt: still connecting.
	waitForState(t, m, tabID, stateConnecting)

	if err := m.ApproveHostKey(tabID); err != nil {
		t.Fatalf("ApproveHostKey: %v", err)
	}
	waitReady(t, m, em, tabID)

	// The approved key is now persisted in known_hosts.
	kh, err := knownhosts.New(khPath)
	if err != nil {
		t.Fatal(err)
	}
	host, port := splitHostPortInt(t, rig.Addr())
	if !kh.Has(host, port, rig.hostKey.Type(), rig.hostKeyB64()) {
		t.Fatal("approved host key not persisted in known_hosts")
	}

	// Reconnect on the ready tab redials under the same tabID; the cached
	// host key means no new prompt appears.
	promptsBefore := len(em.OfName(EventVaultHostkeyPrompt))
	if err := m.Reconnect(tabID); err != nil {
		t.Fatal(err)
	}
	waitReady(t, m, em, tabID)
	if got := len(em.OfName(EventVaultHostkeyPrompt)); got != promptsBefore {
		t.Fatalf("host-key prompts grew on reconnect: %d → %d", promptsBefore, got)
	}
	_ = m.Disconnect(tabID)
}

// TestInProcessHostKeyReject: rejecting the unknown key surfaces a clear
// terminal:status error.
func TestInProcessHostKeyReject(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	m, em := newManagerAtPath(t, filepath.Join(t.TempDir(), "known_hosts"))
	tabID, _ := m.Connect(rigPasswordSession(rig.Addr(), "u", "p"))
	em.WaitEvent(t, EventVaultHostkeyPrompt, 15*time.Second)

	if err := m.RejectHostKey(tabID); err != nil {
		t.Fatal(err)
	}
	errEv := em.WaitEvent(t, EventTerminalStatus, 15*time.Second)
	ep := errEv.payload.(TerminalStatusPayload)
	if ep.State != StateError {
		t.Fatalf("state = %s, want error", ep.State)
	}
	if !strings.Contains(ep.Message, "rejected") {
		t.Fatalf("message = %q, want it to mention rejection", ep.Message)
	}
	_ = m.Disconnect(tabID)
}

// TestInProcessKeyPassphrase covers the correct / wrong / timeout paths
// for a passphrase-protected key (each with a fresh manager so the A2
// cache never short-circuits the prompt).
func TestInProcessKeyPassphrase(t *testing.T) {
	keyPath, pub := writeEncryptedKey(t, "hunter2")
	rig := newTestSSHServer(t, testSSHOpts{user: "u", authorizedKeys: []ssh.PublicKey{pub}})
	sess := &model.Session{
		Host: "127.0.0.1", Port: rig.port(), User: "u",
		Auth: model.Auth{Type: model.AuthKey, KeyPath: keyPath},
	}
	newManager := func(t *testing.T) (*Manager, *CapturingEmitter) {
		return newManagerForRig(t, rig)
	}

	t.Run("correct passphrase", func(t *testing.T) {
		m, em := newManager(t)
		tabID, _ := m.Connect(sess)
		em.WaitEvent(t, EventVaultKeyPrompt, 15*time.Second)
		if err := m.SubmitKeyPassphrase(tabID, "hunter2"); err != nil {
			t.Fatal(err)
		}
		waitReady(t, m, em, tabID)
		_ = m.Disconnect(tabID)
	})

	t.Run("wrong passphrase", func(t *testing.T) {
		m, em := newManager(t)
		tabID, _ := m.Connect(sess)
		em.WaitEvent(t, EventVaultKeyPrompt, 15*time.Second)
		if err := m.SubmitKeyPassphrase(tabID, "wrong"); err != nil {
			t.Fatal(err)
		}
		ev := em.WaitEvent(t, EventTerminalStatus, 15*time.Second)
		if got := ev.payload.(TerminalStatusPayload).State; got != StateError {
			t.Fatalf("state = %s, want error", got)
		}
		_ = m.Disconnect(tabID)
	})

	t.Run("prompt timeout", func(t *testing.T) {
		m, em := newManager(t)
		m.PromptTimeout = 500 * time.Millisecond
		tabID, _ := m.Connect(sess)
		em.WaitEvent(t, EventVaultKeyPrompt, 15*time.Second)
		ev := em.WaitEvent(t, EventTerminalStatus, 15*time.Second)
		if got := ev.payload.(TerminalStatusPayload).State; got != StateError {
			t.Fatalf("state = %s, want error", got)
		}
		_ = m.Disconnect(tabID)
	})
}

// TestInProcessKeyAuthJumpHost dials a chain THROUGH a jump host whose own
// Auth is a key path (regression: the engine must use the jump host's key
// for that hop — not the target session's password). Covers both Connect
// (PTY path, live.go) and the shared bare-chain dial (TestConnection /
// DialMonitorClient, testconn.go).
func TestInProcessKeyAuthJumpHost(t *testing.T) {
	keyPath, pub := writePlainKey(t)
	jumpRig := newTestSSHServer(t, testSSHOpts{user: "tunnel", authorizedKeys: []ssh.PublicKey{pub}})
	targetRig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})

	// Pre-seed known_hosts with BOTH hops' host keys so the dial reaches
	// auth without host-key prompts.
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	kh, err := knownhosts.New(khPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, rig := range []*testSSHServer{jumpRig, targetRig} {
		host, port := splitHostPortInt(t, rig.Addr())
		if err := kh.Add(host, port, rig.hostKey.Type(), rig.hostKeyB64()); err != nil {
			t.Fatal(err)
		}
	}
	if err := kh.Save(); err != nil {
		t.Fatal(err)
	}
	em := NewCapturingEmitter()
	m := New(em, kh)

	jumpHost, jumpPort := splitHostPortInt(t, jumpRig.Addr())
	sess := &model.Session{
		Host: "127.0.0.1", Port: targetRig.port(), User: "u",
		Auth: model.Auth{Type: model.AuthPassword, Password: "p"},
		JumpHosts: []model.JumpHost{
			{Host: jumpHost, Port: jumpPort, User: "tunnel",
				Auth: model.Auth{Type: model.AuthKey, KeyPath: keyPath}},
		},
	}

	tabID, err := m.Connect(sess)
	if err != nil {
		t.Fatalf("Connect through key-auth jump host: %v", err)
	}
	waitReady(t, m, em, tabID)

	// Regression (jump-chain routing): the target hop must be reached
	// THROUGH the jump host via a direct-tcpip channel, never by dialing
	// the target directly from the client machine. With the routing bug
	// the jump host serves zero direct-tcpip requests.
	wantTarget := net.JoinHostPort("127.0.0.1", strconv.Itoa(targetRig.port()))
	waitFor(t, func() bool {
		return len(jumpRig.directTCPReqs()) >= 1
	}, "jump host serves direct-tcpip to target")
	if !containsString(jumpRig.directTCPReqs(), wantTarget) {
		t.Fatalf("jump host direct-tcpip targets = %v, want it to contain %s (target must be routed THROUGH the jump host)", jumpRig.directTCPReqs(), wantTarget)
	}

	_ = m.Disconnect(tabID)

	// The bare-chain dial (TestConnection / DialMonitorClient) must apply
	// the jump host's key the same way AND route through it again.
	if err := m.TestConnection(sess); err != nil {
		t.Fatalf("TestConnection through key-auth jump host: %v", err)
	}
	waitFor(t, func() bool {
		return len(jumpRig.directTCPReqs()) >= 2
	}, "TestConnection routes target via jump host")
}

// containsString reports whether s contains v.
func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestInProcessForwardL proves -L dial-through: a connection to the local
// port reaches an in-test TCP echo server, and teardown emits "closed".
func TestInProcessForwardL(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	echoLn := startEchoServer(t)
	echoHost, echoPort := splitHostPortInt(t, echoLn.Addr().String())
	localPort := freePort(t)

	sess := rigPasswordSession(rig.Addr(), "u", "p")
	sess.ExtraArgs = fmt.Sprintf("-L %d:%s:%d", localPort, echoHost, echoPort)

	m, em := newManagerForRig(t, rig)
	tabID, _ := m.Connect(sess)
	waitReady(t, m, em, tabID)

	fp := waitForward(t, em, ForwardListening)
	if fp.LocalAddr == "" {
		t.Fatalf("forward payload = %+v, want local addr", fp)
	}

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	if err != nil {
		t.Fatalf("dial local forward: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read forwarded echo: %v", err)
	}
	if got := string(buf[:n]); !strings.Contains(got, "ping") {
		t.Fatalf("forwarded echo = %q, want it to contain %q", got, "ping")
	}
	_ = c.Close()
	_ = echoLn.Close()

	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
	if got := waitForward(t, em, ForwardClosed).State; got != ForwardClosed {
		t.Fatalf("forward teardown state = %s, want closed", got)
	}
}

// TestInProcessForwardBindFailure: a pre-bound local port yields
// ssh:forward failed + app:toast, while the connection still reaches ready.
func TestInProcessForwardBindFailure(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	localPort := freePort(t)
	block, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer block.Close()

	sess := rigPasswordSession(rig.Addr(), "u", "p")
	sess.ExtraArgs = fmt.Sprintf("-L %d:127.0.0.1:80", localPort)

	m, em := newManagerForRig(t, rig)
	tabID, _ := m.Connect(sess)
	waitReady(t, m, em, tabID)

	if got := waitForward(t, em, ForwardFailed).State; got != ForwardFailed {
		t.Fatalf("forward state = %s, want failed", got)
	}
	waitFor(t, func() bool { return em.Has(EventAppToast) }, "app:toast for bind failure")
	waitForState(t, m, tabID, stateReady)

	_ = m.Disconnect(tabID)
}

// TestInProcessForwardD proves the minimal SOCKS5 proxy: handshake +
// CONNECT tunnels to the in-test echo server, and teardown emits "closed".
func TestInProcessForwardD(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	echoLn := startEchoServer(t)
	echoHost, echoPort := splitHostPortInt(t, echoLn.Addr().String())
	localPort := freePort(t)

	sess := rigPasswordSession(rig.Addr(), "u", "p")
	sess.ExtraArgs = fmt.Sprintf("-D %d", localPort)

	m, em := newManagerForRig(t, rig)
	tabID, _ := m.Connect(sess)
	waitReady(t, m, em, tabID)

	if got := waitForward(t, em, ForwardListening).State; got != ForwardListening {
		t.Fatalf("forward state = %s, want listening", got)
	}

	c, err := socks5Dial(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)), echoHost, echoPort)
	if err != nil {
		t.Fatalf("socks5 dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read via socks: %v", err)
	}
	if got := string(buf[:n]); !strings.Contains(got, "ping") {
		t.Fatalf("socks echo = %q, want it to contain %q", got, "ping")
	}
	_ = c.Close()

	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
	if got := waitForward(t, em, ForwardClosed).State; got != ForwardClosed {
		t.Fatalf("forward teardown state = %s, want closed", got)
	}
}

// TestInProcessTestConnection covers success (no tab record), auth
// failure naming the hop, and the unknown-host-key approve path.
func TestInProcessTestConnection(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	host, port := splitHostPortInt(t, rig.Addr())

	t.Run("success leaves no tab record", func(t *testing.T) {
		m, _ := newManagerForRig(t, rig)
		sess := &model.Session{Host: host, Port: port, User: "u",
			Auth: model.Auth{Type: model.AuthPassword, Password: "p"}}
		if err := m.TestConnection(sess); err != nil {
			t.Fatalf("TestConnection: %v", err)
		}
		if got := len(m.Tabs()); got != 0 {
			t.Fatalf("Tabs() = %d, want 0 (no tab record)", got)
		}
	})

	t.Run("auth failure names the hop", func(t *testing.T) {
		m, _ := newManagerForRig(t, rig)
		sess := &model.Session{Host: host, Port: port, User: "u",
			Auth: model.Auth{Type: model.AuthPassword, Password: "wrong"}}
		err := m.TestConnection(sess)
		if err == nil {
			t.Fatal("TestConnection with wrong password: want error")
		}
		if !strings.Contains(err.Error(), "target") {
			t.Fatalf("error = %q, want target attribution", err)
		}
	})

	t.Run("unknown host key then approve succeeds", func(t *testing.T) {
		m, em := newManagerAtPath(t, filepath.Join(t.TempDir(), "known_hosts"))
		sess := &model.Session{Host: host, Port: port, User: "u",
			Auth: model.Auth{Type: model.AuthPassword, Password: "p"}}
		done := make(chan error, 1)
		go func() { done <- m.TestConnection(sess) }()
		em.WaitEvent(t, EventVaultHostkeyPrompt, 15*time.Second)
		prompt := em.OfName(EventVaultHostkeyPrompt)[0].payload.(HostKeyPromptPayload)
		if err := m.ApproveHostKey(prompt.ConnID); err != nil {
			t.Fatalf("ApproveHostKey: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("TestConnection after approve: %v", err)
		}
	})
}

// TestInProcessShutdownLeakFree connects three live tabs, shuts down, and
// asserts no goroutines leak and a second Shutdown is a no-op.
func TestInProcessShutdownLeakFree(t *testing.T) {
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	m, em := newManagerForRig(t, rig)
	baseline := settleGoroutines(t)

	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		tabID, err := m.Connect(rigPasswordSession(rig.Addr(), "u", "p"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tabID)
	}
	for _, id := range ids {
		waitReady(t, m, em, id)
	}
	if got := len(m.Tabs()); got != 3 {
		t.Fatalf("Tabs() = %d, want 3", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	m.Shutdown(ctx)
	cancel()
	if got := len(m.Tabs()); got != 0 {
		t.Fatalf("Tabs() after Shutdown = %d, want 0", got)
	}
	if got := settleGoroutines(t); got != baseline {
		t.Fatalf("goroutine delta after Shutdown: %d → %d", baseline, got)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	m.Shutdown(ctx2)
	cancel2()
	if got := settleGoroutines(t); got != baseline {
		t.Fatalf("goroutine delta after double Shutdown: %d → %d", baseline, got)
	}
}

// TestInProcessPerfSmoke runs 3 concurrent + 10 sequential connects against
// a 300-session fixture pointed at the rig, logging timings (localhost; no
// hard assert beyond < 2 s each, master plan §9 perf smoke).
func TestInProcessPerfSmoke(t *testing.T) {
	if runtime.GOARCH == "386" {
		t.Skip("perf smoke skipped on 386")
	}
	rig := newTestSSHServer(t, testSSHOpts{user: "u", password: "p"})
	host, port := splitHostPortInt(t, rig.Addr())

	sessions := make([]*model.Session, 300)
	for i := range sessions {
		sessions[i] = &model.Session{
			Name: fmt.Sprintf("S-%04d", i), Host: host, Port: port, User: "u",
			Auth: model.Auth{Type: model.AuthPassword, Password: "p"},
		}
	}
	m, _ := newManagerForRig(t, rig)

	connectToReady := func(s *model.Session) (time.Time, error) {
		start := time.Now()
		tabID, err := m.Connect(s)
		if err != nil {
			return start, err
		}
		waitFor(t, func() bool {
			st, _ := m.tabState(tabID)
			return st == stateReady
		}, "perf connect ready")
		return start, nil
	}

	// 3 concurrent connects.
	type res struct {
		start time.Time
		err   error
	}
	resCh := make(chan res, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			st, err := connectToReady(sessions[i])
			resCh <- res{st, err}
		}(i)
	}
	for i := 0; i < 3; i++ {
		r := <-resCh
		if r.err != nil {
			t.Errorf("concurrent Connect %d: %v", i, r.err)
			continue
		}
		if d := time.Since(r.start); d > 2*time.Second {
			t.Errorf("concurrent connect %d took %v (>2s)", i, d)
		}
	}

	// 10 sequential connects.
	seqStart := time.Now()
	for i := 10; i < 20; i++ {
		if _, err := connectToReady(sessions[i]); err != nil {
			t.Fatal(err)
		}
	}
	avg := time.Since(seqStart) / 10
	t.Logf("perf smoke: 3 concurrent connects OK; 10 sequential connects avg %v", avg)
	if avg > 2*time.Second {
		t.Fatalf("sequential connect avg %v (>2s each)", avg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	m.Shutdown(ctx)
	cancel()
}
