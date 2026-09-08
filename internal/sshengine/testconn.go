package sshengine

// TestConnection (master plan phase 3d) dials a session's full hop chain
// (structured jump hosts, ProxyJump, target) with the SAME auth / host-key
// / key-passphrase paths as Connect, but performs NO pty, shell or port
// forwards and closes the connection immediately. It uses an ephemeral
// connID (no tab record) so any host-key or key-passphrase prompt surfaced
// during the test resolves through the same connID-keyed machinery as a
// real connect. Returns nil on success; failures are attributed to the
// failing hop (phase 3c format: "jump host N/M (…): …" / "target …: …").
//
// The same bare-chain dial is reused for the monitor's dedicated connection
// (DialMonitorClient, plan P004): the engine keeps ONE dial-path
// implementation for every PTY-less connection.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
)

// TestConnection dials the hop chain without opening a terminal or any
// forwards, then closes immediately. On success it returns nil and leaves
// no tab record behind.
func (m *Manager) TestConnection(sess *model.Session) error {
	if sess == nil {
		return errors.New("sshengine: nil session")
	}
	connID := model.NewID()
	clients, err := m.dialHopChain(context.Background(), connID, *sess)
	defer func() {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		m.dropConnPrompt(connID)
	}()
	return err
}

// dialHopChain dials every hop of sess (structured jumps + ProxyJump +
// target) with the same auth / host-key / key-passphrase / kbdint paths as
// Connect, but opens NO pty/shell/forwards. Hops after the first are
// tunneled through the previous hop's SSH client as direct-tcpip channels.
// With a bastion hop the target is mark skipDial and the bastion is the
// final dialed client (plan P009), so success == bastion relay reachable.
// It returns all dialed clients in hop order (last = final dialed hop); on
// error the partially-dialed clients are closed before returning. connID
// keys any host-key / key-passphrase / kbdint prompts.
func (m *Manager) dialHopChain(ctx context.Context, connID string, sess model.Session) ([]*ssh.Client, error) {
	hops, timeout, _, err := buildSessionChain(sess)
	if err != nil {
		return nil, err
	}
	clients := make([]*ssh.Client, 0, len(hops))
	cleanup := func() {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
	}
	for i := range hops {
		h := &hops[i]
		if h.skipDial {
			// Bastion target (plan P009): reached through the bastion
			// relay, never dialed. The bastion client is the final one.
			break
		}
		addr := net.JoinHostPort(h.host, strconv.Itoa(h.port))

		methods, err := m.authenticateHop(ctx, connID, h)
		if err != nil {
			cleanup()
			return nil, hopErrorAt(h, i, len(hops), err)
		}
		var nc net.Conn
		if i == 0 {
			nc, err = net.DialTimeout("tcp", addr, timeout)
		} else {
			if clients[i-1] == nil {
				cleanup()
				return nil, hopErrorAt(h, i, len(hops), errors.New("previous hop client missing"))
			}
			nc, err = dialHopVia(ctx, clients[i-1], addr, timeout)
		}
		if err != nil {
			cleanup()
			return nil, hopErrorAt(h, i, len(hops), err)
		}
		cfg := &ssh.ClientConfig{
			User:            h.user,
			Auth:            methods,
			HostKeyCallback: m.HostKeyCallback(connID),
		}
		cn, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
		if err != nil {
			nc.Close()
			cleanup()
			return nil, hopErrorAt(h, i, len(hops), err)
		}
		clients = append(clients, ssh.NewClient(cn, chans, reqs))
	}
	return clients, nil
}

// DialMonitorClient opens a DEDICATED SSH connection to the FINAL hop of a
// ready tab for system monitoring (plan P004). It reuses the tab's stored
// session and the same dial chain + auth caches as the live connection, but
// the returned clients are fully independent of the tab's PTY channel — so
// monitor execs can never contend with terminal output (fix: P004 stutter).
// The caller owns the returned clients (all hops; last = target) and must
// close them together when monitoring stops.
func (m *Manager) DialMonitorClient(tabID string) ([]*ssh.Client, error) {
	l := m.getTab(tabID)
	if l == nil {
		return nil, ErrUnknownTab
	}
	m.mu.Lock()
	st := l.state
	sess := l.session
	m.mu.Unlock()
	if st != stateReady {
		return nil, fmt.Errorf("%w: tab is %s", ErrTabNotReady, st)
	}
	// Distinct connID so the monitor's own host-key / key-passphrase prompts
	// resolve independently of the tab's (known hosts are usually already
	// accepted after the live dial, so the TOFU path is rarely hit).
	connID := tabID + "-monitor"
	clients, err := m.dialHopChain(context.Background(), connID, sess)
	if err != nil {
		m.dropConnPrompt(connID)
		return nil, err
	}
	return clients, nil
}
