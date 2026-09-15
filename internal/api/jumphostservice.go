package api

import (
	"shelve/internal/model"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// JumpHostService is the saved-jump-host CRUD surface for the frontend
// (plan P006): secret-free read views, secret-bearing writes that only
// ever reach the encrypted vault. Like CredentialService, every method
// requires an unlocked vault.
type JumpHostService struct {
	store *store.Store
	vault *vault.Vault
}

// NewJumpHostService wires the saved-jump-host service.
func NewJumpHostService(st *store.Store, v *vault.Vault) *JumpHostService {
	return &JumpHostService{store: st, vault: v}
}

func (s *JumpHostService) requireUnlocked() error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return nil
}

// List returns every stored saved jump host as a secret-free read view
// (ID, Name, Host, Port, User, AuthType, HasPassword, KeyPath — never
// the password).
func (s *JumpHostService) List() ([]SavedJumpHostDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	hosts := s.store.ListSavedJumpHosts()
	out := make([]SavedJumpHostDTO, 0, len(hosts))
	for _, jh := range hosts {
		out = append(out, ToSavedJumpHostDTO(jh))
	}
	return out, nil
}

// Create adds a named saved jump host and returns its ID.
func (s *JumpHostService) Create(input SavedJumpHostInput) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.store.CreateSavedJumpHost(input.toModel())
}

// Update replaces a saved jump host's editable fields (ID required).
//
// Password-merge rule (mirror of CredentialService.Update): the read
// view never carries the password, so an empty incoming password on a
// password-auth saved jump host means "keep the current password".
// Switching to key auth (or leaving a key host on key) never resurrects
// a stale password.
func (s *JumpHostService) Update(input SavedJumpHostInput) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	jh := input.toModel()
	if jh.Auth.Type == model.AuthPassword && jh.Auth.Password == "" {
		old, err := s.store.SavedJumpHost(jh.ID)
		if err != nil {
			return err
		}
		if old.Auth.Type == model.AuthPassword {
			jh.Auth.Password = old.Auth.Password
		}
	}
	return s.store.UpdateSavedJumpHost(jh)
}

// Delete removes a saved jump host. Sessions referencing it keep their
// inline snapshot (their JumpHostRef is soft-nulled); the returned count
// is the number of affected sessions (A8-style confirm dialog, plan P006).
func (s *JumpHostService) Delete(id string) (int, error) {
	if err := s.requireUnlocked(); err != nil {
		return 0, err
	}
	return s.store.DeleteSavedJumpHost(id)
}

// Get returns the secret-free read view of one saved jump host.
func (s *JumpHostService) Get(id string) (SavedJumpHostDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return SavedJumpHostDTO{}, err
	}
	jh, err := s.store.SavedJumpHost(id)
	if err != nil {
		return SavedJumpHostDTO{}, err
	}
	return ToSavedJumpHostDTO(jh), nil
}

// Usage reports how many sessions currently reference the saved jump
// host, for the delete-confirm dialog (plan P006).
func (s *JumpHostService) Usage(id string) (int, error) {
	if err := s.requireUnlocked(); err != nil {
		return 0, err
	}
	if _, err := s.store.SavedJumpHost(id); err != nil {
		return 0, err
	}
	return s.store.SavedJumpHostUsage(id), nil
}

// resolveSessionJumpHost applies the plan P006 "single source of truth"
// rule at connect/test time: when the session references a saved jump
// host, the saved host's hop replaces the entire inline chain. The
// inline rows remain the session's self-contained fallback for after the
// reference is cleared. A dangling reference (the store soft-nulls these
// on delete; this only catches externally-edited payloads) falls back to
// the inline snapshot untouched.
func resolveSessionJumpHost(st *store.Store, sess model.Session) model.Session {
	if sess.JumpHostRef == "" {
		return sess
	}
	jh, err := st.SavedJumpHost(sess.JumpHostRef)
	if err != nil {
		return sess // inline snapshot fallback
	}
	sess.JumpHosts = []model.JumpHost{
		{Host: jh.Host, Port: jh.Port, User: jh.User, Auth: jh.Auth, Bastion: jh.Bastion},
	}
	return sess
}

// applyJumpHostSnapshot merges a referenced saved jump host into a
// session draft so the saved session stays self-contained and validates
// even when the frontend could not send the secret (passwords never
// cross IPC): the inline chain is replaced with the saved host's hop —
// the saved host is the authoritative source while referenced (plan P006).
//
// It also enforces that the reference exists (store layer does the same;
// this surfaces a nicer field error before store validation runs).
func (s *SessionService) applyJumpHostSnapshot(sess *model.Session) error {
	if sess.JumpHostRef == "" {
		return nil
	}
	jh, err := s.store.SavedJumpHost(sess.JumpHostRef)
	if err != nil {
		return &model.ValidationError{Field: "session.jumpHostRef", Rule: "references unknown saved jump host"}
	}
	sess.JumpHosts = []model.JumpHost{
		{Host: jh.Host, Port: jh.Port, User: jh.User, Auth: jh.Auth, Bastion: jh.Bastion},
	}
	return nil
}
