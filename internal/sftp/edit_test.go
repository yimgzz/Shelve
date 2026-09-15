package sftp

// Phase 5b remote text-editing state-machine tests (master plan §9): a FAKE
// editor is a shell script in t.TempDir(). The watcher poll/stability windows
// are shortened so the save-detect round trip completes quickly. Save, the
// ErrAlreadyEditing guard, cancel, non-zero exit, probe rejections and
// Cleanup are covered against the mem SFTP server.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// newTestManagerFast wires a manager whose watcher uses short timings.
func newTestManagerFast(t *testing.T) (*Manager, *captureEmitter) {
	t.Helper()
	m, emit := newTestManagerEmit(t)
	m.editPoll = 15 * time.Millisecond
	m.editStability = 50 * time.Millisecond
	return m, emit
}

// getEdit returns the live editState for a tab, or nil.
func getEdit(m *Manager, tab string) *editState {
	m.editsMu.Lock()
	defer m.editsMu.Unlock()
	return m.edits[tab]
}

// waitFor waits up to timeout for cond to become true.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// groupGone reports whether the process group pgid no longer exists (signal
// 0 returns ESRCH once every member is dead and reaped). Used instead of
// reading cmd.ProcessState, which races with the waiter's cmd.Wait().
func groupGone(pgid int) bool {
	return syscall.Kill(-pgid, 0) != nil
}

func TestEditSaveRoundTripAndTempCleanup(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/notes.txt", []byte("hello\n"))

	editor := writeScript(t, "append.sh", `printf 'appended\n' >> "$1"; exit 0`)
	if err := m.EditRemoteText(tab, "/notes.txt", editor); err != nil {
		t.Fatalf("EditRemoteText: %v", err)
	}

	// The watcher re-uploads once stable; remote content must change.
	waitFor(t, 5*time.Second, "remote content updated",
		func() bool { return strings.Contains(string(readRemoteAll(t, m, tab, "/notes.txt")), "appended") })

	// After the single successful save the temp copy is deleted and the
	// editor log removed once the process exits, leaving no edit-* file
	// behind. (This editor exits immediately, so the record itself may
	// already be gone — assert on the tmp dir rather than the record.)
	waitFor(t, 5*time.Second, "edit temp/log cleaned after save", func() bool {
		ents, err := os.ReadDir(m.tmpDir)
		if err != nil {
			return false
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "edit-") {
				return false
			}
		}
		return true
	})
}

func TestEditErrAlreadyEditing(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/a.txt", []byte("x"))

	editor := writeScript(t, "long.sh", `sleep 30`)
	if err := m.EditRemoteText(tab, "/a.txt", editor); err != nil {
		t.Fatalf("first edit: %v", err)
	}
	waitFor(t, 2*time.Second, "edit registered", func() bool { return getEdit(m, tab) != nil })

	if err := m.EditRemoteText(tab, "/a.txt", editor); !errors.Is(err, ErrAlreadyEditing) {
		t.Fatalf("second edit err = %v, want ErrAlreadyEditing", err)
	}

	if err := m.CancelEdit(tab); err != nil {
		t.Fatalf("CancelEdit: %v", err)
	}
}

func TestEditCancelNoUploadTempGone(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/keep.txt", []byte("original"))

	editor := writeScript(t, "sleep30.sh", `sleep 30`)
	if err := m.EditRemoteText(tab, "/keep.txt", editor); err != nil {
		t.Fatalf("EditRemoteText: %v", err)
	}
	es := getEdit(m, tab)
	waitFor(t, 2*time.Second, "edit registered", func() bool { return getEdit(m, tab) != nil })

	if err := m.CancelEdit(tab); err != nil {
		t.Fatalf("CancelEdit: %v", err)
	}

	// No upload happened.
	if got := readRemoteAll(t, m, tab, "/keep.txt"); string(got) != "original" {
		t.Fatalf("remote changed after cancel: %q", got)
	}
	// Temp + log gone, edit record gone.
	if _, err := os.Stat(es.tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp not removed after cancel: %v", err)
	}
	if _, err := os.Stat(es.logPath); !os.IsNotExist(err) {
		t.Fatalf("log not removed after cancel: %v", err)
	}
	if getEdit(m, tab) != nil {
		t.Fatal("edit record not cleared after cancel")
	}
	// The process group was killed (signal 0 → ESRCH).
	waitFor(t, 5*time.Second, "editor process group dead", func() bool { return groupGone(es.pgid) })
}

