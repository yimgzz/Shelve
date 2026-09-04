package sftp

// Phase 5b remote text-file editing (master plan §2 D4): download any remote
// file (≤ MaxTextSize = 2 MiB) to tmp/, launch the configured system editor
// on a copy, watch for a stable mtime/size, and on stability re-upload
// atomically (temp + rename). Any file can be opened regardless of
// extension; before
// the full download only the first sniffSize bytes are read, and a file
// containing a NUL byte in that window (the file(1) text/binary heuristic)
// is refused with ErrBinary without downloading or staging anything. One
// edit per tab; CancelEdit and Cleanup own the process-group lifecycle so
// editors are never orphaned.
//
// The editor command from settings is run verbatim (strings.Fields) with the
// temp path appended last. This is intentional — the user explicitly chose the
// command — so the variable-command execution flagged by gosec (G204) is
// expected and documented here.
//
// The stat (mtime/size) watcher is the ONLY save trigger. The editor process's exit is
// deliberately NOT treated as a terminal signal: non-blocking launchers (the
// default xdg-open) exit within milliseconds while the real editor, a
// detached child, is still running, so an exit-based "session over" decision
// would stop watching before the user's first save. The watcher therefore
// runs until a single successful save, CancelEdit, or Cleanup; a file opened
// and closed without changes keeps its temp copy (swept by the tmp lifecycle)
// and produces no user-facing messages.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

// Typed errors for the edit state machine.
var (
	// ErrTooLarge: the remote file exceeds MaxTextSize (2 MiB).
	ErrTooLarge = errors.New("sftp: file too large to edit")
	// ErrBinary: the remote file's head window contains a NUL byte, so the
	// content probes as binary and is refused for text editing.
	ErrBinary = errors.New("sftp: file looks binary; use Download instead")
	// ErrAlreadyEditing: a second edit on the same tab is refused.
	ErrAlreadyEditing = errors.New("sftp: already editing on this tab")
)

// killEscalateTimeout is how long we wait after SIGTERM before SIGKILLing a
// live editor process group (master plan phase 5b task 2 CancelEdit/Cleanup).
const killEscalateTimeout = 3 * time.Second

// sniffSize is the number of leading bytes probed to classify content as
// text vs binary before the full download (8 KiB — the classic window used
// by file(1): a NUL byte in the head means binary).
const sniffSize = 8 << 10

// editState tracks one in-flight EditRemoteText. cmd/pgid identify the editor
// process group; tempPath is the local editable copy; saved/canceled describe
// the terminal transition. Watcher and waiter goroutines coordinate via mu.
type editState struct {
	mu         sync.Mutex
	tabID      string
	remote     string // resolved remote path
	tempPath   string
	logPath    string
	cmd        *exec.Cmd
	pgid       int
	saved      bool
	canceled   bool
	cancel     chan struct{}
	dirty      bool
	lastMtime  time.Time
	lastSize   int64
	lastChange time.Time
}

// stop idempotently closes the cancel channel (stops the watcher).
func (es *editState) stop() {
	es.mu.Lock()
	defer es.mu.Unlock()
	if !es.canceled {
		es.canceled = true
		close(es.cancel)
	}
}

