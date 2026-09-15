package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"shelve/internal/app"
)

// recordedEvent is one server→client frame captured while waiting for a
// response.
type recordedEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// rpcCaller is a tiny multiplexing /rpc client: it writes numbered requests
// and reads frames until the matching response, recording any interleaved
// events.
type rpcCaller struct {
	t      *testing.T
	conn   *websocket.Conn
	events []recordedEvent
}

func (rc *rpcCaller) call(id uint64, svc, method string, args ...any) json.RawMessage {
	rc.t.Helper()
	body, err := json.Marshal(map[string]any{"id": id, "svc": svc, "method": method, "args": args})
	if err != nil {
		rc.t.Fatal(err)
	}
	writeText(rc.t, rc.conn, string(body))
	for {
		frame := readText(rc.t, rc.conn)
		var probe struct {
			ID    *uint64 `json:"id"`
			Event string  `json:"event"`
		}
		if err := json.Unmarshal(frame, &probe); err != nil {
			rc.t.Fatalf("frame: %v", err)
		}
		if probe.ID == nil {
			var ev recordedEvent
			if err := json.Unmarshal(frame, &ev); err != nil {
				rc.t.Fatalf("event: %v", err)
			}
			rc.events = append(rc.events, ev)
			continue
		}
		var resp struct {
			ID     uint64          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal(frame, &resp); err != nil {
			rc.t.Fatalf("response: %v", err)
		}
		if resp.ID != id {
			rc.t.Fatalf("response id = %d, want %d", resp.ID, id)
		}
		if resp.Error != "" {
			rc.t.Fatalf("%s.%s error: %s", svc, method, resp.Error)
		}
		return resp.Result
	}
}

func (rc *rpcCaller) sawEvent(name string) bool {
	for _, ev := range rc.events {
		if ev.Event == name {
			return true
		}
	}
	return false
}

// TestEndToEndVaultLifecycleOverRPC is the headless end-to-end probe from the
// phase verification: with no Electron and no display, a real WS client drives
// the real composition root — status → create vault → tree → events received.
func TestEndToEndVaultLifecycleOverRPC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(a.Shutdown)

	s := New(a.TerminalWS())
	s.RegisterAll(a)
	a.SetEmitter(s)
	t.Cleanup(func() { _ = s.Close() })

	addr, err := s.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rc := &rpcCaller{t: t, conn: dialURL(t, "ws://"+addr+"/rpc?token="+s.token)}

	// Fresh config dir: no vault file → the create state.
	if status := string(rc.call(1, "VaultService", "Status")); !strings.Contains(status, `"state":"create"`) {
		t.Fatalf("Status = %s, want create", status)
	}

	// Create the vault; an empty tree is then readable.
	rc.call(2, "VaultService", "CreateVault", "correct-horse-battery")
	if tree := string(rc.call(3, "SessionService", "Tree")); tree != "[]" {
		t.Fatalf("Tree = %s, want []", tree)
	}

	// Drain any still-pending frames until the vault:state-changed event
	// surfaces (the event and the response are written by different
	// goroutines, so their relative order on the socket is not fixed).
	for i := uint64(4); i < 12 && !rc.sawEvent("vault:state-changed"); i++ {
		rc.call(i, "AppService", "GetVersion")
	}
	if !rc.sawEvent("vault:state-changed") {
		t.Fatalf("vault:state-changed not received; events = %+v", rc.events)
	}
}
