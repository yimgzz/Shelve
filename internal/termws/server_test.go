package termws

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startServer mounts a Server on an httptest transport.
func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := NewServer()
	ts := httptest.NewServer(s)
	t.Cleanup(func() {
		_ = s.Close()
		ts.Close()
	})
	return s, ts.URL
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func TestOutputFrameDelivered(t *testing.T) {
	s, base := startServer(t)
	c := dialWS(t, base+"/terminal")
	defer c.Close(websocket.StatusNormalClosure, "")

	go s.OnTerminalData("tab1", []byte("hello"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type = %v, want binary", typ)
	}
	tabID, payload, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tabID != "tab1" || string(payload) != "hello" {
		t.Fatalf("frame = %q/%q, want tab1/hello", tabID, payload)
	}
}

func TestInputFrameRoutesToHandler(t *testing.T) {
	s, base := startServer(t)
	type got struct {
		tabID string
		data  []byte
	}
	ch := make(chan got, 1)
	s.SetInputHandler(func(tabID string, data []byte) error {
		ch <- got{tabID, data}
		return nil
	})
	c := dialWS(t, base+"/terminal")
	defer c.Close(websocket.StatusNormalClosure, "")

	frame, err := AppendFrame(nil, "tab9", []byte("input"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case m := <-ch:
		if m.tabID != "tab9" || string(m.data) != "input" {
			t.Fatalf("handler got %q/%q", m.tabID, m.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input handler not called")
	}
}

func TestMalformedInputDropped(t *testing.T) {
	s, base := startServer(t)
	called := false
	s.SetInputHandler(func(tabID string, data []byte) error {
		called = true
		return nil
	})
	c := dialWS(t, base+"/terminal")
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A frame with a zero tabID length must be dropped, not routed.
	if err := c.Write(ctx, websocket.MessageBinary, []byte{0x00, 0x01, 0x02}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if called {
		t.Fatal("malformed input reached the handler")
	}
}

func TestSecondConnectionRejected(t *testing.T) {
	s, base := startServer(t)
	c1 := dialWS(t, base+"/terminal")
	defer c1.Close(websocket.StatusNormalClosure, "")

	// The server accepts the upgrade and then closes it with a policy
	// violation; the second client only observes that on its first read.
	c2 := dialWS(t, base+"/terminal")
	defer c2.Close(websocket.StatusAbnormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c2.Read(ctx); err == nil {
		t.Fatal("second connection was not closed by the server")
	}

	// And the first connection keeps delivering.
	go s.OnTerminalData("t", []byte("still-alive"))
	typ, data, err := c1.Read(ctx)
	if err != nil {
		t.Fatalf("first connection broke: %v", err)
	}
	if typ != websocket.MessageBinary || !bytes.Contains(data, []byte("still-alive")) {
		t.Fatalf("first connection payload = %q", data)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	s, base := startServer(t)
	_ = s
	resp, err := http.Get(base + "/other")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCloseUnblocksSink(t *testing.T) {
	s, base := startServer(t)
	_ = base
	done := make(chan struct{})
	go func() {
		// No connection yet: OnTerminalData must block, then return on Close.
		s.OnTerminalData("t", []byte("x"))
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTerminalData not unblocked by Close")
	}
}

func TestOversizePayloadSplit(t *testing.T) {
	s, base := startServer(t)
	c := dialWS(t, base+"/terminal")
	defer c.Close(websocket.StatusNormalClosure, "")
	c.SetReadLimit(maxPayload + 1024) // message = payload + framing header

	big := make([]byte, maxPayload+1000)
	for i := range big {
		big[i] = byte(i % 251)
	}
	go s.OnTerminalData("t", big)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var total []byte
	for len(total) < len(big) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %d of %d)", err, len(total), len(big))
		}
		_, p, err := DecodeFrame(data)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		total = append(total, p...)
	}
	if !bytes.Equal(total, big) {
		t.Fatal("payload mismatch after splitting")
	}
}

func TestFallbackWhenNeverConnected(t *testing.T) {
	old := fallbackGrace
	fallbackGrace = 50 * time.Millisecond
	t.Cleanup(func() { fallbackGrace = old })

	s := NewServer()
	called := make(chan []byte, 1)
	s.SetFallback(func(tabID string, data []byte) {
		called <- append([]byte(nil), data...)
	})
	s.OnTerminalData("t", []byte("fb"))
	select {
	case d := <-called:
		if string(d) != "fb" {
			t.Fatalf("fallback data = %q", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fallback not invoked after grace")
	}
}

func TestStartBindsLoopbackAndServes(t *testing.T) {
	s := NewServer()
	addr, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if addr == "" {
		t.Fatal("empty address")
	}
	t.Cleanup(func() { _ = s.Close() })

	// A second Start is idempotent and reports the same address.
	if again, err := s.Start("127.0.0.1:0"); err != nil || again != addr {
		t.Fatalf("second start = %q/%v, want %q/nil", again, err, addr)
	}

	c := dialWS(t, "ws://"+addr+"/terminal")
	defer c.Close(websocket.StatusNormalClosure, "")

	go s.OnTerminalData("t", []byte("loopback"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, p, err := DecodeFrame(data); err != nil || string(p) != "loopback" {
		t.Fatalf("frame decode: %v, payload %q", err, p)
	}
}

func TestTermWSPortEndpoint(t *testing.T) {
	s, base := startServer(t)

	// Not started yet: the endpoint reports 503.
	resp, err := http.Get(base + "/termws-port")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status before start = %d, want 503", resp.StatusCode)
	}

	addr, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	resp, err = http.Get(base + "/termws-port")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != addr {
		t.Fatalf("address = %q, want %q", got, addr)
	}

	// Non-GET is rejected.
	req, _ := http.NewRequest(http.MethodPost, base+"/termws-port", nil)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", r2.StatusCode)
	}
}

func TestCloseStopsLoopbackListener(t *testing.T) {
	s := NewServer()
	addr, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := websocket.Dial(ctx, "ws://"+addr+"/terminal", nil); err == nil {
		t.Fatal("dial succeeded after Close")
	}
}

func TestOriginPolicy(t *testing.T) {
	_, base := startServer(t)

	// A foreign web page origin must be rejected.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, base+"/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
	})
	if err == nil {
		t.Fatal("foreign origin was accepted")
	}

	// The wails:// custom-scheme page origin must be accepted.
	c, _, err := websocket.Dial(ctx, base+"/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"wails://wails"}},
	})
	if err != nil {
		t.Fatalf("wails:// origin rejected: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// An opaque ("null") origin — how WebKitGTK may serialize the page
	// origin — must be accepted too.
	c2, _, err := websocket.Dial(ctx, base+"/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"null"}},
	})
	if err != nil {
		t.Fatalf("null origin rejected: %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")

	// The Electron renderer's loadFile page origin ("file://") must be
	// accepted: Chromium serializes file:// pages this way and
	// coder/websocket cannot match an origin without a host.
	c3, _, err := websocket.Dial(ctx, base+"/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"file://"}},
	})
	if err != nil {
		t.Fatalf("file:// origin rejected: %v", err)
	}
	defer c3.Close(websocket.StatusNormalClosure, "")
}
