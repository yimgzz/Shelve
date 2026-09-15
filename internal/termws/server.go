package termws

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
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

// defaultFallbackGrace bounds how long output waits for the first connection
// before routing through the fallback. It is per Server (tests shorten it via
// setFallbackGrace) so no package-global state is mutated across goroutines.
const defaultFallbackGrace = 3 * time.Second

// Server is the terminal I/O WebSocket endpoint. It owns a loopback TCP
// listener started by Start (the bridge mounts ServeHTTP listener-less and
// provisions the bound address to the renderer through the Electron
// handshake); the renderer connects to ws://127.0.0.1:<port>/terminal.
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
	// fallbackGrace bounds how long output waits for the first connection
	// before routing through the fallback (read under mu).
	fallbackGrace time.Duration
	connCh        chan struct{} // buffered(1): signaled on connection state change

	ln   net.Listener
	srv  *http.Server
	addr string // bound loopback address ("127.0.0.1:PORT"); "" until Start

	loggedAcceptErr bool // throttle accept-error logging (client retries)
	loggedFallback  bool // log the fallback engagement once
}

// NewServer creates a terminal I/O WebSocket server.
func NewServer() *Server {
	return &Server{
		startedAt:     time.Now(),
		connCh:        make(chan struct{}, 1),
		fallbackGrace: defaultFallbackGrace,
	}
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

// setFallbackGrace overrides the never-connected fallback window (tests).
func (s *Server) setFallbackGrace(d time.Duration) {
	s.mu.Lock()
	s.fallbackGrace = d
	s.mu.Unlock()
}

// Start binds the loopback listener (addr should be 127.0.0.1:0 for an
// ephemeral port) and serves /terminal WebSocket upgrades on it. Returns the
// bound address. Idempotent: a second call returns the stored address without
// re-binding.
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

// ServeHTTP handles the terminal endpoint on whichever transport it is
// mounted on:
//
//   - /terminal — upgrades to the terminal WebSocket (mounted by the bridge
//     on the token-gated loopback listener in production; any other path is a
//     plain 404).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/terminal":
		s.handleUpgrade(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	// Origin check (master plan §8.9): the renderer loads a local file or a
	// loopback dev server, so its page origin is the opaque form or
	// file:///http://127.0.0.1. coder/websocket rejects origins it cannot
	// parse into a host, so strip the opaque form first:
	//   * "null"    — an opaque page origin;
	//   * "file://" — how Chromium serializes a loadFile page origin
	//                 (Electron's renderer).
	// Everything else that is not one of the patterns — e.g. any
	// http(s):// web page origin (DNS rebinding / CSRF) — is rejected by
	// Accept below; the bridge's per-run token is the actual gate.
	switch origin := r.Header.Get("Origin"); {
	case origin == "null", strings.HasPrefix(origin, "file://"):
		r.Header.Del("Origin")
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
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
		grace := s.fallbackGrace
		deadline := s.startedAt.Add(grace)
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
					log.Printf("termws: falling back to legacy terminal events (socket never connected within %s)", grace)
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
