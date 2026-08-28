package wailsvc

import (
	"errors"

	"dummy-ssh-manager/internal/sshx"
	"dummy-ssh-manager/internal/store"
	"dummy-ssh-manager/internal/vault"
)

// ErrEngineNotWired is the stable placeholder returned by
// TestConnection until the Phase 3 SSH engine lands.
var ErrEngineNotWired = errors.New("ssh engine not wired yet (Phase 3)")

// SessionService is the tree/session CRUD surface for the frontend
// (master plan §5). Every method requires an unlocked vault.
type SessionService struct {
	store *store.Store
	vault *vault.Vault
	emit  Emitter
}

// NewSessionService wires the session-tree service.
func NewSessionService(st *store.Store, v *vault.Vault, emit Emitter) *SessionService {
	return &SessionService{store: st, vault: v, emit: emit}
}

func (s *SessionService) requireUnlocked() error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return nil
}

// Tree returns the ordered session tree (secret-free DTOs).
func (s *SessionService) Tree() ([]NodeDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	return toNodeDTOs(s.store.Tree()), nil
}

// CreateFolder adds a folder under parentID ("" = root).
func (s *SessionService) CreateFolder(parentID, name string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.store.CreateFolder(parentID, name)
}

// RenameFolder renames a folder.
func (s *SessionService) RenameFolder(id, name string) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.store.RenameFolder(id, name)
}

// MoveNode re-positions a node: newParentID ("" = root), sibling index.
func (s *SessionService) MoveNode(id, newParentID string, index int) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	return s.store.MoveNode(id, newParentID, index)
}

// DeleteNode removes a node (recursively for folders) and returns the
// number of removed sessions+folders (master plan A8 confirm dialog).
func (s *SessionService) DeleteNode(id string) (int, error) {
	if err := s.requireUnlocked(); err != nil {
		return 0, err
	}
	return s.store.DeleteNode(id)
}

// CreateSession creates a session under input.FolderID ("" = root).
func (s *SessionService) CreateSession(input SessionInput) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.store.CreateSession(input.FolderID, input.toModel())
}

// UpdateSession replaces a session's fields (ID and placement required
// to be unchanged; use MoveNode to move).
func (s *SessionService) UpdateSession(input SessionInput) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	if input.ID == "" {
		return errors.New("sessionService: update requires the session ID")
	}
	return s.store.UpdateSession(input.toModel())
}

// DuplicateSession copies a session into its folder with a " (copy)"
// name suffix.
func (s *SessionService) DuplicateSession(id string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.store.DuplicateSession(id)
}

// ValidateExtraArgs delegates to the strict Extra Args parser
// (master plan §2 D5, Phase 3); placeholder now: only "" is accepted.
func (s *SessionService) ValidateExtraArgs(extraArgs string) error {
	return sshx.ValidateExtraArgs(extraArgs)
}

// TestConnection is a placeholder until the Phase 3 engine: always
// returns ErrEngineNotWired (stable message for the frontend).
func (s *SessionService) TestConnection(sessionID string) error {
	_ = sessionID
	return ErrEngineNotWired
}