// EditRemoteText downloads a remote file (any name, ≤ MaxTextSize, content
// probing as text) to tmp/edit-<ULID>.<ext> (0600), launches the configured
// editor command with the temp path appended last, and starts the
// save-detection watcher. It returns once the editor is started; the
// watcher/waiter goroutines run the state machine. Rejections, in order:
// ErrAlreadyEditing, ErrTooLarge (stat), ErrBinary (head-window content
// probe — no download happens in that case).
func (m *Manager) EditRemoteText(tabID, remotePath, editorCmd string) error {
	m.editsMu.Lock()
	if _, ok := m.edits[tabID]; ok {
		m.editsMu.Unlock()
		return ErrAlreadyEditing
	}
	m.editsMu.Unlock()

	c, err := m.ClientFor(tabID)
	if err != nil {
		return err
	}
	abs, err := m.resolve(tabID, remotePath)
	if err != nil {
		return err
	}
	st, err := c.Stat(abs)
	if err != nil {
		return fmt.Errorf("sftp: stat %s: %w", abs, err)
	}
	if st.IsDir() {
		return fmt.Errorf("sftp: edit %s: is a directory", abs)
	}
	if st.Size() > MaxTextSize {
		return fmt.Errorf("%w: %s (%d bytes)", ErrTooLarge, abs, st.Size())
	}
	if err := m.sniffText(c, abs); err != nil {
		return err
	}

	if err := m.ensureTmp(); err != nil {
		return err
	}
	ul := newULID()
	tempPath := filepath.Join(m.tmpDir, "edit-"+ul+path.Ext(abs))
	logPath := filepath.Join(m.tmpDir, "edit-"+ul+".log")

	// Download the remote content to the temp copy (0600).
	tf, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	rf, err := c.Open(abs)
	if err != nil {
		_ = tf.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("sftp: open %s: %w", abs, err)
	}
	if _, err := io.Copy(tf, rf); err != nil {
		_ = rf.Close()
		_ = tf.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("sftp: download %s: %w", abs, err)
	}
	_ = rf.Close()
	_ = tf.Close()

	// Capture the pristine download mtime/size BEFORE the editor runs so the
	// watcher treats any later change as a user edit (task 2 save detection).
	dlStat, err := os.Stat(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	baselineMtime := dlStat.ModTime()
	baselineSize := dlStat.Size()

	// Editor stdout/stderr → a 0600 log under tmp/.
	lf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(tempPath)
		return err
	}

	argv := strings.Fields(editorCmd)
	if len(argv) == 0 {
		_ = lf.Close()
		_ = os.Remove(tempPath)
		_ = os.Remove(logPath)
		return errors.New("sftp: edit: empty text editor command")
	}
	// Command is run verbatim with the temp path appended last (documented).
	argv = append(argv, tempPath)
	//nolint:gosec // The configured editor command is executed intentionally.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	// Own process group → clean cancel/kill of the whole tree (task 2).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		_ = os.Remove(tempPath)
		_ = os.Remove(logPath)
		return fmt.Errorf("sftp: edit: start editor: %w", err)
	}

	es := &editState{
		tabID:     tabID,
		remote:    abs,
		tempPath:  tempPath,
		logPath:   logPath,
		cmd:       cmd,
		pgid:      cmd.Process.Pid, // Setpgid ⇒ child pgid == child PID
		cancel:    make(chan struct{}),
		lastMtime: baselineMtime,
		lastSize:  baselineSize,
	}
	m.editsMu.Lock()
	m.edits[tabID] = es
	m.editsMu.Unlock()

	go m.editWatcher(es, c)
	go m.editWaiter(es)
	return nil
}

// sniffText reads only the first sniffSize bytes of abs over SFTP and
// returns ErrBinary when the window contains a NUL byte — the classic
// text/binary heuristic (file(1) treats a NUL in the head 8 KiB as binary).
// Non-UTF-8 (e.g. CP1251/Latin-1) text passes: only NUL bytes are rejected,
// so legacy-encoded files stay editable. An empty file probes as text. The
// probe reads a small window, not the whole file, and nothing local is
// staged on refusal.
func (m *Manager) sniffText(c *sftp.Client, abs string) error {
	rf, err := c.Open(abs)
	if err != nil {
		return fmt.Errorf("sftp: open %s: %w", abs, err)
	}
	defer rf.Close()
	buf := make([]byte, sniffSize)
	n, err := io.ReadFull(rf, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("sftp: read %s: %w", abs, err)
	}
	if bytes.IndexByte(buf[:n], 0) >= 0 {
		return fmt.Errorf("%w: %s", ErrBinary, abs)
	}
	return nil
}

// editWatcher polls the temp file's stat (mtime AND size) every editPoll;
// once it has been modified and then stable for editStability, it saves
// (atomically re-upload + rename) and stops after that single successful
// save (documented). A change counts as either an mtime or a size delta:
// the size also catches saves on filesystems with coarse mtime granularity
// (a write inside the same mtime quantum as the download). On a save
// failure it keeps watching so a later stability window can retry. The
// watcher does not react to the editor process exiting (see the package
// comment on non-blocking launchers): for launcher editors the real writing
// process outlives the launched command, so only save/cancel/cleanup end the
// watch.
func (m *Manager) editWatcher(es *editState, c *sftp.Client) {
	poll := m.editPoll
	if poll <= 0 {
		poll = time.Second
	}
	stability := m.editStability
	if stability <= 0 {
		stability = 3 * time.Second
	}
	for {
		select {
		case <-es.cancel:
			return
		case <-time.After(poll):
		}
		st, err := os.Stat(es.tempPath)
		if err != nil {
			continue // temp removed (cleanup); keep polling until cancel
		}
		mt := st.ModTime()
		sz := st.Size()
		es.mu.Lock()
		if es.lastMtime.IsZero() {
			// First observation: record the baseline; not yet dirty (a file
			// that is merely opened and closed without edits must not save).
			es.lastMtime = mt
			es.lastSize = sz
		} else if !mt.Equal(es.lastMtime) || sz != es.lastSize {
			es.lastMtime = mt
			es.lastSize = sz
			es.lastChange = time.Now()
			es.dirty = true
		}
		stable := es.dirty && time.Since(es.lastChange) >= stability
		es.mu.Unlock()
		if !stable {
			continue
		}
		if err := m.editSave(es, c); err != nil {
			m.emitToast("error", "Failed to save edited file: "+err.Error())
			continue
		}
		return
	}
}

