//go:build integration

package sftp

// Phase 5c integration tests (master plan §9, task 3): the sftp.Manager
// exercised against a REAL openssh server via testcontainers (docker/sshd).
// Run with `make test-integration` (Go tag `integration`; Docker socket
// mounted). Credentials and fixtures live ONLY inside docker/sshd.
//
// The plan references a shared `startSshd(t)` helper; this file defines it
// (no 3e helper shipped before 5c) plus the SFTP cases: list ordering + ops,
// upload progress/content, download round-trip, rename + remote text editing
// with the baked-in fake editor, and clean failure when a tab closes
// mid-transfer.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"golang.org/x/crypto/ssh"
)

// sshdBox holds the published address of the test sshd container.
type sshdBox struct {
	addr string
}

// liveProvider is a TabProvider backed by a real SSH client to the test box.
type liveProvider struct {
	cli *ssh.Client
}

func (p *liveProvider) SSHClient(string) (*ssh.Client, error) {
	return p.cli, nil
}

// startSshd builds the docker/sshd image and starts it, returning its
// published address. The container is terminated when the test finishes.
func startSshd(t *testing.T) sshdBox {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    "../../docker/sshd",
			Dockerfile: "Dockerfile",
		},
		ExposedPorts: []string{"22/tcp"},
		WaitingFor:   wait.ForListeningPort("22/tcp").WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start sshd: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("sshd host: %v", err)
	}
	port, err := c.MappedPort(ctx, "22/tcp")
	if err != nil {
		t.Fatalf("sshd port: %v", err)
	}
	return sshdBox{addr: net.JoinHostPort(host, port.Port())}
}

