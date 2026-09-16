// Package bridge is the token-gated loopback transport between the Shelve
// backend and its renderer (master plan §5). One http.Server on 127.0.0.1:0
// serves two WebSocket endpoints:
//
//   - /rpc      — text JSON request/response frames plus server→client
//     events, dispatched by reflection over the registered api services
//     (see dispatch.go);
//   - /terminal — the plan P005 binary terminal framing, delegated verbatim
//     to internal/termws (which runs listener-less here).
//
// Both endpoints require the per-run token minted by New. The entry point
// prints Handshake() on stdout so the Electron main process can provision
// {addr,token} to the renderer; the listener is loopback-only (§8.9).
package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"shelve/internal/termws"
)

// eventBuffer is the per-client /rpc event queue depth. A full queue blocks
// Emit rather than dropping a lifecycle event; terminal bytes do not use this
// path (the /terminal sink is authoritative), so the queue stays low-rate.
const eventBuffer = 4096

// blockingConcurrency bounds concurrent executions of the allow-listed
// long-running RPC methods per client. A multi-GB SFTP transfer or a dial
// timeout must not starve the rest of the surface, but a flooding client must
// not spawn unbounded goroutines either — readLoop waits for a slot, so the
// goroutine count stays bounded.
const blockingConcurrency = 4

// blockingMethods is the allow-list of RPC methods known to block for the
// whole duration of a transfer/dial (api/sftp_service.go, sessionservice.go).
// They are dispatched on bounded goroutines so the read loop keeps serving
// fast calls; every other method stays fully sequential. The client matches
// responses by id, so completion order is irrelevant.
var blockingMethods = map[string]struct{}{
	"SftpService.Upload":            {},
	"SftpService.Download":          {},
	"SftpService.DownloadTo":        {},
	"SessionService.TestConnection": {},
}

// isBlockingMethod reports whether (svc, method) is on the async allow-list.
func isBlockingMethod(svc, method string) bool {
	_, ok := blockingMethods[svc+"."+method]
	return ok
}

// Server owns the loopback listener, the per-run token, the RPC service
// registry and the connected /rpc clients. It implements api.Emitter.
type Server struct {
	termwsSrv *termws.Server
	token     string

	mu       sync.RWMutex
	ln       net.Listener
	srv      *http.Server
	addr     string
	closed   bool
	clients  map[*rpcClient]struct{}
	services map[string]*serviceEntry
}

// New creates the bridge and mints its per-run token. termwsSrv may be nil
// (tests that only exercise RPC dispatch); when set it is mounted at
// /terminal.
func New(termwsSrv *termws.Server) *Server {
	return &Server{
		termwsSrv: termwsSrv,
		token:     newToken(),
		clients:   make(map[*rpcClient]struct{}),
		services:  make(map[string]*serviceEntry),
	}
}

// newToken mints the per-run bearer token (32 random bytes, base64url, no
// padding).
func newToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is unrecoverable and has no safe fallback.
		panic("bridge: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Addr returns the bound loopback address, or "" before a successful Start.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Start binds 127.0.0.1:0 and serves the bridge. Idempotent: a second call
// returns the stored address without re-binding.
func (s *Server) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.addr, nil
	}
	if s.closed {
		return "", errors.New("bridge: server closed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	srv := s.srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				log.Printf("bridge: loopback listener: %v", err)
			}
		}
	}()
	return s.addr, nil
}

// Handshake returns the one-line stdout JSON handshake the Electron main
// process parses to provision the renderer.
func (s *Server) Handshake() string {
	b, err := json.Marshal(handshake{
		Event: "ready",
		Addr:  s.Addr(),
		Token: s.token,
	})
	if err != nil {
		// Marshaling a plain string struct cannot fail.
		panic("bridge: handshake marshal: " + err.Error())
	}
	return string(b)
}

