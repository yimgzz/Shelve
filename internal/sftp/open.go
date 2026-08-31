package sftp

// Plan P002 "Open file on double-click" (master plan §2 D4, A5): download a
// remote file to tmp/open-<ULID>.<ext> (0600) and launch the configured local
// "open command" (default xdg-open) on that path so the OS default handler
// opens it — like a normal file manager. Only a path crosses IPC; Go streams
// the bytes server-side.
//
// Unlike EditRemoteText this does NOT run the save-detection watcher and does
// NOT re-upload: the temp copy is left in place for the tmp/ sweep on
// lock/exit/startup (§8.8). The open command from settings is run verbatim
// (strings.Fields) with the temp path appended last — the same intentional
// variable-command pattern documented in edit.go (gosec G204).

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// OpenRemoteFile downloads the remote file at remotePath to a temp file under
// tmp/ (0600), launches the configured open command on that local path and
// returns the temp path. It errors if the path is a directory or the launch
// fails. The opened temp file is swept by the existing tmp/ lifecycle.
func (m *Manager) OpenRemoteFile(tabID, remotePath, openCmd string) (string, error) {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return "", err
	}
	abs, err := m.resolve(tabID, remotePath)
	if err != nil {
		return "", err
	}
	st, err := c.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("sftp: stat %s: %w", abs, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("sftp: open %s: is a directory", abs)
	}

	if err := m.ensureTmp(); err != nil {
		return "", err
	}
	tempPath := filepath.Join(m.tmpDir, "open-"+newULID()+path.Ext(abs))

	tf, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	rf, err := c.Open(abs)
	if err != nil {
		_ = tf.Close()
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("sftp: open %s: %w", abs, err)
	}
	if _, err := io.Copy(tf, rf); err != nil {
		_ = rf.Close()
		_ = tf.Close()
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("sftp: download %s: %w", abs, err)
	}
	_ = rf.Close()
	_ = tf.Close()

	argv := strings.Fields(openCmd)
	if len(argv) == 0 {
		_ = os.Remove(tempPath)
		return "", errors.New("sftp: open: empty open command")
	}
	// Command is run verbatim with the temp path appended last (documented).
	argv = append(argv, tempPath)
	//nolint:gosec // The configured open command is executed intentionally.
	cmd := exec.Command(argv[0], argv[1:]...)
	// Own process group so the opened app outlives this call independently.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("sftp: open: start: %w", err)
	}
	// Release the handle; the opener continues on its own. Its temp file is
	// swept by the tmp/ lifecycle (§8.8).
	_ = cmd.Process.Release()
	return tempPath, nil
}
