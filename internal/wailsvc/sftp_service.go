package wailsvc

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

// PickLocalFiles opens the native file picker and returns the chosen paths.
// tabID is ignored by this build's dialog-free fallback (kept in the contract
// per master plan §5). It returns the typed ErrSftpDialogUnsupported; the 5c
// frontend then shows a manual multi-path prompt.
func (s *SftpService) PickLocalFiles(tabID string, multi bool) ([]string, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	return s.mgr.PickLocalFiles(multi)
}

// Upload streams local files into remoteDir with progress events.
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

// DownloadThenSave downloads a remote file to a temp path. This build uses
// the documented fallback (no native save-dialog API wired): it returns the
// temp path and shows an app:toast with it.
func (s *SftpService) DownloadThenSave(tabID, remotePath string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.mgr.DownloadThenSave(tabID, remotePath)
}

// EditRemoteText downloads a text-like file to tmp/, opens the configured
// system editor, and starts the save-detection loop. The editor command
// comes from settings (config.Load); it is run verbatim with the temp path
// appended last.
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

// OpenRemoteFile downloads a remote file to a local temp path and launches
// the local system default handler on it (no file bytes over IPC, plan P002).
// The open command comes from settings (config.Load); it is run verbatim with
// the temp path appended last. Returns the temp path for the toast.
func (s *SftpService) OpenRemoteFile(tabID, remotePath string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	settings, err := config.Load()
	if err != nil {
		return "", err
	}
	return s.mgr.OpenRemoteFile(tabID, remotePath, settings.SftpOpenCommand)
}

// CancelEdit aborts an in-flight EditRemoteText.
func (s *SftpService) CancelEdit(tabID string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.mgr.CancelEdit(tabID)
}
