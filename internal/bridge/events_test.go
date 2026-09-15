package bridge

import (
	"encoding/json"
	"testing"
	"time"

	"shelve/internal/api"
	"shelve/internal/monitor"
	"shelve/internal/sftp"
	"shelve/internal/sshengine"
)

// TestEmitEventContract guards the master plan §5 event contract: every event
// name the backend produces rides /rpc as {"event":…,"data":…} with the exact
// payload field names (a rename here breaks the renderer, so the expected JSON
// is hard-coded).
func TestEmitEventContract(t *testing.T) {
	s, _, addr := newTestBridge(t)
	c := dialURL(t, "ws://"+addr+"/rpc?token="+s.token)
	awaitRegistered(t, c)

	cases := []struct {
		event   string
		payload any
		want    string
	}{
		{
			api.EventVaultStateChanged,
			api.VaultStatePayload{Unlocked: true},
			`{"unlocked":true}`,
		},
		{
			sshengine.EventVaultHostkeyPrompt,
			sshengine.HostKeyPromptPayload{
				ConnID: "c1", Host: "h.example.com", Port: 22, KeyType: "ssh-ed25519",
				KeyB64: "AAA", Fingerprint: "SHA256:abc",
			},
			`{"connID":"c1","host":"h.example.com","port":22,"keyType":"ssh-ed25519","keyB64":"AAA","fingerprint":"SHA256:abc"}`,
		},
		{
			sshengine.EventVaultKeyPrompt,
			sshengine.KeyPromptPayload{ConnID: "c1", KeyPath: "/home/u/.ssh/id_ed25519"},
			`{"connID":"c1","keyPath":"/home/u/.ssh/id_ed25519"}`,
		},
		{
			sshengine.EventVaultKbdintPrompt,
			sshengine.KbdintPromptPayload{ConnID: "c1", Questions: []string{"Password:"}, Echo: []bool{false}},
			`{"connID":"c1","questions":["Password:"],"echo":[false]}`,
		},
		{
			sshengine.EventTerminalStatus,
			sshengine.TerminalStatusPayload{TabID: "t1", State: sshengine.StateReady},
			`{"tabID":"t1","state":"ready"}`,
		},
		{
			sshengine.EventTerminalData,
			sshengine.TerminalDataPayload{TabID: "t1", Data: "aGk="},
			`{"tabID":"t1","data":"aGk="}`,
		},
		{
			sshengine.EventTerminalExit,
			sshengine.TerminalExitPayload{TabID: "t1"},
			`{"tabID":"t1"}`,
		},
		{
			sshengine.EventForward,
			sshengine.ForwardPayload{TabID: "t1", Spec: "-L 8080:localhost:80", State: sshengine.ForwardListening, LocalAddr: "127.0.0.1:8080"},
			`{"tabID":"t1","spec":"-L 8080:localhost:80","state":"listening","localAddr":"127.0.0.1:8080"}`,
		},
		{
			sftp.EventProgress,
			sftp.ProgressPayload{TransferID: "x", TabID: "t1", Direction: "up", FileName: "f", DoneBytes: 1, TotalBytes: 2},
			`{"transferID":"x","tabID":"t1","direction":"up","fileName":"f","doneBytes":1,"totalBytes":2}`,
		},
		{
			monitor.EventMonitorMetrics,
			monitor.MetricsPayload{
				TabID: "t1", Hostname: "h", CPUPercent: 1.5,
				MemUsedBytes: 100, MemTotalBytes: 200, NetUpBps: 1, NetDownBps: 2,
				UptimeSeconds: 3, DiskUsedPct: 4.5, DiskRoot: "/", DfText: "x",
			},
			`{"tabID":"t1","hostname":"h","cpuPercent":1.5,"memUsedBytes":100,"memTotalBytes":200,"netUpBps":1,"netDownBps":2,"uptimeSeconds":3,"diskUsedPct":4.5,"diskRoot":"/","dfText":"x"}`,
		},
		{
			sftp.EventToast,
			sftp.ToastPayload{Level: "info", Message: "m"},
			`{"level":"info","message":"m"}`,
		},
	}

	seen := make(map[string]bool, len(cases))
	for _, tc := range cases {
		s.Emit(tc.event, tc.payload)
		var frame struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(readText(t, c), &frame); err != nil {
			t.Fatalf("event %s: %v", tc.event, err)
		}
		if frame.Event != tc.event {
			t.Fatalf("event name = %q, want %q", frame.Event, tc.event)
		}
		if string(frame.Data) != tc.want {
			t.Fatalf("event %s data = %s, want %s", tc.event, frame.Data, tc.want)
		}
		seen[tc.event] = true
	}

	// The set of names must be exactly the §5 backend surface.
	want := []string{
		"vault:state-changed", "vault:hostkey-prompt", "vault:key-prompt",
		"vault:kbdint-prompt", "terminal:status", "terminal:data",
		"terminal:exit", "ssh:forward", "sftp:progress", "monitor:metrics",
		"app:toast",
	}
	if len(seen) != len(want) {
		t.Fatalf("emitted %d distinct event names, want %d: %v", len(seen), len(want), seen)
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("event name %q missing from the contract", name)
		}
	}
}

// TestEmitBeforeAnyClientIsDropped documents the LateEmitter-equivalent
// behavior: events emitted before a client connects are dropped, never queued
// and never an error.
func TestEmitBeforeAnyClientIsDropped(t *testing.T) {
	s := New(nil)
	s.Emit("app:toast", map[string]any{"level": "info", "message": "pre-connection"})
}

// TestCloseReleasesBlockedEmit proves a full per-client queue blocks Emit
// (lifecycle events are not dropped) and that Close releases the waiter.
func TestCloseReleasesBlockedEmit(t *testing.T) {
	s := New(nil)
	c := &rpcClient{srv: s, out: make(chan []byte, 1), done: make(chan struct{})}
	s.addClient(c)
	c.out <- []byte("occupied") // fill the queue so the next enqueue blocks

	emitted := make(chan struct{})
	go func() {
		s.Emit("app:toast", map[string]any{"level": "info", "message": "blocked"})
		close(emitted)
	}()

	select {
	case <-emitted:
		t.Fatal("Emit did not block on a full client queue")
	case <-time.After(100 * time.Millisecond):
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-emitted:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release a blocked Emit")
	}
}
