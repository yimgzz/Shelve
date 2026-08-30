package wailsvc

import (
	"errors"

	"dummy-ssh-manager/internal/sftp"
	"dummy-ssh-manager/internal/vault"
)

// ErrSftpNotImplemented is the typed error returned by the Phase 5b stubs
// (PickLocalFiles, Upload, Download, DownloadThenSave, EditRemoteText,
// CancelEdit) until transfers/editing land in Phase 5b.
var ErrSftpNotImplemented = errors.New("sftp: not implemented (phase 5b)")

// SftpService exposes SFTP browse operations to the frontend (master plan
// §5). The real browse methods delegate to the sftp.Manager; the transfer
// and editing methods are stubs replaced in Phase 5b. Every method requires
// an unlocked vault (the connection lives on the encrypted session's SSH
// connection).
type SftpService struct {
	vault  *vault.Vault
	mgr    *sftp.Manager
	tmpDir string
}

// NewSftpService wires the SFTP service. tmpDir is the config-directory
// tmp/ path used by the Phase 5b edit-temp flow (master plan §4, §8.8).
func NewSftpService(v *vault.Vault, mgr *sftp.Manager, tmpDir string) *SftpService {
	return &SftpService{vault: v, mgr: mgr, tmpDir: tmpDir}
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
// Phase 5b stubs — replaced with real streaming/editing implementations.
// Each still enforces the vault-unlock gate before returning the typed
// ErrSftpNotImplemented.
// ---------------------------------------------------------------------

// PickLocalFiles opens the native file picker and returns the chosen paths.
func (s *SftpService) PickLocalFiles(tabID string, multi bool) ([]string, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	return nil, ErrSftpNotImplemented
}

// Upload streams local files into remoteDir.
func (s *SftpService) Upload(tabID string, localPaths []string, remoteDir string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return ErrSftpNotImplemented
}

// Download streams a remote file to a local temp path (returns it).
func (s *SftpService) Download(tabID, remotePath string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return "", ErrSftpNotImplemented
}

// DownloadThenSave streams a remote file through the native save dialog
// (returns the final path).
func (s *SftpService) DownloadThenSave(tabID, remotePath string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return "", ErrSftpNotImplemented
}

// EditRemoteText downloads a text-like file to tmp/, opens the system
// editor, and starts the save-detection loop.
func (s *SftpService) EditRemoteText(tabID, remotePath string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return ErrSftpNotImplemented
}

// CancelEdit aborts an in-flight EditRemoteText.
func (s *SftpService) CancelEdit(tabID string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return ErrSftpNotImplemented
}
