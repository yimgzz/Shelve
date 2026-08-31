package wailsvc

import (
	"shelve/internal/sshengine"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// TerminalService exposes live terminal tabs to the frontend (master
// plan §5). Connect requires an unlocked vault (the session is resolved
// from the encrypted tree); the remaining methods delegate to the
// engine's tab records.
type TerminalService struct {
	store *store.Store
	vault *vault.Vault
	mgr   *sshengine.Manager
}

// NewTerminalService wires the terminal-tab service.
func NewTerminalService(st *store.Store, v *vault.Vault, mgr *sshengine.Manager) *TerminalService {
	return &TerminalService{store: st, vault: v, mgr: mgr}
}

// Connect opens a terminal tab for a stored session (master plan §5):
// resolves the session from the store (unknown ID →
// store.ErrNodeNotFound), requires the vault unlocked, and starts the
// engine dial. The connection runs asynchronously; progress is reported
// through the terminal:status events with the returned tabID.
func (s *TerminalService) Connect(sessionID string) (string, error) {
	if !s.vault.IsUnlocked() {
		return "", vault.ErrLocked
	}
	sess, err := s.store.Session(sessionID)
	if err != nil {
		return "", err
	}
	return s.mgr.Connect(&sess)
}

// Disconnect closes a tab and removes its record (master plan §5).
func (s *TerminalService) Disconnect(tabID string) error {
	return s.mgr.Disconnect(tabID)
}

// Write feeds decoded terminal input to the tab's pty (master plan §5).
func (s *TerminalService) Write(tabID, dataB64 string) error {
	return s.mgr.Write(tabID, dataB64)
}

// Resize applies a window resize to the tab's pty (master plan §5).
func (s *TerminalService) Resize(tabID string, cols, rows int) error {
	return s.mgr.Resize(tabID, cols, rows)
}

// Reconnect re-dials a non-ready tab under the same tabID (the UI
// Retry button, master plan A4).
func (s *TerminalService) Reconnect(tabID string) error {
	return s.mgr.Reconnect(tabID)
}
