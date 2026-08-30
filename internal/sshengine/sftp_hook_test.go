package sshengine

// Phase 5a engine-hook tests: Manager.SSHClient exposes the final-hop
// client SFTP needs, and the OnTabClosed hook fires after a tab's
// resources are torn down across every teardown path (Disconnect, remote
// exit, Shutdown). Uses the in-process sshd rig (testutil_test.go).

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
)

// recordCloser collects tabIDs passed to an OnTabClosed hook.
type recordCloser struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordCloser) add(id string) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
}

func (r *recordCloser) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func (r *recordCloser) has(id string) bool {
	for _, got := range r.list() {
		if got == id {
			return true
		}
	}
	return false
}

// TestSSHClientAndOnTabClosedDisconnect verifies SSHClient returns the
// ready tab's client, the OnTabClosed hook fires on Disconnect (after
// teardown), and SSHClient thereafter reports ErrUnknownTab.
func TestSSHClientAndOnTabClosedDisconnect(t *testing.T) {
	const user, password = "alice", "pw123"
	rig := newTestSSHServer(t, testSSHOpts{user: user, password: password})
	m, em := newManagerForRig(t, rig)
	rec := &recordCloser{}
	m.OnTabClosed(rec.add)

	// Unknown tab before any connect.
	if _, err := m.SSHClient("nope"); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("SSHClient(unknown) = %v, want ErrUnknownTab", err)
	}

	tabID, err := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, m, em, tabID)

	cli, err := m.SSHClient(tabID)
	if err != nil {
		t.Fatalf("SSHClient on ready tab: %v", err)
	}
	if cli == nil {
		t.Fatal("SSHClient returned nil")
	}

	if err := m.Disconnect(tabID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	waitFor(t, func() bool { return rec.has(tabID) }, "OnTabClosed after Disconnect")

	if _, err := m.SSHClient(tabID); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("SSHClient after Disconnect = %v, want ErrUnknownTab", err)
	}
}

// TestOnTabClosedRemoteExitAndShutdown verifies the hook fires when the
// remote shell exits and when Shutdown tears everything down.
func TestOnTabClosedRemoteExitAndShutdown(t *testing.T) {
	const user, password = "bob", "pw456"
	rig := newTestSSHServer(t, testSSHOpts{user: user, password: password})
	m, em := newManagerForRig(t, rig)
	rec := &recordCloser{}
	m.OnTabClosed(rec.add)

	// Remote exit path.
	tabA, _ := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	waitReady(t, m, em, tabA)
	if err := m.Write(tabA, base64.StdEncoding.EncodeToString([]byte("exit\n"))); err != nil {
		t.Fatal(err)
	}
	waitStatusState(t, em, tabA, StateClosed)
	waitFor(t, func() bool { return rec.has(tabA) }, "OnTabClosed after remote exit")

	// Shutdown path (a second live tab).
	tabB, _ := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	waitReady(t, m, em, tabB)
	m.Shutdown(context.Background())
	waitFor(t, func() bool { return rec.has(tabB) }, "OnTabClosed after Shutdown")
	if len(rec.list()) < 2 {
		t.Fatalf("expected ≥2 hook invocations, got %v", rec.list())
	}
}