func TestEditNonZeroExitKeepsTemp(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/err.txt", []byte("data"))

	editor := writeScript(t, "fail.sh", `sleep 0.3; exit 3`)
	if err := m.EditRemoteText(tab, "/err.txt", editor); err != nil {
		t.Fatalf("EditRemoteText: %v", err)
	}
	// The temp copy is staged synchronously; discover it on disk because the
	// record is dropped as soon as the editor exits.
	var tempPath string
	waitFor(t, 5*time.Second, "edit temp staged", func() bool {
		ents, err := os.ReadDir(m.tmpDir)
		if err != nil {
			return false
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "edit-") && !strings.HasSuffix(e.Name(), ".log") {
				tempPath = filepath.Join(m.tmpDir, e.Name())
				return true
			}
		}
		return false
	})

	// Editor exits non-zero before any save → the edit record is removed
	// (process exited) and the temp copy is kept (no user-facing message:
	// with launcher editors like xdg-open, an exit-time signal would be
	// wrong, so the edit flow is silent except for save/failure toasts).
	waitFor(t, 5*time.Second, "edit record removed", func() bool { return getEdit(m, tab) == nil })
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("temp should be KEPT on non-zero exit: %v", err)
	}
}

// TestEditUntouchedExitKeepsTempAndStaysWatchable covers the launcher case
// (xdg-open): the launched command exits zero immediately while the file is
// still untouched. The edit record is removed with the process, but the
// watcher must keep running — it is only the watcher that can save the
// changes once the (detached) real editor writes them and stops.
func TestEditUntouchedExitKeepsTempAndStaysWatchable(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/clean.txt", []byte("orig"))

	// A "launcher" that exits zero immediately without touching the file
	// (xdg-open semantics).
	editor := writeScript(t, "launcher.sh", `exit 0`)
	if err := m.EditRemoteText(tab, "/clean.txt", editor); err != nil {
		t.Fatalf("EditRemoteText: %v", err)
	}

	// The launcher's exit must not end the watch: write the temp copy now,
	// and the (still running) watcher must pick it up on stability and
	// re-upload. The edit record may already be gone (the process exited),
	// so discover the staged temp on disk instead of via the record.
	var tempPath string
	waitFor(t, 5*time.Second, "temp present after launcher exit", func() bool {
		ents, err := os.ReadDir(m.tmpDir)
		if err != nil {
			return false
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "edit-") && !strings.HasSuffix(e.Name(), ".log") {
				tempPath = filepath.Join(m.tmpDir, e.Name())
				return true
			}
		}
		return false
	})
	if err := os.WriteFile(tempPath, []byte("edited by the real editor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "remote updated by watcher after launcher exit",
		func() bool {
			return strings.Contains(string(readRemoteAll(t, m, tab, "/clean.txt")), "edited by the real editor")
		})
}
func TestEditProbeTooLarge(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab := "tab1"
	c, err := m.ClientFor(tab)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c, "/big.txt", make([]byte, MaxTextSize+1))

	editor := writeScript(t, "noop.sh", `exit 0`)

	if err := m.EditRemoteText(tab, "/big.txt", editor); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized edit err = %v, want ErrTooLarge", err)
	}
}

// TestEditFilesWithoutTextExtension covers the relaxed probe: files with a
// non-whitelisted/no extension (dotfiles like .bash_history, extensionless
// files like authorized_keys) are editable, and the save round trip works.
// Legacy non-UTF-8 encodings (CP1251 high bytes) must pass the content
// probe too — only NUL bytes are rejected. Each tab gets its own mem-server
// filesystem.
func TestEditFilesWithoutTextExtension(t *testing.T) {
	m, _ := newTestManagerFast(t)
	cases := []struct {
		tab    string
		remote string
		seed   string
	}{
		{"t-noext", "/authorized_keys", "ssh-ed25519 AAAAC3Nza test@host\n"},
		{"t-dotfile", "/.bash_history", "ls\ncd src\n"},
		{"t-legacy", "/notes.txt", "\xD0\xE8\xF0\xE8\xE2\xF2\r\n"}, // CP1251 "Привет", invalid UTF-8
	}
	editor := writeScript(t, "append.sh", `printf 'appended\n' >> "$1"; exit 0`)
	for _, tc := range cases {
		c, err := m.ClientFor(tc.tab)
		if err != nil {
			t.Fatal(err)
		}
		putFile(t, c, tc.remote, []byte(tc.seed))
		if err := m.EditRemoteText(tc.tab, tc.remote, editor); err != nil {
			t.Fatalf("EditRemoteText %s: %v", tc.remote, err)
		}
		waitFor(t, 5*time.Second, "remote content updated for "+tc.remote,
			func() bool {
				return strings.Contains(string(readRemoteAll(t, m, tc.tab, tc.remote)), "appended\n")
			})
	}
}

