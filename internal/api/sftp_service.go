package api

import (
	"shelve/internal/config"
	"shelve/internal/sftp"
	"shelve/internal/vault"
)

// SftpService exposes SFTP browse, transfer and remote text-editing
// operations to the frontend (master plan §5). Every method delegates to the
// sftp.Manager and requires an unlocked vault (the connection lives on the
// encrypted session's SSH connection). Data never crosses IPC — only paths
// and events do (master plan §2 A5).
type SftpService struct {
	vault *vault.Vault
	mgr   *sftp.Manager
}

// NewSftpService wires the SFTP service around the shared manager.
func NewSftpService(v *vault.Vault, mgr *sftp.Manager) *SftpService {
	return &SftpService{vault: v, mgr: mgr}
}

func (s *SftpService) requireUnlocked() error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return nil
}

// IsActive reports whether a tab currently has a usable SFTP client: the
// vault must be unlocked and the tab ready with a live connection. It is
// side-effect free (never creates a client).
func (s *SftpService) IsActive(tabID string) bool {
	if !s.vault.IsUnlocked() {
		return false
	}
	return s.mgr.IsActive(tabID)
}

// List returns the sorted contents of the remote directory at path.
func (s *SftpService) List(tabID, path string) ([]SftpEntryDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	entries, err := s.mgr.List(tabID, path)
	if err != nil {
		return nil, err
	}
	return toSftpEntryDTOs(entries), nil
}

// Mkdir creates a single remote directory at path.
func (s *SftpService) Mkdir(tabID, path string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.mgr.Mkdir(tabID, path)
}

// Rename moves/renames a remote file or directory from → to.
func (s *SftpService) Rename(tabID, from, to string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.mgr.Rename(tabID, from, to)
}

// Remove deletes a remote file or empty directory.
func (s *SftpService) Remove(tabID, path string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.mgr.Remove(tabID, path)
}

// ---------------------------------------------------------------------
// Phase 5b transfers + remote text editing (real implementations).
// ---------------------------------------------------------------------

// Upload streams local files (paths chosen by the renderer through the native
// picker) into remoteDir with progress events.
func (s *SftpService) Upload(tabID string, localPaths []string, remoteDir string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.mgr.Upload(tabID, localPaths, remoteDir)
}

// Download streams a remote file to a local temp path and returns it.
func (s *SftpService) Download(tabID, remotePath string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.mgr.Download(tabID, remotePath)
}

// DownloadTo streams a remote file directly into destDir (chosen by the
// renderer through the native directory picker) as <destDir>/<basename> and
// returns the final path. It refuses to replace an existing file unless
// overwrite is true (sftp.ErrDestExists); an existing directory at the target
// is refused with sftp.ErrDestIsDir and never replaced. The renderer confirms
// a file replace and retries.
func (s *SftpService) DownloadTo(tabID, remotePath, destDir string, overwrite bool) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.mgr.DownloadTo(tabID, remotePath, destDir, overwrite)
}

// EditRemoteText downloads the file (any name, ≤ 2 MiB, content probing as
// text) to tmp/, opens the configured system editor, and starts the
// save-detection loop. The editor command comes from settings (config.Load);
// it is run verbatim with the temp path appended last.
func (s *SftpService) EditRemoteText(tabID, remotePath string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	settings, err := config.Load()
	if err != nil {
		return err
	}
	return s.mgr.EditRemoteText(tabID, remotePath, settings.TextEditorCommand)
}

// CancelEdit aborts an in-flight EditRemoteText.
func (s *SftpService) CancelEdit(tabID string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.mgr.CancelEdit(tabID)
}
