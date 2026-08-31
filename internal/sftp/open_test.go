package sftp

// Plan P002 "Open file on double-click": OpenRemoteFile downloads a remote
// file to a 0600 temp copy under tmp/, launches the configured open command
// with the temp path appended last, and leaves the temp copy for the tmp/
// sweep. These tests cover the download/temp lifecycle, the command-building,
// the directory/empty-command rejections and Cleanup sweeping.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenRemoteFileDownloadsAndLaunches(t *testing.T) {
	m, _ := newTestManagerEmit(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello world\n")
	putFile(t, c, "/report.txt", payload)

	marker := filepath.Join(t.TempDir(), "opened-path")
	opener := writeScript(t, "open.sh", `printf '%s' "$1" > '`+marker+`'`)

	tempPath, err := m.OpenRemoteFile(tab, "/report.txt", opener)
	if err != nil {
		t.Fatalf("OpenRemoteFile: %v", err)
	}
	if tempPath == "" || !strings.HasPrefix(filepath.Base(tempPath), "open-") {
		t.Fatalf("temp path = %q, want open-* under tmp", tempPath)
	}

	// The temp copy holds the remote content with 0600 perms.
	data, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("temp content = %q, want %q", data, payload)
	}
	fi, err := os.Stat(tempPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("temp perms = %o, want 0600", fi.Mode().Perm())
	}

	// The opener received exactly the temp path as its final argument. The
	// opener runs asynchronously (released process), so poll for the marker.
	waitFor(t, 5*time.Second, "opener marker written", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("opener marker: %v", err)
	}
	if string(got) != tempPath {
		t.Fatalf("opener arg = %q, want %q", got, tempPath)
	}

	// Cleanup sweeps the temp file (tmp/ lifecycle, §8.8).
	if err := m.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp not swept after Cleanup: %v", err)
	}
}

func TestOpenRemoteFileRejectsDirectory(t *testing.T) {
	m, _ := newTestManagerEmit(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir("/adir"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenRemoteFile(tab, "/adir", "xdg-open"); err == nil {
		t.Fatal("expected error opening a directory")
	}
}

func TestOpenRemoteFileEmptyCommand(t *testing.T) {
	m, _ := newTestManagerEmit(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/a.txt", []byte("x"))
	if _, err := m.OpenRemoteFile(tab, "/a.txt", "   "); err == nil {
		t.Fatal("expected error for empty open command")
	}
}
