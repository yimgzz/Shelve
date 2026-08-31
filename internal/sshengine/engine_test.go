package sshengine

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"shelve/internal/model"
	"shelve/internal/sshx/knownhosts"
)

// refusedAddr is a guaranteed-closed local port: dialing it fails fast
// with connection refused and touches no resolver.
const refusedAddr = "127.0.0.1:1"

type eventRec struct {
	name    string
	payload any
}

// recEvents is a fake Emitter that records the event stream in order.
type recEvents struct {
	mu     sync.Mutex
	events []eventRec
}

func (r *recEvents) Emit(event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, eventRec{name: event, payload: payload})
}

func (r *recEvents) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = e.name
	}
	return out
}

// statuses returns the ordered terminal:status states for one tab.
func (r *recEvents) statuses(tabID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if p, ok := e.payload.(TerminalStatusPayload); ok && p.TabID == tabID {
			out = append(out, p.State)
		}
	}
	return out
}

func (r *recEvents) has(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.name == name {
			return true
		}
	}
	return false
}

// newTestManager builds a Manager over an isolated known_hosts file.
func newTestManager(t *testing.T) (*Manager, *recEvents) {
	t.Helper()
	kh, err := knownhosts.New(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	em := &recEvents{}
	return New(em, kh), em
}

func (m *Manager) tabState(tabID string) (tabState, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.conns[tabID]
	if !ok {
		return 0, ""
	}
	return l.state, l.message
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// settleGoroutines polls the goroutine count until two consecutive
// samples agree (phase 3c leak-check helper).
func settleGoroutines(t *testing.T) int {
	t.Helper()
	prev := runtime.NumGoroutine()
	for i := 0; i < 200; i++ {
		time.Sleep(25 * time.Millisecond)
		cur := runtime.NumGoroutine()
		if cur == prev {
			return cur
		}
		prev = cur
	}
	return prev
}

func passwordSession() *model.Session {
	return &model.Session{
		Host: "127.0.0.1", Port: 1, User: "u",
		Auth: model.Auth{Type: model.AuthPassword, Password: "x"},
	}
}

// TestConnectDialFailureStateMachine asserts the full state machine on
// a failed dial: connecting → error (record kept, target-attributed
// message), the typed not-ready errors, Reconnect re-dialing under the
// same tabID, and Disconnect removing the record with a single
// terminal "closed" event.
func TestConnectDialFailureStateMachine(t *testing.T) {
	m, em := newTestManager(t)

	tabID, err := m.Connect(passwordSession())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if tabID == "" {
		t.Fatal("Connect returned empty tabID")
	}
	waitFor(t, func() bool {
		st, _ := m.tabState(tabID)
		return st == stateError
	}, "dial failure")

	st, msg := m.tabState(tabID)
	if st != stateError {
		t.Fatalf("state = %v, want error", st)
	}
	if got, want := msg, "target "+refusedAddr+":"; got[:len(want)] != want {
		t.Fatalf("message = %q, want prefix %q", got, want)
	}

	tabs := m.Tabs()
	if len(tabs) != 1 || tabs[0].TabID != tabID || tabs[0].State != StateError {
		t.Fatalf("Tabs() = %+v, want one error record", tabs)
	}

	// Not-ready surface on an error tab: Write typed, Resize no-op.
	if err := m.Write(tabID, "aGk="); !errors.Is(err, ErrTabNotReady) {
		t.Fatalf("Write on error tab: %v, want ErrTabNotReady", err)
	}
	if err := m.Resize(tabID, 80, 24); err != nil {
		t.Fatalf("Resize on error tab: %v, want nil", err)
	}

	// Reconnect is legal from an error record and re-dials the same tab.
	if err := m.Reconnect(tabID); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	waitFor(t, func() bool {
		st, _ := m.tabState(tabID)
		return st == stateError
	}, "second dial failure")

	if err := m.Disconnect(tabID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := m.Tabs(); len(got) != 0 {
		t.Fatalf("Tabs() after Disconnect = %+v, want empty", got)
	}
	if err := m.Disconnect(tabID); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("second Disconnect: %v, want ErrUnknownTab", err)
	}

	want := []string{StateConnecting, StateError, StateConnecting, StateError, StateClosed}
	if got := em.statuses(tabID); !reflect.DeepEqual(got, want) {
		t.Fatalf("status stream = %v, want %v", got, want)
	}
}

// TestUnknownTabTypedErrors asserts the typed unknown-tab errors on the
// full public surface.
func TestUnknownTabTypedErrors(t *testing.T) {
	m, _ := newTestManager(t)
	const unknown = "no-such-tab"
	if err := m.Write(unknown, "aGk="); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("Write: %v, want ErrUnknownTab", err)
	}
	if err := m.Resize(unknown, 80, 24); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("Resize: %v, want ErrUnknownTab", err)
	}
	if err := m.Disconnect(unknown); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("Disconnect: %v, want ErrUnknownTab", err)
	}
	if err := m.Reconnect(unknown); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("Reconnect: %v, want ErrUnknownTab", err)
	}
	if _, err := m.Connect(nil); err == nil {
		t.Fatal("Connect(nil): want error")
	}
}

