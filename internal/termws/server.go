package termws

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// InputHandler receives one decoded input frame from the frontend.
type InputHandler func(tabID string, data []byte) error

// OutputFallback is called instead of blocking when no connection has EVER
// been established within fallbackGrace of start (plan P005 safety net: if
// the webview cannot upgrade /terminal — e.g. the transport wraps the
// ResponseWriter without hijack support — output falls back to the legacy
// emitter path instead of stalling the tabs forever).
type OutputFallback func(tabID string, data []byte)

// fallbackGrace bounds how long output waits for the first connection
// before routing through the fallback (var so tests can shorten it).
var fallbackGrace = 15 * time.Second

// Server is the terminal I/O WebSocket endpoint, mounted on the app's HTTP
// transport at /terminal (same-origin as the webview). It implements the
// engine's TerminalDataSink: OnTerminalData blocks until a connection is
// active and then delivers — blocking IS the flow control (when the browser
// socket buffer is full, or the webview is disconnected, output pauses here
// and the engine's reader backpressure throttles the remote).
type Server struct {
	mu        sync.Mutex
	conn      *conn
	closed    bool
	everConn  bool // a connection has been established at least once
	startedAt time.Time
	input     InputHandler
	fallback  OutputFallback
	connCh    chan struct{} // buffered(1): signaled on connection state change
}

// NewServer creates a terminal I/O WebSocket server.
func NewServer() *Server {
	return &Server{startedAt: time.Now(), connCh: make(chan struct{}, 1)}
}

// SetInputHandler installs the engine write path for input frames.
func (s *Server) SetInputHandler(fn InputHandler) {
	s.mu.Lock()
	s.input = fn
	s.mu.Unlock()
}

// SetFallback installs the legacy output path used when the socket has
// never connected within fallbackGrace (see OutputFallback).
func (s *Server) SetFallback(fn OutputFallback) {
	s.mu.Lock()
	s.fallback = fn
	s.mu.Unlock()
}

// ServeHTTP upgrades /terminal to a WebSocket; any other path is a plain
// 404 so the asset handler wrapper can delegate.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/terminal" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "wails.localhost:*", "null"},
	})
	if err != nil {
		return // Accept already wrote the error response
	}
	s.attach(c)
}

// attach registers the new connection as the single active one.
func (s *Server) attach(c *websocket.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close(websocket.StatusNormalClosure, "server closing")
		return
	}
	if s.conn != nil {
		s.mu.Unlock()
		c.Close(websocket.StatusPolicyViolation, "single connection only")
		return
	}
	nc := &conn{
		srv:  s,
		ws:   c,
		out:  make(chan []byte, 16),
		done: make(chan struct{}),
	}
	s.conn = nc
	s.everConn = true
	s.signalConn()
	s.mu.Unlock()

	go nc.writeLoop()
	go nc.readLoop()
}

// signalConn wakes OnTerminalData waiters after a connection state change.
func (s *Server) signalConn() {
	select {
	case s.connCh <- struct{}{}:
	default:
	}
}

// OnTerminalData implements sshengine.TerminalDataSink.
func (s *Server) OnTerminalData(tabID string, data []byte) {
	for {
		s.mu.Lock()
		nc := s.conn
		closed := s.closed
		ever := s.everConn
		fb := s.fallback
		deadline := s.startedAt.Add(fallbackGrace)
		s.mu.Unlock()
		if closed {
			return
		}
		if nc != nil {
			if err := nc.deliver(tabID, data); err == nil {
				return
			}
			// Connection died mid-write: loop to wait for a fresh one.
			continue
		}
		// Safety net: if the socket has never connected (the webview could
		// not upgrade /terminal), route through the fallback instead of
		// blocking the tabs indefinitely. Once a connection has existed,
		// blocking remains correct (transient drops resume on reconnect).
		if !ever && fb != nil {
			if !time.Now().Before(deadline) {
				fb(tabID, data)
				return
			}
			// Wait for a connection, a Close, or the fallback deadline —
			// the wait MUST be bounded or a never-connecting webview
			// stalls every tab forever.
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-s.connCh:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		// No connection yet (or dropped): wait for one or for Close.
		<-s.connCh
	}
}

// Close drops the active connection and unblocks every waiter.
func (s *Server) Close() error {
	s.mu.Lock()
	nc := s.conn
	s.conn = nil
	s.closed = true
	s.mu.Unlock()
	s.signalConn()
	if nc != nil {
		nc.teardown()
	}
	return nil
}

// conn is one accepted connection: a writer goroutine drains out; a reader
// goroutine feeds input frames to the server.
type conn struct {
	srv       *Server
	ws        *websocket.Conn
	out       chan []byte
	closeOnce sync.Once
	done      chan struct{}
}

// deliver encodes (splitting oversize payloads) and queues output frames.
// It blocks while the browser socket buffer is full (backpressure) and
// returns an error only when the connection is being torn down.
func (nc *conn) deliver(tabID string, data []byte) error {
	for len(data) > 0 {
		n := len(data)
		if n > maxPayload {
			n = maxPayload
		}
		frame, err := AppendFrame(nil, tabID, data[:n])
		if err != nil {
			return err
		}
		select {
		case nc.out <- frame:
		case <-nc.done:
			return errors.New("termws: connection closing")
		}
		data = data[n:]
	}
	return nil
}

// writeLoop serializes output frames onto the socket. A blocked Write (the
// browser isn't consuming) is the intended flow control; ctx from teardown
// releases it on close.
func (nc *conn) writeLoop() {
	for {
		select {
		case frame := <-nc.out:
			if err := nc.ws.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
				nc.teardown()
				return
			}
		case <-nc.done:
			return
		}
	}
}

// readLoop routes input frames to the engine write path.
func (nc *conn) readLoop() {
	defer nc.teardown()
	for {
		typ, data, err := nc.ws.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		tabID, payload, err := DecodeFrame(data)
		if err != nil {
			continue // malformed input is dropped
		}
		nc.srv.mu.Lock()
		fn := nc.srv.input
		nc.srv.mu.Unlock()
		if fn != nil {
			_ = fn(tabID, payload) // engine rejects unknown tabs; drop
		}
	}
}

// teardown detaches the connection and releases blocked writers/readers.
func (nc *conn) teardown() {
	nc.closeOnce.Do(func() {
		nc.srv.mu.Lock()
		if nc.srv.conn == nc {
			nc.srv.conn = nil
		}
		nc.srv.mu.Unlock()
		nc.srv.signalConn()
		close(nc.done)
		nc.ws.CloseNow()
	})
}