// TestEditBinaryContentRejected covers the head-window content probe: a NUL
// byte in the first sniffSize bytes refuses the edit with ErrBinary BEFORE
// any download or local staging (tmpDir stays empty), regardless of
// extension (a text file that turns binary mid-way is still caught, and so
// is a binary with a no/whitelisted extension).
func TestEditBinaryContentRejected(t *testing.T) {
	m, _ := newTestManagerFast(t)
	cases := []struct {
		tab    string
		remote string
		data   []byte
	}{
		{"t-binpng", "/photo.png", append([]byte{0x89, 'P', 'N', 'G'}, 0x00, 0x01)},
		{"t-binnul", "/app.txt", []byte("some text line\n\x00then binary\n")}, // NUL mid-head
		{"t-binnoext", "/coredump", []byte{0x7f, 'E', 'L', 'F', 0x01, 0x02, 0x03, 0x00}},
	}
	editor := writeScript(t, "noop.sh", `exit 0`)
	for _, tc := range cases {
		c, err := m.ClientFor(tc.tab)
		if err != nil {
			t.Fatal(err)
		}
		putFile(t, c, tc.remote, tc.data)
		if err := m.EditRemoteText(tc.tab, tc.remote, editor); !errors.Is(err, ErrBinary) {
			t.Fatalf("EditRemoteText %s err = %v, want ErrBinary", tc.remote, err)
		}
		// No temp copy/log may be staged when the probe refuses.
		dirents, err := os.ReadDir(m.tmpDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(dirents) != 0 {
			t.Fatalf("refused edit left temp files: %v", namesEntries(dirents))
		}
	}
}

func namesEntries(es []fs.DirEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestCleanupKillsEditorsAndEmptiesTmp(t *testing.T) {
	m, _ := newTestManagerFast(t)
	tab1, tab2 := "t1", "t2"
	// Each tab lazily creates its own mem-server filesystem, so seed each
	// file through that tab's own cached client.
	c1, err := m.ClientFor(tab1)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := m.ClientFor(tab2)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, c1, "/x.txt", []byte("x"))
	putFile(t, c2, "/y.txt", []byte("y"))

	editor := writeScript(t, "sleep30.sh", `sleep 30`)
	if err := m.EditRemoteText(tab1, "/x.txt", editor); err != nil {
		t.Fatal(err)
	}
	if err := m.EditRemoteText(tab2, "/y.txt", editor); err != nil {
		t.Fatal(err)
	}
	es1 := getEdit(m, tab1)
	es2 := getEdit(m, tab2)
	waitFor(t, 2*time.Second, "both edits registered",
		func() bool { return getEdit(m, tab1) != nil && getEdit(m, tab2) != nil })

	// Stray temp files that a previous crash might have left behind.
	stray := filepath.Join(m.tmpDir, "stale-temp")
	if err := os.WriteFile(stray, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Editor process groups are dead (signal 0 → ESRCH), edit records gone.
	waitFor(t, 5*time.Second, "editors dead", func() bool {
		return groupGone(es1.pgid) && groupGone(es2.pgid)
	})
	if getEdit(m, tab1) != nil || getEdit(m, tab2) != nil {
		t.Fatal("edit records not cleared by Cleanup")
	}
	// tmp/ is empty.
	entries, err := os.ReadDir(m.tmpDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("tmp/ not empty after Cleanup: %v", names)
	}

	// Idempotent.
	if err := m.Cleanup(); err != nil {
		t.Fatalf("second Cleanup: %v", err)
	}
}
