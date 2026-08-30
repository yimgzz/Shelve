package sftp

// Phase 5a unit tests (master plan §9): the sftp.Manager's browse
// operations are exercised against pkg/sftp's in-memory request server
// (sftp.InMemHandler) served over an in-process SSH server — no Docker,
// no external sshd. A real ssh.Client is dialed to it so the manager's
// ClientFor path (sftp.NewClient over the provider's *ssh.Client) is the
// real one.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/pkg/sftp"

	"golang.org/x/crypto/ssh"
)

// stubProvider is a TabProvider returning a fixed client until err is set
// (simulating the tab going away after its SSH connection closes).
type stubProvider struct {
	cli *ssh.Client
	err error
}

func (p *stubProvider) SSHClient(string) (*ssh.Client, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.cli, nil
}

// newMemSFTPClient starts an in-process SSH server whose "sftp" subsystem
// is served by sftp.InMemHandler() over the session channel, and returns a
// connected *ssh.Client.
func newMemSFTPClient(t *testing.T) *ssh.Client {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveMemSFTP(nc, cfg)
		}
	}()

	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("test")},
		HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// serveMemSFTP accepts one SSH connection and serves the sftp subsystem on
// any session channel with a fresh in-memory filesystem per connection.
func serveMemSFTP(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chanReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func(ch ssh.Channel, chanReqs <-chan *ssh.Request) {
			defer ch.Close()
			for req := range chanReqs {
				if req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp" {
					_ = req.Reply(true, nil)
					srv := sftp.NewRequestServer(ch, sftp.InMemHandler())
					_ = srv.Serve()
					return
				}
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}(ch, chanReqs)
	}
}

// newTestManager wires an unattached-emit Manager over a stub provider for
// a mem-server ssh client.
func newTestManager(t *testing.T) (*Manager, *stubProvider) {
	t.Helper()
	cli := newMemSFTPClient(t)
	prov := &stubProvider{cli: cli}
	m := New(t.TempDir(), nil)
	m.Attach(prov)
	return m, prov
}

