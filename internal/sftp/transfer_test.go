package sftp

// Phase 5b transfer unit tests (master plan §9): upload/download streaming
// against the mem SFTP server, progress-event throttling/terminal semantics,
// batch-stop on failure, and the dialog-free fallbacks. A capturing emitter
// records sftp:progress / app:toast payloads.

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// captureEmitter records sftp:progress and app:toast payloads.
type captureEmitter struct {
	mu       sync.Mutex
	progress []ProgressPayload
	toasts   []ToastPayload
}

func (c *captureEmitter) Emit(event string, payload any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p := payload.(type) {
	case ProgressPayload:
		c.progress = append(c.progress, p)
	case ToastPayload:
		c.toasts = append(c.toasts, p)
	}
}

func (c *captureEmitter) progressFor(file string) []ProgressPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ProgressPayload, 0, len(c.progress))
	for _, p := range c.progress {
		if p.FileName == file {
			out = append(out, p)
		}
	}
	return out
}

func (c *captureEmitter) errorToasts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.toasts {
		if t.Level == "error" {
			n++
		}
	}
	return n
}

// newTestManagerEmit wires a manager over the mem-server ssh client with a
// capturing emitter attached (no provider errors).
func newTestManagerEmit(t *testing.T) (*Manager, *captureEmitter) {
	t.Helper()
	emit := &captureEmitter{}
	cli := newMemSFTPClient(t)
	prov := &stubProvider{cli: cli}
	m := New(t.TempDir(), emit)
	m.Attach(prov)
	return m, emit
}

// readRemoteAll reads an entire remote file through a fresh sftp client.
func readRemoteAll(t *testing.T, m *Manager, tabID, remote string) []byte {
	t.Helper()
	c, err := m.ClientFor(tabID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.Open(remote)
	if err != nil {
		t.Fatalf("open remote %s: %v", remote, err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// writeLocal writes data to a fresh temp local file and returns its path.
func writeLocal(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "local.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUploadProgressMonotonicAndTerminal(t *testing.T) {
	m, emit := newTestManagerEmit(t)
	tab := "tab1"

	const size = 5 << 20 // 5 MiB
	data := bytes.Repeat([]byte("x"), size)
	lp := writeLocal(t, data)

	if err := m.Upload(tab, []string{lp}, "/"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Remote content round-trips exactly.
	if got := readRemoteAll(t, m, tab, "/local.bin"); !bytes.Equal(got, data) {
		t.Fatalf("remote content mismatch: got %d bytes, want %d", len(got), len(data))
	}

	events := emit.progressFor("local.bin")
	if len(events) < 2 {
		t.Fatalf("expected ≥2 progress events (intermediate + terminal), got %d", len(events))
	}
	last := events[len(events)-1]
	if last.Direction != "up" || last.DoneBytes != size || last.TotalBytes != size || last.Error != "" {
		t.Fatalf("terminal event wrong: %+v", last)
	}
	var prev int64 = -1
	for i, e := range events {
		if e.Direction != "up" {
			t.Fatalf("event[%d] direction = %q, want up", i, e.Direction)
		}
		if e.DoneBytes < prev {
			t.Fatalf("progress not monotonic at %d: %d < %d", i, e.DoneBytes, prev)
		}
		prev = e.DoneBytes
	}
}

func TestUploadTwoFilesSequential(t *testing.T) {
	m, _ := newTestManagerEmit(t)
	tab := "tab1"

	a := writeLocal(t, []byte("aaa"))
	b := writeLocal(t, []byte("bbbb"))
	// Rename so both land with distinct remote basenames.
	dir := filepath.Dir(a)
	ba := filepath.Join(dir, "a.txt")
	bb := filepath.Join(dir, "b.txt")
	if err := os.Rename(a, ba); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(b, bb); err != nil {
		t.Fatal(err)
	}

	if err := m.Upload(tab, []string{ba, bb}, "/"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if got := readRemoteAll(t, m, tab, "/a.txt"); string(got) != "aaa" {
		t.Fatalf("/a.txt = %q", got)
	}
	if got := readRemoteAll(t, m, tab, "/b.txt"); string(got) != "bbbb" {
		t.Fatalf("/b.txt = %q", got)
	}
}

func TestUploadMissingLocalFileStopsQueue(t *testing.T) {
	m, emit := newTestManagerEmit(t)
	tab := "tab1"

	ok := writeLocal(t, []byte("first"))
	missing1 := filepath.Join(t.TempDir(), "nope-1")
	missing2 := filepath.Join(t.TempDir(), "nope-2")

	err := m.Upload(tab, []string{ok, missing1, missing2}, "/")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Upload missing file err = %v, want fs.ErrNotExist", err)
	}
	// First file was uploaded before the failure.
	if got := readRemoteAll(t, m, tab, "/local.bin"); string(got) != "first" {
		t.Fatalf("/local.bin = %q, want first", got)
	}
	// Queue stopped: the second (also valid-absent) batch member was never
	// attempted — only ONE error toast for the first failure.
	if n := emit.errorToasts(); n != 1 {
		t.Fatalf("error toasts = %d, want 1 (queue stops at first failure)", n)
	}
}

func TestDownloadReturnsTempWithPerms(t *testing.T) {
	m, emit := newTestManagerEmit(t)
	tab := "tab1"

	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/notes.txt", []byte("remote content"))

	p, err := m.Download(tab, "/notes.txt")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if string(data) != "remote content" {
		t.Fatalf("temp content = %q", data)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("temp perms = %o, want 0600", fi.Mode().Perm())
	}

	hasDown := false
	emit.mu.Lock()
	for _, ev := range emit.progress {
		if ev.Direction == "down" && ev.FileName == "notes.txt" && ev.DoneBytes == ev.TotalBytes {
			hasDown = true
		}
	}
	emit.mu.Unlock()
	if !hasDown {
		t.Fatal("expected a terminal down-progress event for notes.txt")
	}
}

func TestDownloadThenSaveFallback(t *testing.T) {
	m, emit := newTestManagerEmit(t)
	tab := "tab1"

	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/doc.md", []byte("# hello"))

	p, err := m.DownloadThenSave(tab, "/doc.md")
	if err != nil {
		t.Fatalf("DownloadThenSave: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# hello" {
		t.Fatalf("temp content = %q", data)
	}
	// Fallback path shows an info toast with the path.
	emit.mu.Lock()
	var found bool
	for _, toast := range emit.toasts {
		if toast.Level == "info" && toast.Message == "Downloaded to "+p {
			found = true
		}
	}
	emit.mu.Unlock()
	if !found {
		t.Fatal("expected an info toast with the downloaded path")
	}
}

func TestPickLocalFilesUnsupported(t *testing.T) {
	m, _ := newTestManagerEmit(t)
	if _, err := m.PickLocalFiles(true); !errors.Is(err, ErrSftpDialogUnsupported) {
		t.Fatalf("PickLocalFiles = %v, want ErrSftpDialogUnsupported", err)
	}
}
