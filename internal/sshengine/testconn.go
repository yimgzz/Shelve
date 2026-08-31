package sshengine

// TestConnection (master plan phase 3d) dials a session's full hop chain
// (structured jump hosts, ProxyJump, target) with the SAME auth / host-key
// / key-passphrase paths as Connect, but performs NO pty, shell or port
// forwards and closes the connection immediately. It uses an ephemeral
// connID (no tab record) so any host-key or key-passphrase prompt surfaced
// during the test resolves through the same connID-keyed machinery as a
// real connect. Returns nil on success; failures are attributed to the
// failing hop (phase 3c format: "jump host N/M (…): …" / "target …: …").

import (
	"context"
	"errors"
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
	hops, timeout, _, err := buildSessionChain(*sess)
	if err != nil {
		return err
	}

	var clients []*ssh.Client
	defer func() {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		m.dropHostKeyPrompt(connID)
	}()

	for i := range hops {
		h := &hops[i]
		addr := net.JoinHostPort(h.host, strconv.Itoa(h.port))

		methods, err := m.authenticateHop(context.Background(), connID, h)
		if err != nil {
			return hopErrorAt(h, i, len(hops), err)
		}
		nc, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return hopErrorAt(h, i, len(hops), err)
		}
		cfg := &ssh.ClientConfig{
			User:            h.user,
			Auth:            methods,
			HostKeyCallback: m.HostKeyCallback(connID),
		}
		cn, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
		if err != nil {
			nc.Close()
			return hopErrorAt(h, i, len(hops), err)
		}
		clients = append(clients, ssh.NewClient(cn, chans, reqs))
	}
	return nil
}