// putFile writes data into the mem FS via an existing sftp client.
func putFile(t *testing.T, c *sftp.Client, path string, data []byte) {
	t.Helper()
	f, err := c.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func names(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestTextLike(t *testing.T) {
	m := &Manager{}
	cases := []struct {
		name string
		size int64
		want bool
	}{
		{"readme.md", 100, true},
		{"script.SH", 5, true},             // extension case-insensitive
		{"config.yaml", 99, true},          // multi-char whitelisted
		{"app.go", MaxTextSize, true},      // exactly 2 MiB → text-like
		{"app.go", MaxTextSize + 1, false}, // one byte over → not text-like
		{"notes.txt", -1, false},           // negative size → false
		{"README", 10, false},              // no extension → false
		{"photo.png", 100, false},          // extension not whitelisted
		{"archive.tar.gz", 10, false},      // composite ext not whitelisted
		{"data.bin", 0, false},
	}
	for _, c := range cases {
		if got := m.TextLike(c.name, c.size); got != c.want {
			t.Errorf("TextLike(%q, %d) = %v, want %v", c.name, c.size, got, c.want)
		}
	}
}

func TestListSortingAndTextLike(t *testing.T) {
	m, _ := newTestManager(t)
	tabID := "tab1"
	c, err := m.ClientFor(tabID)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	// Directories.
	for _, d := range []string{"/Zeta", "/beta", "/Alpha"} {
		if err := c.Mkdir(d); err != nil {
			t.Fatalf("Mkdir %s: %v", d, err)
		}
	}
	// Files (mixed case, extensions, a no-extension one).
	putFile(t, c, "/readme.md", []byte("hi"))
	putFile(t, c, "/zebra.txt", []byte("z"))
	putFile(t, c, "/Photo.PNG", []byte("img"))
	putFile(t, c, "/noext", []byte("x"))

	entries, err := m.List(tabID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotNames := names(entries)
	// Dirs first (case-insensitive), then files (case-insensitive).
	wantNames := []string{"Alpha", "beta", "Zeta", "noext", "Photo.PNG", "readme.md", "zebra.txt"}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("List names = %v, want %v", gotNames, wantNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("List names = %v, want %v", gotNames, wantNames)
		}
	}
	// TextLike flags per entry.
	flags := map[string]bool{}
	for _, e := range entries {
		flags[e.Name] = e.TextLike
	}
	if !flags["readme.md"] || !flags["zebra.txt"] {
		t.Errorf("expected text-like files flagged: %v", flags)
	}
	if flags["Photo.PNG"] || flags["noext"] {
		t.Errorf("expected non-text files unflagged: %v", flags)
	}
	// The three created directories must be flagged IsDir.
	dirSet := map[string]bool{"Alpha": true, "beta": true, "Zeta": true}
	for _, e := range entries {
		if dirSet[e.Name] && !e.IsDir {
			t.Errorf("%s should be IsDir", e.Name)
		}
	}
}

func TestResolve(t *testing.T) {
	m, _ := newTestManager(t)
	tabID := "tab1"
	if _, err := m.ClientFor(tabID); err != nil { // populates home
		t.Fatalf("ClientFor: %v", err)
	}
	if got := m.homes[tabID]; got != "/" {
		t.Fatalf("home = %q, want %q (mem server Getwd)", got, "/")
	}
	cases := map[string]string{
		"":          "/",
		"~":         "/",
		"~/":        "/",
		"~/a":       "/a",
		"~/a/b/..":  "/a",
		"a/../b":    "b",
		"a":         "a",
		"/x/y":      "/x/y",
		"/x/../y":   "/y",
		"/./z":      "/z",
		"~/sub/dir": "/sub/dir",
	}
	for in, want := range cases {
		got, err := m.resolve(tabID, in)
		if err != nil {
			t.Errorf("resolve(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMkdirAndNestedParentFailure(t *testing.T) {
	m, _ := newTestManager(t)
	tabID := "tab1"
	if err := m.Mkdir(tabID, "/newdir"); err != nil {
		t.Fatalf("Mkdir top-level: %v", err)
	}
	// Nested mkdir with a missing parent must fail (no recursive mkdir).
	if err := m.Mkdir(tabID, "/a/b/c"); err == nil {
		t.Fatalf("expected nested-parent mkdir to fail")
	}
}

func TestRenameFileAndDir(t *testing.T) {
	m, _ := newTestManager(t)
	tabID := "tab1"
	c, err := m.ClientFor(tabID)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/old.txt", []byte("x"))
	if err := m.Rename(tabID, "/old.txt", "/renamed.txt"); err != nil {
		t.Fatalf("Rename file: %v", err)
	}
	if _, err := c.Stat("/old.txt"); err == nil {
		t.Fatalf("old file still exists after rename")
	}
	if err := m.Mkdir(tabID, "/adir"); err != nil {
		t.Fatal(err)
	}
	if err := m.Rename(tabID, "/adir", "/bdir"); err != nil {
		t.Fatalf("Rename dir: %v", err)
	}
	if _, err := c.Stat("/bdir"); err != nil {
		t.Fatalf("renamed dir missing: %v", err)
	}
}

func TestRemoveFileEmptyDirNonEmptyDir(t *testing.T) {
	m, _ := newTestManager(t)
	tabID := "tab1"
	c, err := m.ClientFor(tabID)
	if err != nil {
		t.Fatal(err)
	}
	// File removal.
	putFile(t, c, "/f.txt", []byte("x"))
	if err := m.Remove(tabID, "/f.txt"); err != nil {
		t.Fatalf("Remove file: %v", err)
	}
	if _, err := c.Stat("/f.txt"); err == nil {
		t.Fatalf("file still exists after Remove")
	}
	// Empty directory removal.
	if err := m.Mkdir(tabID, "/edir"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(tabID, "/edir"); err != nil {
		t.Fatalf("Remove empty dir: %v", err)
	}
	// Non-empty directory → ErrDirNotEmpty.
	if err := m.Mkdir(tabID, "/full"); err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/full/a.txt", []byte("x"))
	err = m.Remove(tabID, "/full")
	if !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("Remove non-empty dir err = %v, want ErrDirNotEmpty", err)
	}
}

func TestHandleTabClosedReleasesClient(t *testing.T) {
	m, prov := newTestManager(t)
	tabID := "tab1"
	if _, err := m.ClientFor(tabID); err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if _, ok := m.clients[tabID]; !ok {
		t.Fatalf("client not cached before close")
	}
	// Simulate the tab's SSH connection dying: provider now reports the
	// tab gone, and the engine would call the OnTabClosed hook.
	prov.err = errors.New("tab gone")
	m.HandleTabClosed(tabID)
	if _, ok := m.clients[tabID]; ok {
		t.Fatalf("client still cached after HandleTabClosed")
	}
	if _, err := m.List(tabID, "/"); err == nil {
		t.Fatalf("expected List to fail after tab close, got nil")
	}
}

func TestCloseAllIdempotent(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.ClientFor("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ClientFor("b"); err != nil {
		t.Fatal(err)
	}
	m.CloseAll()
	if len(m.clients) != 0 || len(m.homes) != 0 {
		t.Fatalf("CloseAll did not clear caches: clients=%d homes=%d", len(m.clients), len(m.homes))
	}
	m.CloseAll() // idempotent: no panic
	if len(m.clients) != 0 {
		t.Fatalf("second CloseAll changed state")
	}
}

func TestIsActive(t *testing.T) {
	m, prov := newTestManager(t)
	// A ready (but not yet materialized) tab is already active.
	if !m.IsActive("tab1") {
		t.Fatalf("IsActive before any client should be true (provider ready)")
	}
	if _, err := m.ClientFor("tab1"); err != nil {
		t.Fatal(err)
	}
	if !m.IsActive("tab1") {
		t.Fatalf("IsActive after client created should be true")
	}
	prov.err = errors.New("tab gone")
	if !m.IsActive("tab1") {
		t.Fatalf("IsActive should be true while cached even if provider errs")
	}
	// A never-created tab now reports provider failure.
	if m.IsActive("other") {
		t.Fatalf("IsActive(other) should be false when provider errors")
	}
	// No provider → false.
	none := New(t.TempDir(), nil)
	if none.IsActive("x") {
		t.Fatalf("IsActive without provider should be false")
	}
}
