package sshengine

// Tests for DialMonitorClient (plan P004 dedicated monitoring connection):
// a ready tab yields an INDEPENDENT, working SSH client to the final hop
// that coexists with the live PTY; unknown / removed tabs fail with typed
// errors.

import (
	"bufio"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDialMonitorClientConnectsAndCoexists asserts the dedicated monitoring
// connection is usable on its own (independent session channel) AND does not
// disturb the tab's live PTY: after dialing the monitor client, terminal
// input still echoes through the tab's own channel.
func TestDialMonitorClientConnectsAndCoexists(t *testing.T) {
	const user, password = "alice", "pw123"
	rig := newTestSSHServer(t, testSSHOpts{user: user, password: password})
	m, em := newManagerForRig(t, rig)

	tabID, err := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitReady(t, m, em, tabID)

	clients, err := m.DialMonitorClient(tabID)
	if err != nil {
		t.Fatalf("DialMonitorClient: %v", err)
	}
	defer func() {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
	}()
	if len(clients) == 0 || clients[len(clients)-1] == nil {
		t.Fatal("DialMonitorClient returned no usable target client")
	}
	target := clients[len(clients)-1]

	// The dedicated client carries its OWN session channel (independent of
	// the tab's pty): request a shell and echo a line through it.
	sess, err := target.NewSession()
	if err != nil {
		t.Fatalf("NewSession on monitor client: %v", err)
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if _, err := stdin.Write([]byte("hi monitor\n")); err != nil {
		t.Fatalf("monitor session write: %v", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("monitor session read: %v", err)
	}
	if !strings.Contains(line, "echo: hi monitor") {
		t.Fatalf("monitor session echo = %q, want %q", line, "echo: hi monitor")
	}

	// The live PTY still echoes while the monitor connection is open.
	if err := m.Write(tabID, base64.StdEncoding.EncodeToString([]byte("hi pty\n"))); err != nil {
		t.Fatalf("pty Write: %v", err)
	}
	ev := em.WaitEvent(t, EventTerminalData, 15*time.Second)
	data, err := base64.StdEncoding.DecodeString(ev.payload.(TerminalDataPayload).Data)
	if err != nil {
		t.Fatalf("decode terminal data: %v", err)
	}
	if !strings.Contains(string(data), "echo: hi pty") {
		t.Fatalf("pty echo = %q, want it to contain %q", data, "echo: hi pty")
	}

	if err := m.Disconnect(tabID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
}

// TestDialMonitorClientErrors asserts the typed errors for an unknown tab
// and for a tab whose record has been removed.
func TestDialMonitorClientErrors(t *testing.T) {
	const user, password = "alice", "pw123"
	rig := newTestSSHServer(t, testSSHOpts{user: user, password: password})
	m, em := newManagerForRig(t, rig)

	if _, err := m.DialMonitorClient("nope"); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("DialMonitorClient(unknown) = %v, want ErrUnknownTab", err)
	}

	tabID, err := m.Connect(rigPasswordSession(rig.Addr(), user, password))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitReady(t, m, em, tabID)
	clients, err := m.DialMonitorClient(tabID)
	if err != nil {
		t.Fatalf("DialMonitorClient on ready tab: %v", err)
	}
	for i := len(clients) - 1; i >= 0; i-- {
		clients[i].Close()
	}

	if err := m.Disconnect(tabID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, err := m.DialMonitorClient(tabID); !errors.Is(err, ErrUnknownTab) {
		t.Fatalf("DialMonitorClient(after disconnect) = %v, want ErrUnknownTab", err)
	}
}
