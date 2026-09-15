package sshengine

// Plan P009: bastion-style jump host — chain building (embedded target
// username + skipDial), the keyboard-interactive handshake flow, and
// TestConnection parity. The in-process rig's kbdint callback mirrors the
// vendor bastion from the Phase A logs: keyboard-interactive only, two
// question rounds, then the zero-question completion round (RFC 4256
// §3.3).

import (
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
)

// ---------------------------------------------------------------------
// Server-side kbdint flows
// ---------------------------------------------------------------------

// kbdintRecorder captures what the bastion server observed: the
// authenticated username and the answers of every question round.
type kbdintRecorder struct {
	mu      sync.Mutex
	user    string
	answers [][]string
}

func (r *kbdintRecorder) record(answers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers = append(r.answers, answers)
}

func (r *kbdintRecorder) username() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.user
}

func (r *kbdintRecorder) answerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.answers)
}

// twoRoundKbdint builds the rig callback for the two-round bastion flow
// (plan P009 §1): round 1 is a masked LDAP password (pass1), round 2 a
// second masked password (pass2), then the zero-question completion
// round. A wrong answer rejects the auth attempt.
func twoRoundKbdint(rec *kbdintRecorder, pass1, pass2 string) func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
	return func(meta ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		rec.mu.Lock()
		rec.user = meta.User()
		rec.mu.Unlock()
		rounds := []struct {
			name, instr string
			expect      string
		}{
			{"LDAP", "LDAP authentication: server bastion.test", pass1},
			{"", "", pass2},
		}
		for i, rd := range rounds {
			answers, err := client(rd.name, rd.instr, []string{"Password:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			rec.record(answers)
			if len(answers) != 1 || answers[0] != rd.expect {
				return nil, errors.New("bastion: wrong password (round " + strconv.Itoa(i+1) + ")")
			}
		}
		// Completion round (RFC 4256 §3.3): zero questions, empty answers.
		if _, err := client("", "", nil, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// runawayKbdint is a misbehaving bastion that keeps demanding a password
// round (plan P009 §7: defense against >maxKbdintRounds rounds). It never
// completes within 10 question rounds.
func runawayKbdint(rec *kbdintRecorder) func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
	return func(meta ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		rec.mu.Lock()
		rec.user = meta.User()
		rec.mu.Unlock()
		for i := 0; i < maxKbdintRounds+5; i++ {
			answers, err := client("", "", []string{"Password:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			rec.record(answers)
			if len(answers) != 1 || answers[0] != "pw" {
				return nil, errors.New("bastion: wrong password")
			}
		}
		return nil, nil // unreachable for the round-bounded client
	}
}

// dropMidPromptKbdint starts a two-round exchange, then drops the
// connection ~300 ms in — while the client's round-1 prompt is still
// pending (plan P009 §7: server disconnect mid-prompt).
func dropMidPromptKbdint(rec *kbdintRecorder, pass1, pass2 string) func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
	two := twoRoundKbdint(rec, pass1, pass2)
	return func(meta ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		closer, ok := meta.(interface{ Close() error })
		if ok {
			go func() {
				time.Sleep(300 * time.Millisecond)
				_ = closer.Close()
			}()
		}
		return two(meta, client)
	}
}

// ---------------------------------------------------------------------
// Session builders
// ---------------------------------------------------------------------

// bastionTestSession builds a bastion session whose jump hop is the rig at
// rigAddr (password auth, the stored password the prompts prefill) and
// whose target is targetHost:22 — never dialed, reached through the
// bastion relay.
func bastionTestSession(rigAddr, targetHost string) *model.Session {
	host, portStr, _ := net.SplitHostPort(rigAddr)
	port, _ := strconv.Atoi(portStr)
	return &model.Session{
		Name: "bastion-test",
		Host: targetHost, Port: 22, User: "alice",
		Auth: model.Auth{}, // target auth optional in bastion mode
		JumpHosts: []model.JumpHost{{
			Host: host, Port: port, User: "relay", Bastion: true,
			Auth: model.Auth{Type: model.AuthPassword, Password: "b-pass"},
		}},
	}
}

// ---------------------------------------------------------------------
// Chain building
// ---------------------------------------------------------------------

// TestBuildSessionChainBastion asserts the plan P009 §4 chain semantics:
// the bastion hop carries the embedded target username, the target hop is
// skipDial, and non-bastion chains are unchanged.
func TestBuildSessionChainBastion(t *testing.T) {
	sess := bastionTestSession("127.0.0.1:2222", "target.example.com")
	hops, _, _, err := buildSessionChain(*sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 2 {
		t.Fatalf("hops = %d, want 2 (bastion + skipped target)", len(hops))
	}
	if hops[0].user != "relay@target.example.com" {
		t.Fatalf("bastion user = %q, want relay@target.example.com", hops[0].user)
	}
	if !hops[0].bastion || hops[0].isTarget || hops[0].skipDial {
		t.Fatalf("bastion hop flags = {bastion:%v isTarget:%v skipDial:%v}, want {true false false}",
			hops[0].bastion, hops[0].isTarget, hops[0].skipDial)
	}
	if !hops[1].isTarget || !hops[1].skipDial || hops[1].bastion {
		t.Fatalf("target flags = {isTarget:%v skipDial:%v bastion:%v}, want {true true false}",
			hops[1].isTarget, hops[1].skipDial, hops[1].bastion)
	}
	if hops[1].user != "alice" || hops[1].host != "target.example.com" {
		t.Fatalf("target hop = %s@%s, want alice@target.example.com", hops[1].user, hops[1].host)
	}

	// An intermediate hop before the bastion keeps direct dialing.
	plain := &model.Session{
		Name: "bastion-test-mid", Host: "target.example.com", Port: 22, User: "alice",
		Auth: model.Auth{},
		JumpHosts: []model.JumpHost{
			{Host: "10.0.0.1", Port: 22, User: "mid",
				Auth: model.Auth{Type: model.AuthPassword, Password: "mp"}},
			sess.JumpHosts[0],
		},
	}
	hops, _, _, err = buildSessionChain(*plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 3 {
		t.Fatalf("hops = %d, want 3", len(hops))
	}
	if hops[0].skipDial || hops[0].bastion || hops[0].user != "mid" {
		t.Fatalf("intermediate hop mutated: %+v", hops[0])
	}
	if !hops[1].bastion || hops[1].user != "relay@target.example.com" {
		t.Fatalf("bastion hop = %+v", hops[1])
	}
	if !hops[2].skipDial || !hops[2].isTarget {
		t.Fatalf("target hop = %+v", hops[2])
	}

	// Non-bastion chains stay byte-identical to the pre-P009 shape.
	direct := rigPasswordSession("127.0.0.1:3333", "u", "p")
	hops, _, _, err = buildSessionChain(*direct)
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 1 || hops[0].skipDial || hops[0].bastion || !hops[0].isTarget {
		t.Fatalf("direct chain changed: %+v", hops)
	}
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// statusSequence returns the ordered, compact event stream of one tab:
// "status:<state>" for terminal:status, raw event names for the prompt
// events (plan P009 §7: full success event-order assertion).
func statusSequence(em *CapturingEmitter) []string {
	var seq []string
	for _, e := range em.Events() {
		switch e.name {
		case EventTerminalStatus:
			if p, ok := e.payload.(TerminalStatusPayload); ok {
				seq = append(seq, "status:"+p.State)
			}
		case EventVaultHostkeyPrompt, EventVaultKbdintPrompt, EventVaultKeyPrompt:
			seq = append(seq, e.name)
		}
	}
	return seq
}

// assertSubsequence fails unless want appears in got in order (not
// necessarily contiguously).
func assertSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event sequence %v does not contain subsequence %v", got, want)
	}
}

// waitForErrorState polls until the tab reports an error state and
// returns its message.
func waitForErrorState(t *testing.T, m *Manager, tabID string) string {
	t.Helper()
	var msg string
	waitFor(t, func() bool {
		st, message := m.tabState(tabID)
		if st == stateError {
			msg = message
			return true
		}
		return false
	}, "error state for "+tabID)
	return msg
}

// assertNoPromptSlot fails if a prompt slot survived for connID (the
// "no leaked prompt slot" invariant, plan P009 §7).
func assertNoPromptSlot(t *testing.T, m *Manager, connID string) {
	t.Helper()
	m.mu.Lock()
	_, ok := m.prompts[connID]
	m.mu.Unlock()
	if ok {
		t.Fatalf("prompt slot leaked for connID %s", connID)
	}
}

// ---------------------------------------------------------------------
// kbdint flow tests
// ---------------------------------------------------------------------

// TestInProcessBastionKbdintSuccess drives the full plan P009 §7 happy
// path: fresh known_hosts → host-key prompt (approve) → kbdint round 1
// (prefill = stored bastion password) → submit → round 2 → submit →
// ready, PTY echo, and the server-observed username is the embedded
// user@target. The event order is asserted exactly.
func TestInProcessBastionKbdintSuccess(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerAtPath(t, t.TempDir()+"/known_hosts")

	base := settleGoroutines(t)
	hostkey := em.Arm(EventVaultHostkeyPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}

	// 1) Host-key prompt (fresh known_hosts) → approve.
	hostkey.Wait(t, 15*time.Second)
	round1 := em.Arm(EventVaultKbdintPrompt)
	if err := m.ApproveHostKey(tabID); err != nil {
		t.Fatal(err)
	}

	// 2) kbdint round 1: prefill carries the stored bastion password.
	ev := round1.Wait(t, 15*time.Second)
	p := ev.payload.(KbdintPromptPayload)
	if p.ConnID != tabID || p.Name != "LDAP" || len(p.Questions) != 1 ||
		p.Questions[0] != "Password:" || len(p.Echo) != 1 || p.Echo[0] ||
		p.Prefill != "b-pass" {
		t.Fatalf("round-1 payload = %+v, want connID=%s name=LDAP [Password:] echo=[false] prefill=b-pass", p, tabID)
	}
	round2 := em.Arm(EventVaultKbdintPrompt)
	if err := m.SubmitKbdintResponse(p.ConnID, []string{"b-pass"}); err != nil {
		t.Fatal(err)
	}

	// 3) kbdint round 2: the prefill is offered again (the user
	// overwrites it — here the target answer differs).
	ev = round2.Wait(t, 15*time.Second)
	p = ev.payload.(KbdintPromptPayload)
	if p.Prefill != "b-pass" || p.Name != "" {
		t.Fatalf("round-2 payload = %+v, want prefill=b-pass empty name", p)
	}
	if err := m.SubmitKbdintResponse(p.ConnID, []string{"t-pass"}); err != nil {
		t.Fatal(err)
	}

	// 4) Ready + PTY echo.
	waitReady(t, m, em, tabID)
	echo := em.Arm(EventTerminalData)
	if err := m.Write(tabID, base64.StdEncoding.EncodeToString([]byte("hi\n"))); err != nil {
		t.Fatal(err)
	}
	ev = echo.Wait(t, 15*time.Second)
	data, _ := base64.StdEncoding.DecodeString(ev.payload.(TerminalDataPayload).Data)
	if !strings.Contains(string(data), "echo: hi") {
		t.Fatalf("terminal data = %q, want it to contain %q", data, "echo: hi")
	}

	// 5) The bastion saw the embedded target username.
	if got := rec.username(); got != "relay@target.example.com" {
		t.Fatalf("server username = %q, want relay@target.example.com", got)
	}
	if rec.answerCount() != 2 {
		t.Fatalf("server rounds = %d, want 2", rec.answerCount())
	}

	// 6) Exact event order (plan P009 §7).
	assertSubsequence(t, statusSequence(em), []string{
		"status:" + StateConnecting,
		EventVaultHostkeyPrompt,
		EventVaultKbdintPrompt,
		EventVaultKbdintPrompt,
		"status:" + StateReady,
	})

	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
	assertNoPromptSlot(t, m, tabID)
	if got := settleGoroutines(t); got != base {
		t.Fatalf("goroutines = %d, want %d (leak)", got, base)
	}
}

// TestInProcessBastionKbdintWrongAnswer: a rejected answer fails the dial
// with a bastion-hop-attributed message.
func TestInProcessBastionKbdintWrongAnswer(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)

	kbd := em.Arm(EventVaultKbdintPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	ev := kbd.Wait(t, 15*time.Second)
	if err := m.SubmitKbdintResponse(ev.payload.(KbdintPromptPayload).ConnID, []string{"wrong"}); err != nil {
		t.Fatal(err)
	}
	msg := waitForErrorState(t, m, tabID)
	if !strings.Contains(msg, "jump host 1/2") || !strings.Contains(msg, "relay@target.example.com@") {
		t.Fatalf("error = %q, want bastion-hop attribution", msg)
	}
	if !strings.Contains(msg, "unable to authenticate") {
		t.Fatalf("error = %q, want x/crypto auth failure", msg)
	}
	assertNoPromptSlot(t, m, tabID)
	if got := rec.username(); got != "relay@target.example.com" {
		t.Fatalf("server username = %q, want relay@target.example.com", got)
	}
}

// TestInProcessBastionKbdintCancel: CancelKbdint aborts the handshake and
// surfaces the cancel as a hop-attributed dial error.
func TestInProcessBastionKbdintCancel(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)

	kbd := em.Arm(EventVaultKbdintPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	ev := kbd.Wait(t, 15*time.Second)
	if err := m.CancelKbdint(ev.payload.(KbdintPromptPayload).ConnID); err != nil {
		t.Fatal(err)
	}
	msg := waitForErrorState(t, m, tabID)
	if !strings.Contains(msg, "jump host 1/2") || !strings.Contains(msg, "keyboard-interactive prompt cancelled") {
		t.Fatalf("error = %q, want hop-attributed cancel", msg)
	}
	assertNoPromptSlot(t, m, tabID)
}

// TestInProcessBastionKbdintTimeout: an unanswered round fails after the
// per-round PromptTimeout (plan P009 §7: ~300 ms) with the timeout
// message.
func TestInProcessBastionKbdintTimeout(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)
	m.PromptTimeout = 300 * time.Millisecond

	start := time.Now()
	kbd := em.Arm(EventVaultKbdintPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	kbd.Wait(t, 15*time.Second)
	// No submit: the per-round timer must fire.
	msg := waitForErrorState(t, m, tabID)
	elapsed := time.Since(start)
	if !strings.Contains(msg, "jump host 1/2") || !strings.Contains(msg, "keyboard-interactive prompt timed out") {
		t.Fatalf("error = %q, want hop-attributed timeout", msg)
	}
	if elapsed < 250*time.Millisecond || elapsed > 10*time.Second {
		t.Fatalf("dial took %s, want ~PromptTimeout (300 ms)", elapsed)
	}
	assertNoPromptSlot(t, m, tabID)
}

// TestInProcessBastionKbdintRunaway: a server that keeps sending question
// rounds is cut off at maxKbdintRounds (plan P009 §7: exactly 10 prompts,
// then "too many keyboard-interactive rounds").
func TestInProcessBastionKbdintRunaway(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: runawayKbdint(rec),
	})
	m, em := newManagerForRig(t, rig)

	kbd := em.Arm(EventVaultKbdintPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxKbdintRounds; i++ {
		ev := kbd.Wait(t, 15*time.Second)
		kbd = em.Arm(EventVaultKbdintPrompt)
		if err := m.SubmitKbdintResponse(ev.payload.(KbdintPromptPayload).ConnID, []string{"pw"}); err != nil {
			t.Fatal(err)
		}
	}
	msg := waitForErrorState(t, m, tabID)
	if !strings.Contains(msg, "too many keyboard-interactive rounds") {
		t.Fatalf("error = %q, want round limit", msg)
	}
	if got := rec.answerCount(); got != maxKbdintRounds {
		t.Fatalf("server accepted %d rounds, want exactly %d", got, maxKbdintRounds)
	}
	assertNoPromptSlot(t, m, tabID)
}

// TestInProcessBastionKbdintTabDisconnectMidPrompt: closing the tab while
// a round is pending unblocks the challenge (ctx cancel), ends the tab
// cleanly (connecting → closed, no error event), and leaks nothing.
func TestInProcessBastionKbdintTabDisconnectMidPrompt(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)

	base := settleGoroutines(t)
	kbd := em.Arm(EventVaultKbdintPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	kbd.Wait(t, 15*time.Second)
	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
	assertNoPromptSlot(t, m, tabID)
	sts := capturedStatuses(em, tabID)
	for _, s := range sts {
		if s == StateError {
			t.Fatalf("error status emitted on disconnect: %v", sts)
		}
	}
	if len(sts) == 0 || sts[len(sts)-1] != StateClosed {
		t.Fatalf("status stream = %v, want it to end in %q", sts, StateClosed)
	}
	if got := settleGoroutines(t); got != base {
		t.Fatalf("goroutines = %d, want %d (leak)", got, base)
	}
}

// TestInProcessBastionKbdintServerDropMidPrompt: the bastion drops the
// connection while round 1 is pending (plan P009 §7). The client's
// handshake can only observe the drop when the user resolves the open
// prompt — a submit then fails the dial (dead transport) and nothing is
// leaked on either side.
func TestInProcessBastionKbdintServerDropMidPrompt(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: dropMidPromptKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)

	base := settleGoroutines(t)
	kbd := em.Arm(EventVaultKbdintPrompt)
	tabID, err := m.Connect(bastionTestSession(rig.Addr(), "target.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	ev := kbd.Wait(t, 15*time.Second)
	// The server drops ~300 ms after the round starts; the user still
	// submits (the modal is open), which is when the dial fails.
	time.Sleep(400 * time.Millisecond)
	if err := m.SubmitKbdintResponse(ev.payload.(KbdintPromptPayload).ConnID, []string{"b-pass"}); err != nil {
		t.Fatal(err)
	}
	msg := waitForErrorState(t, m, tabID)
	if !strings.Contains(msg, "jump host 1/2") {
		t.Fatalf("error = %q, want bastion-hop attribution", msg)
	}
	assertNoPromptSlot(t, m, tabID)
	if got := settleGoroutines(t); got != base {
		t.Fatalf("goroutines = %d, want %d (leak)", got, base)
	}
}

// ---------------------------------------------------------------------
// TestConnection parity
// ---------------------------------------------------------------------

// TestInProcessBastionTestConnection: TestConnection walks the same
// kbdint prompt flow under an ephemeral connID and succeeds when both
// rounds are answered (plan P009 §7).
func TestInProcessBastionTestConnection(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)
	sess := bastionTestSession(rig.Addr(), "target.example.com")

	errCh := make(chan error, 1)
	round1 := em.Arm(EventVaultKbdintPrompt)
	go func() { errCh <- m.TestConnection(sess) }()

	ev := round1.Wait(t, 15*time.Second)
	connID := ev.payload.(KbdintPromptPayload).ConnID
	if connID == "" || connID == sess.ID {
		t.Fatalf("ephemeral connID = %q", connID)
	}
	round2 := em.Arm(EventVaultKbdintPrompt)
	if err := m.SubmitKbdintResponse(connID, []string{"b-pass"}); err != nil {
		t.Fatal(err)
	}
	ev = round2.Wait(t, 15*time.Second)
	if err := m.SubmitKbdintResponse(ev.payload.(KbdintPromptPayload).ConnID, []string{"t-pass"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if got := rec.username(); got != "relay@target.example.com" {
		t.Fatalf("server username = %q, want relay@target.example.com", got)
	}
	assertNoPromptSlot(t, m, connID)
}

// TestInProcessBastionTestConnectionFailure: a rejected answer makes
// TestConnection return the hop-attributed error and leaves no prompt
// slot.
func TestInProcessBastionTestConnectionFailure(t *testing.T) {
	rec := &kbdintRecorder{}
	rig := newTestSSHServer(t, testSSHOpts{
		kbdint: twoRoundKbdint(rec, "b-pass", "t-pass"),
	})
	m, em := newManagerForRig(t, rig)
	sess := bastionTestSession(rig.Addr(), "target.example.com")

	errCh := make(chan error, 1)
	kbd := em.Arm(EventVaultKbdintPrompt)
	go func() { errCh <- m.TestConnection(sess) }()

	ev := kbd.Wait(t, 15*time.Second)
	if err := m.SubmitKbdintResponse(ev.payload.(KbdintPromptPayload).ConnID, []string{"nope"}); err != nil {
		t.Fatal(err)
	}
	err := <-errCh
	if err == nil {
		t.Fatal("TestConnection succeeded, want failure")
	}
	if !strings.Contains(err.Error(), "jump host 1/2 (relay@target.example.com@") {
		t.Fatalf("error = %q, want bastion-hop attribution with embedded user", err)
	}
	assertNoPromptSlot(t, m, ev.payload.(KbdintPromptPayload).ConnID)
}