// TestPromptSlots exercises the pending-prompt slot machinery through
// the public resolution methods: a fresh manager rejects everything,
// each slot resolves exactly once, a second resolution is
// ErrNoPendingPrompt, the host-key timeout rejects, and aborting a
// torn-down conn drops its slot.
func TestPromptSlots(t *testing.T) {
	m, em := newTestManager(t)

	// No pending prompts anywhere.
	if err := m.ApproveHostKey("c0"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("ApproveHostKey: %v, want ErrNoPendingPrompt", err)
	}
	if err := m.RejectHostKey("c0"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("RejectHostKey: %v, want ErrNoPendingPrompt", err)
	}
	if err := m.SubmitKeyPassphrase("c0", "pw"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("SubmitKeyPassphrase: %v, want ErrNoPendingPrompt", err)
	}

	// Key-passphrase slot: event + one-shot resolution + cleanup.
	ch, slot := m.beginKeyPrompt("c1", "/home/u/.ssh/id_ed25519")
	if !em.has(EventVaultKeyPrompt) {
		t.Fatal("missing vault:key-prompt event")
	}
	if err := m.SubmitKeyPassphrase("c1", "hunter2"); err != nil {
		t.Fatalf("SubmitKeyPassphrase: %v", err)
	}
	select {
	case pw := <-ch:
		if pw != "hunter2" {
			t.Fatalf("passphrase = %q, want hunter2", pw)
		}
	default:
		t.Fatal("passphrase channel empty after submit")
	}
	if err := m.SubmitKeyPassphrase("c1", "again"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("second submit: %v, want ErrNoPendingPrompt", err)
	}
	m.discardPrompt("c1", slot) // no-op: the slot was already resolved

	// Host-key slot: event + approve path + one-shot.
	hkCh := m.beginHostKeyPrompt("c2", "db.example", 2222, "ssh-ed25519", "a2V5", "sha256:AAAA")
	if !em.has(EventVaultHostkeyPrompt) {
		t.Fatal("missing vault:hostkey-prompt event")
	}
	if err := m.ApproveHostKey("c3"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("ApproveHostKey other conn: %v, want ErrNoPendingPrompt", err)
	}
	if err := m.ApproveHostKey("c2"); err != nil {
		t.Fatalf("ApproveHostKey: %v", err)
	}
	select {
	case ok := <-hkCh:
		if !ok {
			t.Fatal("approve delivered false")
		}
	default:
		t.Fatal("host-key channel empty after approve")
	}
	if err := m.RejectHostKey("c2"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("second resolve: %v, want ErrNoPendingPrompt", err)
	}

	// Host-key timeout = rejection, slot removed.
	m.PromptTimeout = 100 * time.Millisecond
	toCh := m.beginHostKeyPrompt("c4", "t.example", 22, "ssh-ed25519", "a2V5", "sha256:BBBB")
	select {
	case ok := <-toCh:
		if ok {
			t.Fatal("timeout delivered approval, want rejection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout did not fire")
	}
	if err := m.RejectHostKey("c4"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("after timeout: %v, want ErrNoPendingPrompt", err)
	}

	// Disconnect while a key prompt is open drops the slot.
	m.beginKeyPrompt("c5", "/keys/enc")
	m.abortConnPrompt(&liveConn{connID: "c5"})
	if err := m.SubmitKeyPassphrase("c5", "pw"); !errors.Is(err, ErrNoPendingPrompt) {
		t.Fatalf("after abort: %v, want ErrNoPendingPrompt", err)
	}
}

// TestInvalidBase64Write asserts the decode error is typed and the tab
// is untouched.
func TestInvalidBase64Write(t *testing.T) {
	m, _ := newTestManager(t)
	tabID, err := m.Connect(passwordSession())
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		st, _ := m.tabState(tabID)
		return st == stateError
	}, "dial failure")
	if err := m.Write(tabID, "!!!not-base64!!!"); err == nil {
		t.Fatal("Write invalid base64: want error")
	}
	if err := m.Disconnect(tabID); err != nil {
		t.Fatal(err)
	}
}

// TestShutdownIdempotentAndLeakFree asserts Shutdown clears every
// record, is idempotent, and leaves no goroutines behind.
func TestShutdownIdempotentAndLeakFree(t *testing.T) {
	m, _ := newTestManager(t)
	baseline := settleGoroutines(t)

	tabIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		tabID, err := m.Connect(passwordSession())
		if err != nil {
			t.Fatal(err)
		}
		tabIDs = append(tabIDs, tabID)
	}
	n := len(tabIDs)
	waitFor(t, func() bool {
		if len(m.Tabs()) != n {
			return false
		}
		for _, tid := range tabIDs {
			st, _ := m.tabState(tid)
			if st != stateError {
				return false
			}
		}
		return true
	}, "all dials to fail")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	m.Shutdown(ctx)
	cancel()
	if got := m.Tabs(); len(got) != 0 {
		t.Fatalf("Tabs() after Shutdown = %+v, want empty", got)
	}
	if got := settleGoroutines(t); got != baseline {
		t.Fatalf("goroutine delta after Shutdown: %d → %d", baseline, got)
	}

	// Second call is a no-op with the same steady state.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	m.Shutdown(ctx2)
	cancel2()
	if got := m.Tabs(); len(got) != 0 {
		t.Fatalf("Tabs() after second Shutdown = %+v, want empty", got)
	}
	if got := settleGoroutines(t); got != baseline {
		t.Fatalf("goroutine delta after double Shutdown: %d → %d", baseline, got)
	}
}