// editSave uploads the temp copy to remote + ".tmp.<ULID>" in the same remote
// dir, then atomically renames it over the original, toasts, deletes the temp
// and stops the watcher (es.stop closes the cancel channel).
func (m *Manager) editSave(es *editState, c *sftp.Client) error {
	tmpRemote := es.remote + ".tmp." + newULID()
	lf, err := os.Open(es.tempPath)
	if err != nil {
		return err
	}
	rf, err := c.Create(tmpRemote)
	if err != nil {
		_ = lf.Close()
		return err
	}
	if _, err := io.Copy(rf, lf); err != nil {
		_ = rf.Close()
		_ = lf.Close()
		_ = c.Remove(tmpRemote)
		return err
	}
	_ = rf.Close()
	_ = lf.Close()
	if err := m.overwriteRename(c, tmpRemote, es.remote); err != nil {
		_ = c.Remove(tmpRemote)
		return err
	}
	es.mu.Lock()
	es.saved = true
	es.mu.Unlock()
	m.emitToast("info", "Saved to "+es.remote)
	_ = os.Remove(es.tempPath)
	es.stop()
	return nil
}

// overwriteRename atomically replaces `to` with `from` (master plan phase 5b
// task 2: ".tmp.<ULID>" → rename over the original). The SFTP-v2 Rename
// refuses to overwrite an existing target, so we prefer the extended
// posix-rename (POSIX replace semantics) and only fall back to remove+rename
// when the server lacks the extension.
func (m *Manager) overwriteRename(c *sftp.Client, from, to string) error {
	if err := c.PosixRename(from, to); err == nil {
		return nil
	} else if !errors.Is(err, sftp.ErrSSHFxOpUnsupported) {
		return err
	}
	// Server without posix-rename: best-effort remove + rename.
	_ = c.Remove(to)
	if err := c.Rename(from, to); err != nil {
		return fmt.Errorf("sftp: rename %s → %s: %w", from, to, err)
	}
	return nil
}

// editWaiter blocks on the editor process and, once it exits, drops the edit
// record and removes the editor log. It emits nothing on purpose (see the
// package comment): with a launcher like xdg-open the process exits within
// milliseconds while the real editor is still open, so an exit-time message
// would fire on every open and a save could no longer be reached. User
// feedback is the "Saved to …" toast from editSave (and error toasts from the
// watcher); with no save the temp copy is kept silently and swept by the tmp
// lifecycle.
func (m *Manager) editWaiter(es *editState) {
	_ = es.cmd.Wait()

	m.editsMu.Lock()
	if cur, ok := m.edits[es.tabID]; ok && cur == es {
		delete(m.edits, es.tabID)
	}
	m.editsMu.Unlock()

	_ = os.Remove(es.logPath)
}

// CancelEdit aborts an in-flight edit for the tab: it stops the watcher (no
// upload), signals the editor process group (SIGTERM, escalating to SIGKILL
// after killEscalateTimeout), deletes the temp + log copies and resets the
// state. Idempotent no-op when nothing is editing on the tab.
func (m *Manager) CancelEdit(tabID string) error {
	m.editsMu.Lock()
	es, ok := m.edits[tabID]
	if ok {
		delete(m.edits, tabID)
	}
	m.editsMu.Unlock()
	if !ok {
		return nil
	}
	es.stop() // stops the watcher before signaling → guaranteed no upload
	killGroup(es.pgid)
	_ = os.Remove(es.tempPath)
	_ = os.Remove(es.logPath)
	return nil
}

// Cleanup terminates every live editor process group, deletes all temp files
// under tmp/ (best effort) and resets the edit state. Idempotent. Called on
// lock, app shutdown and app start (stale sweep) — master plan §8.8.
func (m *Manager) Cleanup() error {
	m.editsMu.Lock()
	edits := m.edits
	m.edits = map[string]*editState{}
	m.editsMu.Unlock()

	for _, es := range edits {
		es.stop()
		killGroup(es.pgid)
		_ = os.Remove(es.tempPath)
		_ = os.Remove(es.logPath)
	}
	return removeAllInDir(m.tmpDir)
}

// killGroup SIGTERMs the process group, escalating to SIGKILL after
// killEscalateTimeout. pgid must be > 0.
func killGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.AfterFunc(killEscalateTimeout, func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
}

// removeAllInDir removes every entry under dir (not the dir itself), best
// effort: the first error is returned, others ignored.
func removeAllInDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
