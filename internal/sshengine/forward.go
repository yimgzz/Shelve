package sshengine

// Local port forwards (master plan phase 3d). After a tab reaches the
// ready state, each parsed Extra Args forward (-L / -D) is bound on the
// local machine and tunnels through the target SSH client:
//
//   - "-L bind:localPort:dstHost:dstPort": a local net.Listener binds the
//     socket; each accepted local connection opens a direct-tcpip channel
//     to dstHost:dstPort via client.Dial and is bridged bidirectionally.
//   - "-D bind:localPort": the same local listener feeds a MINIMAL SOCKS5
//     proxy (CONNECT verb only, no auth). Greeting "05 01" → reply
//     "05 00"; request "05 01 00 ATYP …" → client.Dial to the target →
//     bidirectional io.Copy pairs. UDP, BIND, RELAY and authentication
//     are documented v1 non-goals.
//
// A bind failure is non-fatal (master plan §5): ssh:forward{state:"failed"}
// + an app:toast error, while the connection proceeds to ready. Tab
// teardown closes every listener and accepted connection and emits
// ssh:forward{state:"closed"} per forward.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"

	"dummy-ssh-manager/internal/sshx/args"
)

// forwardSpec renders the human-readable spec for a parsed forward used
// in ssh:forward payloads and toasts: "bind:localPort" for -D and
// "bind:localPort:dstHost:dstPort" for -L.
func forwardSpec(f args.Forward) string {
	local := net.JoinHostPort(f.BindAddr, strconv.Itoa(f.LocalPort))
	if f.Kind == "D" {
		return local
	}
	return local + ":" + f.DstHost + ":" + strconv.Itoa(f.DstPort)
}

// forwardListener is one live local port forward on a tab. Its fields
// are set once at start; conns is guarded by mu. done is closed when the
// accept loop exits (i.e. the listener was closed).
type forwardListener struct {
	m       *Manager
	tabID   string
	spec    string
	kind    string // "L" (local) or "D" (dynamic SOCKS5)
	ln      net.Listener
	client  *ssh.Client
	dstHost string
	dstPort int

	mu        sync.Mutex
	conns     []net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

// startForwards binds every parsed forward for a ready tab, emitting the
// ssh:forward listening/failed event (and a toast on failure) for each.
// Non-fatal: a bind failure does not disturb the running tab.
func (m *Manager) startForwards(l *liveConn, client *ssh.Client, forwards []args.Forward) {
	for _, f := range forwards {
		// Skip if the tab was torn down between the ready transition and
		// here (a Disconnect in that window owns the teardown): binding a
		// listener now would leak it.
		m.mu.Lock()
		aborted := l.disconnected
		m.mu.Unlock()
		if aborted {
			return
		}
		spec := forwardSpec(f)
		addr := net.JoinHostPort(f.BindAddr, strconv.Itoa(f.LocalPort))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			m.emitForward(l.tabID, spec, ForwardFailed, "", err.Error())
			m.emit.Emit(EventAppToast, ToastPayload{
				Level:   ToastError,
				Message: fmt.Sprintf("port forward %s: bind failed: %v", spec, err),
			})
			continue
		}
		fw := &forwardListener{
			m: m, tabID: l.tabID, spec: spec,
			kind: f.Kind, ln: ln, client: client,
			dstHost: f.DstHost, dstPort: f.DstPort,
			done: make(chan struct{}),
		}
		m.mu.Lock()
		l.forwards = append(l.forwards, fw)
		m.mu.Unlock()
		m.emitForward(l.tabID, spec, ForwardListening, ln.Addr().String(), "")
		go fw.acceptLoop()
	}
}

func (m *Manager) emitForward(tabID, spec, state, localAddr, errMsg string) {
	m.emit.Emit(EventForward, ForwardPayload{
		TabID: tabID, Spec: spec, State: state,
		LocalAddr: localAddr, Error: errMsg,
	})
}

// acceptLoop accepts local connections until the listener is closed,
// handing -L connections to the dial-through handler and -D connections
// to the SOCKS5 handler.
func (f *forwardListener) acceptLoop() {
	defer close(f.done)
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		if f.kind == "D" {
			go f.handleSOCKS(conn)
		} else {
			go f.handleLocal(conn)
		}
	}
}

// close stops the listener and closes every accepted connection. It is
// idempotent and unblocks the accept loop (which then closes done).
func (f *forwardListener) close() {
	f.closeOnce.Do(func() {
		f.ln.Close()
		f.mu.Lock()
		conns := f.conns
		f.conns = nil
		f.mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
}

// handleLocal implements -L dial-through: a local connection is bridged
// bidirectionally with a direct-tcpip channel to the destination.
func (f *forwardListener) handleLocal(c net.Conn) {
	defer c.Close()
	target, err := f.client.Dial("tcp", net.JoinHostPort(f.dstHost, strconv.Itoa(f.dstPort)))
	if err != nil {
		return
	}
	defer target.Close()
	go func() {
		_, _ = io.Copy(target, c)
		if cc, ok := target.(interface{ CloseWrite() error }); ok {
			_ = cc.CloseWrite()
		}
	}()
	_, _ = io.Copy(c, target)
}

// handleSOCKS implements the minimal SOCKS5 proxy (see the package
// comment for the supported subset).
func (f *forwardListener) handleSOCKS(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)

	// Greeting: VER(1) NMETHODS(1) METHODS(N) → expect version 5.
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return
	}
	if head[0] != 0x05 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	// Reply: no authentication required.
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// Request: VER(1) CMD(1) RSV(1) ATYP(1) ADDR PORT.
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if hdr[0] != 0x05 || hdr[1] != 0x01 { // CONNECT only
		f.socksReply(c, 0x07) // command not supported
		return
	}
	host, err := f.socksReadAddr(br, hdr[3])
	if err != nil {
		f.socksReply(c, 0x08) // address type not supported
		return
	}
	portB := make([]byte, 2)
	if _, err := io.ReadFull(br, portB); err != nil {
		return
	}
	port := int(portB[0])<<8 | int(portB[1])

	target, err := f.client.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		f.socksReply(c, 0x05) // connection refused
		return
	}
	defer target.Close()
	f.socksReply(c, 0x00) // success

	// Bridge both directions; closing either side tears down the pair.
	go func() { io.Copy(target, br) }()
	io.Copy(c, target)
}

// socksReadAddr reads the ADDR field of a SOCKS5 CONNECT request for the
// given ATYP and returns the host string.
func (f *forwardListener) socksReadAddr(br *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03: // domain name
		l := make([]byte, 1)
		if _, err := io.ReadFull(br, l); err != nil {
			return "", err
		}
		if l[0] == 0 {
			return "", io.ErrUnexpectedEOF
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return string(b), nil
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	default:
		return "", io.ErrUnexpectedEOF
	}
}

// socksReply writes a SOCKS5 reply: VER REP RSV ATYP BND.ADDR(4) BND.PORT.
func (f *forwardListener) socksReply(c net.Conn, code byte) {
	_, _ = c.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}