// dialBox opens a password-authenticated SSH connection to the test box.
func dialBox(t *testing.T, box sshdBox) *ssh.Client {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("test")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // integration-test fixture only
		Timeout:         15 * time.Second,
	}
	cli, err := ssh.Dial("tcp", box.addr, cfg)
	if err != nil {
		t.Fatalf("dial %s: %v", box.addr, err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// newLiveManager wires a Manager over a real box connection with a capturing
// emitter.
func newLiveManager(t *testing.T, box sshdBox) (*Manager, *captureEmitter) {
	t.Helper()
	cli := dialBox(t, box)
	emit := &captureEmitter{}
	m := New(t.TempDir(), emit)
	m.Attach(&liveProvider{cli: cli})
	return m, emit
}

// writeLocalFile writes `data` to a fresh temp file named `name`.
func writeLocalFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeFakeEditor writes a host-side fake text editor: it appends a marker
// line to the file given as its LAST argument and exits (the same contract as
// the app's EditRemoteText, which runs the configured editor on the host with
// the temp path appended last).
func writeFakeEditor(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-editor.sh")
	script := "#!/bin/sh\nset -eu\neval \"target=\\${$#}\"\nprintf '# edited-by-dsm-fake-editor-%s\\n' \"$(date +%s)\" >> \"$target\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// readRemote reads an entire remote file via the manager's client (paths are
// resolved through the manager so "~" expands to the remote home).
func readRemote(t *testing.T, m *Manager, tabID, remote string) []byte {
	t.Helper()
	abs, err := m.resolve(tabID, remote)
	if err != nil {
		t.Fatalf("resolve %s: %v", remote, err)
	}
	c, err := m.ClientFor(tabID)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	f, err := c.Open(abs)
	if err != nil {
		t.Fatalf("open %s: %v", abs, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read %s: %v", abs, err)
	}
	return data
}

// ----------------------------------------------------------- list + ops ----

func TestIntegrationListOrderingAndOps(t *testing.T) {
	m, _ := newLiveManager(t, startSshd(t))
	tab := "tab-list"

	// Home listing: directories first, then case-insensitive names.
	entries, err := m.List(tab, "~")
	if err != nil {
		t.Fatalf("List ~: %v", err)
	}
	want := []string{"subdir", "a.txt", "big.txt", "data.bin"}
	if got := names(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("home names = %v, want %v", got, want)
	}

	// Nested subtree (dir "deep" first).
	sub, err := m.List(tab, "~/subdir")
	if err != nil {
		t.Fatalf("List subdir: %v", err)
	}
	if got := names(sub); !reflect.DeepEqual(got, []string{"deep", "b.md"}) {
		t.Fatalf("subdir names = %v", got)
	}

	// Mkdir then remove the empty directory.
	if err := m.Mkdir(tab, "~/newdir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	after, err := m.List(tab, "~")
	if err != nil {
		t.Fatalf("List after mkdir: %v", err)
	}
	if !containsName(after, "newdir") {
		t.Fatalf("newdir not listed after Mkdir")
	}
	if err := m.Remove(tab, "~/newdir"); err != nil {
		t.Fatalf("Remove empty dir: %v", err)
	}

	// Removing a non-empty directory maps to ErrDirNotEmpty.
	err = m.Remove(tab, "~/subdir")
	if !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("Remove non-empty dir err = %v, want ErrDirNotEmpty", err)
	}
}

// ---------------------------------------------------------- upload + prog --

func TestIntegrationUploadProgressAndContent(t *testing.T) {
	m, emit := newLiveManager(t, startSshd(t))
	tab := "tab-up"

	// One generated ~12 MiB file plus two small ones.
	const bigSize = 12 << 20
	bigData := bytes.Repeat([]byte("U"), bigSize)
	big := writeLocalFile(t, "big_upload.dat", bigData)
	small := writeLocalFile(t, "notes.txt", []byte("small file content\n"))
	tiny := writeLocalFile(t, "tiny.conf", []byte("key=value\n"))

	if err := m.Upload(tab, []string{big, small, tiny}, "~"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Remote content round-trips exactly (download back + compare).
	if got := readRemote(t, m, tab, "~/big_upload.dat"); !bytes.Equal(got, bigData) {
		t.Fatalf("big_upload.dat mismatch: got %d bytes, want %d", len(got), len(bigData))
	}
	if got := string(readRemote(t, m, tab, "~/notes.txt")); got != "small file content\n" {
		t.Fatalf("notes.txt = %q", got)
	}

	// Progress for the big file: monotonic and a terminal done == total.
	evs := emit.progressFor("big_upload.dat")
	if len(evs) < 2 {
		t.Fatalf("big_upload.dat got %d progress events, want >= 2", len(evs))
	}
	var last int64 = -1
	for _, e := range evs {
		if e.DoneBytes < last {
			t.Fatalf("progress not monotonic: %v", e.DoneBytes)
		}
		last = e.DoneBytes
		if e.Error != "" {
			t.Fatalf("unexpected error in progress: %s", e.Error)
		}
	}
	term := evs[len(evs)-1]
	if term.TotalBytes != bigSize || term.DoneBytes != bigSize {
		t.Fatalf("terminal done=%d total=%d, want %d/%d", term.DoneBytes, term.TotalBytes, bigSize, bigSize)
	}
}

// ------------------------------------------------------------- download ----

func TestIntegrationDownloadAndSave(t *testing.T) {
	m, _ := newLiveManager(t, startSshd(t))
	tab := "tab-dl"

	remote := "~/a.txt"
	want := "hello root text\n"

	// Download → bytes-identical temp copy.
	p, err := m.Download(tab, remote)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if p == "" {
		t.Fatal("Download returned empty path")
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != want {
		t.Fatalf("downloaded content = %q (err %v), want %q", got, err, want)
	}

	// DownloadThenSave → fallback branch returns the temp path.
	p2, err := m.DownloadThenSave(tab, remote)
	if err != nil {
		t.Fatalf("DownloadThenSave: %v", err)
	}
	if p2 == "" {
		t.Fatal("DownloadThenSave returned empty path")
	}
	if _, err := os.Stat(p2); err != nil {
		t.Fatalf("DownloadThenSave file missing: %v", err)
	}
}

// ------------------------------------------------------ rename + editing ----

func TestIntegrationRenameAndEdit(t *testing.T) {
	m, _ := newLiveManager(t, startSshd(t))
	tab := "tab-ed"
	editor := writeFakeEditor(t)

	// Rename a file and confirm it moved.
	if err := m.Rename(tab, "~/a.txt", "~/renamed.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	root, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(homeOf(t, m, tab) + "/renamed.txt"); err != nil {
		t.Fatalf("renamed.txt not found after Rename: %v", err)
	}

	// EditRemoteText with the host-side fake editor: it appends a marker line.
	if err := m.EditRemoteText(tab, "~/renamed.txt", editor); err != nil {
		t.Fatalf("EditRemoteText: %v", err)
	}
	waitForEditSave(t, m, tab, "~/renamed.txt")

	// Temp copies are cleaned after a save (edit-* gone from tmpDir).
	waitForNoEditTemps(t, m)

	// Text-like but too large → ErrTooLarge.
	err = m.EditRemoteText(tab, "~/big.txt", editor)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big.txt edit err = %v, want ErrTooLarge", err)
	}

	// Content probe, not extension: data.bin starts with NUL bytes →
	// ErrBinary, refused before the full download.
	err = m.EditRemoteText(tab, "~/data.bin", editor)
	if !errors.Is(err, ErrBinary) {
		t.Fatalf("data.bin edit err = %v, want ErrBinary", err)
	}

	// Extension no longer gates editing: seed an extensionless file in the
	// remote home and round-trip an edit through the fake editor.
	nf, err := root.Create(homeOf(t, m, tab) + "/noext")
	if err != nil {
		t.Fatalf("Create ~/noext: %v", err)
	}
	if _, err := nf.Write([]byte("no-extension file\n")); err != nil {
		_ = nf.Close()
		t.Fatalf("Write ~/noext: %v", err)
	}
	if err := nf.Close(); err != nil {
		t.Fatalf("Close ~/noext: %v", err)
	}
	if err := m.EditRemoteText(tab, "~/noext", editor); err != nil {
		t.Fatalf("EditRemoteText ~/noext: %v", err)
	}
	waitForEditSave(t, m, tab, "~/noext")
	waitForNoEditTemps(t, m)
}

// homeOf resolves the tab's remote home (absolute path).
func homeOf(t *testing.T, m *Manager, tabID string) string {
	t.Helper()
	h, err := m.home(tabID)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// waitForEditSave polls until the remote file contains the fake-editor marker.
func waitForEditSave(t *testing.T, m *Manager, tabID, remote string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data := readRemote(t, m, tabID, remote)
		if strings.Contains(string(data), "edited-by-dsm-fake-editor") {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("remote %s not updated by fake editor within timeout", remote)
}

// waitForNoEditTemps asserts tmpDir holds no edit-* temp/log leftovers.
func waitForNoEditTemps(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ents, err := os.ReadDir(m.tmpDir)
		if err == nil {
			clean := true
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), "edit-") {
					clean = false
					break
				}
			}
			if clean {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("edit-* temp files left behind in %s", m.tmpDir)
}

// ----------------------------------------------- tab closed mid-transfer ----

func TestIntegrationTabClosedMidTransfer(t *testing.T) {
	box := startSshd(t)
	cli := dialBox(t, box)
	m := New(t.TempDir(), nil)
	m.Attach(&liveProvider{cli: cli})
	tab := "tab-close"

	// Large enough that localhost SFTP cannot finish it within the delay
	// below, so the tab close reliably interrupts an in-flight transfer.
	lp := writeLocalFile(t, "huge.dat", bytes.Repeat([]byte("Z"), 192<<20))

	done := make(chan error, 1)
	go func() {
		done <- m.Upload(tab, []string{lp}, "~")
	}()

	// Let the transfer start, then tear the tab down (closes the client).
	time.Sleep(200 * time.Millisecond)
	m.HandleTabClosed(tab)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Upload to fail after the tab closed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Upload hung after tab closed")
	}
}

func containsName(entries []Entry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}
