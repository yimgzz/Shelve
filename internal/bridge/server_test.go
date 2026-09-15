package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"shelve/internal/termws"
)

// rpcTestService is a minimal service for transport-level round-trips.
type rpcTestService struct{}

func (rpcTestService) Echo(s string) (string, error) { return "echo:" + s, nil }

// newTestBridge starts a bridge with a mounted (listener-less) termws server
// and a trivial service, returning the server, the termws server and the bound
// address.
func newTestBridge(t *testing.T) (*Server, *termws.Server, string) {
	t.Helper()
	tw := termws.NewServer()
	s := New(tw)
	s.Register("Test", &rpcTestService{})
	addr, err := s.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, tw, addr
}

func dialURL(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
	return c
}

func writeText(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readText(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("message type = %v, want text", typ)
	}
	return data
}

// awaitRegistered performs one request/response round-trip on c. The server
// registers an accepted connection only after the upgrade handshake returns
// and the request loop starts, so a response can only be produced once the
// connection is in the fan-out set; an Emit issued afterwards cannot miss it.
func awaitRegistered(t *testing.T, c *websocket.Conn) {
	t.Helper()
	writeText(t, c, `{"id":1,"svc":"Test","method":"Echo","args":["ready"]}`)
	var resp struct {
		ID     uint64 `json:"id"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(readText(t, c), &resp); err != nil {
		t.Fatalf("registration barrier response: %v", err)
	}
	if resp.ID != 1 || resp.Result != "echo:ready" {
		t.Fatalf("registration barrier = %+v", resp)
	}
}

func TestHandshakeLine(t *testing.T) {
	s := New(nil)
	addr, err := s.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	line := s.Handshake()
	if strings.Contains(strings.TrimSpace(line), "\n") {
		t.Fatalf("handshake is not one line: %q", line)
	}
	var hs struct {
		Event string `json:"event"`
		Addr  string `json:"addr"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(line), &hs); err != nil {
		t.Fatalf("handshake not JSON (%q): %v", line, err)
	}
	if hs.Event != "ready" || hs.Addr != addr || hs.Token == "" {
		t.Fatalf("handshake = %+v, want ready/%s/<token>", hs, addr)
	}
}

func TestRPCTokenGate(t *testing.T) {
	s, _, addr := newTestBridge(t)
	base := "http://" + addr

	for _, q := range []string{"", "?token=", "?token=wrong-token"} {
		resp, err := http.Get(base + "/rpc" + q)
		if err != nil {
			t.Fatalf("GET /rpc%s: %v", q, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET /rpc%s status = %d, want 401", q, resp.StatusCode)
		}
	}

	// The right token gets past auth (the plain GET then fails the upgrade,
	// but must not be a 401).
	resp, err := http.Get(base + "/rpc?token=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("correct token rejected")
	}
}

func TestRPCRoundTrip(t *testing.T) {
	s, _, addr := newTestBridge(t)
	c := dialURL(t, "ws://"+addr+"/rpc?token="+s.token)

	writeText(t, c, `{"id":7,"svc":"Test","method":"Echo","args":["hi"]}`)
	var resp struct {
		ID     uint64 `json:"id"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(readText(t, c), &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp.ID != 7 || resp.Result != "echo:hi" || resp.Error != "" {
		t.Fatalf("response = %+v", resp)
	}

	// An unknown method comes back as an error on the same id.
	writeText(t, c, `{"id":8,"svc":"Test","method":"Nope","args":[]}`)
	resp = struct {
		ID     uint64 `json:"id"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}{}
	if err := json.Unmarshal(readText(t, c), &resp); err != nil {
		t.Fatalf("error response: %v", err)
	}
	if resp.ID != 8 || resp.Error != "unknown method Test.Nope" {
		t.Fatalf("error response = %+v", resp)
	}
}

func TestEventFanoutToAllClients(t *testing.T) {
	s, _, addr := newTestBridge(t)
	c1 := dialURL(t, "ws://"+addr+"/rpc?token="+s.token)
	c2 := dialURL(t, "ws://"+addr+"/rpc?token="+s.token)
	awaitRegistered(t, c1)
	awaitRegistered(t, c2)

	s.Emit("test:event", map[string]any{"k": "v"})

	for i, c := range []*websocket.Conn{c1, c2} {
		var frame struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(readText(t, c), &frame); err != nil {
			t.Fatalf("client %d frame: %v", i, err)
		}
		if frame.Event != "test:event" || string(frame.Data) != `{"k":"v"}` {
			t.Fatalf("client %d frame = %s / %s", i, frame.Event, frame.Data)
		}
	}
}

func TestTerminalEndpointMountedAndTokenGated(t *testing.T) {
	s, tw, addr := newTestBridge(t)

	type got struct {
		tabID string
		data  string
	}
	ch := make(chan got, 1)
	tw.SetInputHandler(func(tabID string, data []byte) error {
		ch <- got{tabID: tabID, data: string(data)}
		return nil
	})

	// No token: the upgrade is rejected before termws sees it.
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	if c, _, err := websocket.Dial(dctx, "ws://"+addr+"/terminal", nil); err == nil {
		_ = c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("tokenless /terminal upgrade succeeded")
	}

	// With the token the mounted termws server accepts the upgrade and a
	// binary frame reaches the configured input handler.
	c := dialURL(t, "ws://"+addr+"/terminal?token="+s.token)
	frame, err := termws.AppendFrame(nil, "tab9", []byte("input"))
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := c.Write(wctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	select {
	case m := <-ch:
		if m.tabID != "tab9" || m.data != "input" {
			t.Fatalf("handler got %q/%q", m.tabID, m.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input handler not called")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	_, _, addr := newTestBridge(t)
	resp, err := http.Get("http://" + addr + "/nope?token=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestStartIsIdempotent(t *testing.T) {
	s := New(nil)
	first, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	again, err := s.Start()
	if err != nil || again != first {
		t.Fatalf("second Start = %q/%v, want %q/nil", again, err, first)
	}
}
