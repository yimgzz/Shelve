package termws

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// InputHandler receives one decoded input frame from the frontend.
type InputHandler func(tabID string, data []byte) error

// OutputFallback is called instead of blocking when no connection has EVER
// been established within fallbackGrace of start (plan P005 safety net: if
// the webview cannot reach the loopback socket — firewall, sandbox, broken
// build — output falls back to the legacy emitter path instead of stalling
// the tabs forever).
type OutputFallback func(tabID string, data []byte)

// fallbackGrace bounds how long output waits for the first connection
// before routing through the fallback (var so tests can shorten it).
var fallbackGrace = 3 * time.Second

// Server is the terminal I/O WebSocket endpoint. It owns a loopback TCP
// listener started by Start — a WebSocket cannot be spoken over the
// wails:// custom URI scheme the webview loads (Wails' own stream transport
// documents exactly this), so a real listener is the ONLY way to get a
// socket into the page. The frontend discovers the bound address with a
// plain GET /termws-port on the wails:// asset handler (served via
// ServeHTTP) and connects to ws://127.0.0.1:<port>/terminal.
//
// It implements the engine's TerminalDataSink: OnTerminalData blocks until
// a connection is active and then delivers — blocking IS the flow control
// (when the browser socket buffer is full, or the webview is disconnected,
// output pauses here and the engine's reader backpressure throttles the
// remote). Exactly one connection is active (single-window app, master plan
// A7); a second upgrade is rejected.
type Server struct {
	mu        sync.Mutex
	conn      *conn
	closed    bool
	everConn  bool // a connection has been established at least once
	startedAt time.Time
	input     InputHandler
	fallback  OutputFallback
	connCh    chan struct{} // buffered(1): signaled on connection state change

	ln   net.Listener
	srv  *http.Server
	addr string // bound loopback address ("127.0.0.1:PORT"); "" until Start

	loggedAcceptErr bool // throttle accept-error logging (client retries)
	loggedFallback  bool // log the fallback engagement once
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

// Start binds the loopback listener (addr should be 127.0.0.1:0 for an
// ephemeral port) and serves /terminal WebSocket upgrades on it; the same
// ServeHTTP also answers GET /termws-port for the frontend to learn the
// address. Returns the bound address. Idempotent: a second call returns the
// stored address without re-binding.
func (s *Server) Start(addr string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.addr, nil
	}
	if s.closed {
		return "", errors.New("termws: server closed")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.srv = &http.Server{Handler: s}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				log.Printf("termws: loopback listener: %v", err)
			}
		}
	}()
	log.Printf("termws: terminal WebSocket listening on %s", s.addr)
	return s.addr, nil
}

// Addr returns the bound loopback address, or "" if Start has not succeeded.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// ServeHTTP handles the endpoints on whichever transport it is mounted on:
//
//   - /termws-port — GET, returns the loopback address as plain text. Served
//     on the wails:// asset handler so the webview can provision the socket.
//   - /terminal — upgrades to the terminal WebSocket (on the loopback
//     listener in production; any other path is a plain 404).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/termws-port":
		s.handlePort(w, r)
	case "/terminal":
		s.handleUpgrade(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handlePort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	addr := s.Addr()
	if addr == "" {
		http.Error(w, "termws: not listening", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(addr))
}

func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	// Origin check (master plan §8.9): the webview's page origin is the
	// wails:// custom scheme (host part varies by build — wails://* covers
	// it), and loopback origins cover the coder test client / dev servers.
	// WebKitGTK may serialize the custom-scheme page origin as the literal
	// header "null" (opaque); coder/websocket cannot match that value
	// (url.Parse("null") has no host and authenticateOrigin rejects it), so
	// strip it first. Everything else that is not one of the patterns —
	// e.g. any http(s):// web page origin (DNS rebinding / CSRF) — is
	// rejected by Accept below.
	if r.Header.Get("Origin") == "null" {
		r.Header.Del("Origin")
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"wails://*", "localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		s.mu.Lock()
		if !s.loggedAcceptErr {
			s.loggedAcceptErr = true
			log.Printf("termws: /terminal upgrade rejected: %v", err)
		}
		s.mu.Unlock()
		return // Accept already wrote the error response
	}
	s.attach(c)
}

// attach registers the new connection as the single active one.
func (s *Server) attach(c *websocket.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = c.Close(websocket.StatusNormalClosure, "server closing")
		return
	}
	if s.conn != nil {
		s.mu.Unlock()
		_ = c.Close(websocket.StatusPolicyViolation, "single connection only")
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
	addr := s.addr
	s.mu.Unlock()

	// Input frames are capped by the sender (frontend splits), so a larger
	// read is a malformed/malicious peer — drop the connection.
	c.SetReadLimit(maxPayload + 1024)
	if addr != "" {
		log.Printf("termws: webview connected (%s)", addr)
	}

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
		// not reach the loopback listener), route through the fallback
		// instead of blocking the tabs indefinitely. Once a connection has
		// existed, blocking remains correct (transient drops resume on
		// reconnect).
		if !ever && fb != nil {
			if !time.Now().Before(deadline) {
				s.mu.Lock()
				if !s.loggedFallback {
					s.loggedFallback = true
					log.Printf("termws: falling back to legacy terminal events (socket never connected within %s)", fallbackGrace)
				}
				s.mu.Unlock()
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

// Close drops the active connection, stops the loopback listener, and
// unblocks every waiter.
func (s *Server) Close() error {
	s.mu.Lock()
	nc := s.conn
	s.conn = nil
	s.closed = true
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	s.signalConn()
	if nc != nil {
		nc.teardown()
	}
	if ln != nil {
		_ = ln.Close()
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
// browser isn't consuming) is the intended flow control; teardown releases
// it on close.
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
