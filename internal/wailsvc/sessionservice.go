package wailsvc

import (
	"errors"

	"dummy-ssh-manager/internal/model"
	"dummy-ssh-manager/internal/sshengine"
	"dummy-ssh-manager/internal/sshx"
	"dummy-ssh-manager/internal/store"
	"dummy-ssh-manager/internal/vault"
)

// SearchResultDTO is one flat session search result (master plan §2 A9):
// no password material, folder path included for display context.
type SearchResultDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	User       string `json:"user"`
	FolderPath string `json:"folderPath"`
}

// SessionService is the tree/session CRUD surface for the frontend
// (master plan §5). Every method requires an unlocked vault.
type SessionService struct {
	store  *store.Store
	vault  *vault.Vault
	engine *sshengine.Manager
	emit   Emitter
}

// NewSessionService wires the session-tree service.
func NewSessionService(st *store.Store, v *vault.Vault, engine *sshengine.Manager, emit Emitter) *SessionService {
	return &SessionService{store: st, vault: v, engine: engine, emit: emit}
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

// Search returns flat session-search results for a live query
// (master plan §2 A9): sessions whose Name/Host/User contain q,
// with their folder path. Empty q yields no results.
func (s *SessionService) Search(q string) ([]SearchResultDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	hits := s.store.Search(q)
	out := make([]SearchResultDTO, 0, len(hits))
	for _, h := range hits {
		out = append(out, SearchResultDTO{
			ID:         h.ID,
			Name:       h.Name,
			Host:       h.Host,
			User:       h.User,
			FolderPath: h.FolderPath,
		})
	}
	return out, nil
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

// Session returns the secret-free read view of one stored session (used
// to prefill the session editor). Passwords never leave the vault: only
// HasPassword/AuthType/keyPath are exposed (master plan §5 DTO contract).
func (s *SessionService) Session(id string) (SessionDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return SessionDTO{}, err
	}
	sess, err := s.store.Session(id)
	if err != nil {
		return SessionDTO{}, err
	}
	return ToSessionDTO(sess), nil
}

// UpdateSession replaces a session's editable fields (ID and placement
// required to be unchanged; use MoveNode to move).
//
// Password-merge rule (Phase 4b, task 5): the session editor never
// receives stored passwords, so an empty incoming Password field for a
// password-auth session means "keep the current password". When the
// stored auth (or jump-host auth at the same index) is a password and
// the incoming DTO's password is "", the stored password is preserved.
// This applies only when the incoming auth still selects password (so a
// password→key switch with a blank password does not resurrect the old
// credential and break the auth XOR invariant). The merged result still
// passes model.Validate before it is written.
func (s *SessionService) UpdateSession(input SessionInput) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	if input.ID == "" {
		return errors.New("sessionService: update requires the session ID")
	}
	old, err := s.store.Session(input.ID)
	if err != nil {
		return err
	}
	sess := input.toModel()
	mergeAuthPassword(&sess.Auth, old.Auth)
	for i := range sess.JumpHosts {
		if i < len(old.JumpHosts) {
			mergeAuthPassword(&sess.JumpHosts[i].Auth, old.JumpHosts[i].Auth)
		}
	}
	return s.store.UpdateSession(sess)
}

// mergeAuthPassword preserves a stored password when the incoming auth
// is a password type with a blank password. src is the stored credential;
// dst is the incoming one. A non-password incoming type is left untouched.
func mergeAuthPassword(dst *model.Auth, src model.Auth) {
	if dst.Type == model.AuthPassword && dst.Password == "" && src.Password != "" {
		dst.Password = src.Password
	}
}

// DuplicateSession copies a session into its folder with a " (copy)"
// name suffix.
func (s *SessionService) DuplicateSession(id string) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.store.DuplicateSession(id)
}

// ValidateExtraArgs runs the strict Extra Args parser (master plan
// §2 D5, Phase 3a); error messages are user-facing and shown verbatim
// in the session editor's inline validation.
func (s *SessionService) ValidateExtraArgs(extraArgs string) error {
	return sshx.ValidateExtraArgs(extraArgs)
}

// TestConnection dials a session draft's full hop chain without opening
// a terminal or forwards (the editor's [Test connection] button). It
// enforces the vault-unlock gate and the draft-validation checks first,
// then runs the real engine dial; a successful handshake returns nil and
// any failure is attributed to the failing hop. No tab record is created.
func (s *SessionService) TestConnection(in SessionInput) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	sess := in.toModel()
	if err := sess.Validate(); err != nil {
		return err
	}
	return s.engine.TestConnection(&sess)
}