// handshake is the stdout ready line (master plan §5).
type handshake struct {
	Event string `json:"event"`
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// ServeHTTP routes the two endpoints on the loopback listener; every other
// path is a plain 404.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/rpc":
		if !s.authenticate(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.handleRPC(w, r)
	case "/terminal":
		if !s.authenticate(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if s.termwsSrv == nil {
			http.NotFound(w, r)
			return
		}
		s.termwsSrv.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// authenticate gates an upgrade on the per-run token (§8.9). The token is the
// only gate: no cookies, no ambient browser credentials.
func (s *Server) authenticate(r *http.Request) bool {
	got := r.URL.Query().Get("token")
	if got == "" || s.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// handleRPC accepts one /rpc WebSocket and runs its request loop; responses
// are written inline while a separate goroutine drains the event queue.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	// The token is validated before the upgrade. Browser origin checks are
	// deliberately not the gate here: the renderer's origin is provisioned by
	// the shell and not known at this layer, and the token already gates
	// access (§8.9).
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return // Accept already wrote the error response
	}
	nc := &rpcClient{
		srv:         s,
		ws:          c,
		out:         make(chan []byte, eventBuffer),
		done:        make(chan struct{}),
		blockingSem: make(chan struct{}, blockingConcurrency),
	}
	s.addClient(nc)
	go nc.writeLoop()
	nc.readLoop()
	nc.teardown()
}

func (s *Server) addClient(c *rpcClient) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.teardown()
		return
	}
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeClient(c *rpcClient) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

// Close drops every /rpc client, stops the listener and closes the mounted
// terminal server. Idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.ln
	s.ln = nil
	srv := s.srv
	s.srv = nil
	clients := make([]*rpcClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		c.teardown()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if srv != nil {
		_ = srv.Close()
	}
	if s.termwsSrv != nil {
		_ = s.termwsSrv.Close()
	}
	return nil
}

// rpcClient is one accepted /rpc connection. readLoop handles request frames
// sequentially, except for the allow-listed blocking methods which run on
// bounded goroutines (blockingSem); writeLoop is the single socket writer and
// drains both response and event frames.
type rpcClient struct {
	srv         *Server
	ws          *websocket.Conn
	out         chan []byte
	done        chan struct{}
	blockingSem chan struct{}
	closeOnce   sync.Once
}

func (c *rpcClient) readLoop() {
	for {
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(data, &req); err != nil {
			continue // malformed frame: no request id to answer
		}
		if isBlockingMethod(req.Svc, req.Method) {
			if !c.dispatchAsync(req) {
				return // client closed while waiting for a worker slot
			}
			continue
		}
		c.respond(req)
	}
}

// respond invokes one request and queues its response on the single writer.
func (c *rpcClient) respond(req rpcRequest) {
	result, callErr := c.srv.Invoke(req.Svc, req.Method, req.Args)
	frame, err := encodeResponse(req.ID, result, callErr)
	if err != nil {
		log.Printf("bridge: marshal response %d: %v", req.ID, err)
		return
	}
	c.enqueue(frame)
}

// dispatchAsync runs an allow-listed blocking method on a bounded goroutine so
// it cannot stall the read loop. It returns false when the client closed while
// waiting for a slot (readLoop then exits).
func (c *rpcClient) dispatchAsync(req rpcRequest) bool {
	select {
	case c.blockingSem <- struct{}{}:
	case <-c.done:
		return false
	}
	go func() {
		defer func() { <-c.blockingSem }()
		c.respond(req)
	}()
	return true
}

func (c *rpcClient) writeLoop() {
	for {
		select {
		case frame := <-c.out:
			if err := c.ws.Write(context.Background(), websocket.MessageText, frame); err != nil {
				c.teardown()
				return
			}
		case <-c.done:
			return
		}
	}
}

// enqueue queues one server→client frame (response or event) on the single
// writer, blocking while the client's buffer is full (lifecycle events are
// never dropped) and returning once the client closes.
func (c *rpcClient) enqueue(frame []byte) {
	select {
	case c.out <- frame:
	case <-c.done:
	}
}

// teardown detaches the client and releases blocked emitters/writers.
func (c *rpcClient) teardown() {
	c.closeOnce.Do(func() {
		c.srv.removeClient(c)
		close(c.done)
		if c.ws != nil {
			c.ws.CloseNow()
		}
	})
}
