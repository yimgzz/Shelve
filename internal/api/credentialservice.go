package api

import (
	"shelve/internal/model"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// CredentialService is the named-credential CRUD surface for the frontend
// (plan P003 §4.3): secret-free read views, secret-bearing writes that
// only ever reach the encrypted vault. Like SessionService, every method
// requires an unlocked vault.
type CredentialService struct {
	store *store.Store
	vault *vault.Vault
}

// NewCredentialService wires the credential service.
func NewCredentialService(st *store.Store, v *vault.Vault) *CredentialService {
	return &CredentialService{store: st, vault: v}
}

func (s *CredentialService) requireUnlocked() error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return nil
}

// List returns every stored credential as a secret-free read view
// (ID, Name, User, AuthType, HasPassword, KeyPath — never the password).
func (s *CredentialService) List() ([]CredentialDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	creds := s.store.ListCredentials()
	out := make([]CredentialDTO, 0, len(creds))
	for _, c := range creds {
		out = append(out, ToCredentialDTO(c))
	}
	return out, nil
}

// Create adds a named credential and returns its ID.
func (s *CredentialService) Create(input CredentialInput) (string, error) {
	if err := s.requireUnlocked(); err != nil {
		return "", err
	}
	return s.store.CreateCredential(input.toModel())
}

// Update replaces a credential's editable fields (ID required).
//
// Password-merge rule (mirror of SessionService.UpdateSession): the read
// view never carries the password, so an empty incoming password on a
// password-auth credential means "keep the current password". Switching
// to key auth (or leaving a key credential on key) never resurrects a
// stale password.
func (s *CredentialService) Update(input CredentialInput) error {
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	cred := input.toModel()
	if cred.Auth.Type == model.AuthPassword && cred.Auth.Password == "" {
		old, err := s.store.Credential(cred.ID)
		if err != nil {
			return err
		}
		if old.Auth.Type == model.AuthPassword {
			cred.Auth.Password = old.Auth.Password
		}
	}
	return s.store.UpdateCredential(cred)
}

// Delete removes a credential. Sessions referencing it keep their inline
// snapshot (their CredentialID is soft-nulled); the returned count is the
// number of affected sessions (A8-style confirm dialog, plan P003 §4.5).
func (s *CredentialService) Delete(id string) (int, error) {
	if err := s.requireUnlocked(); err != nil {
		return 0, err
	}
	return s.store.DeleteCredential(id)
}

// Get returns the secret-free read view of one credential.
func (s *CredentialService) Get(id string) (CredentialDTO, error) {
	if err := s.requireUnlocked(); err != nil {
		return CredentialDTO{}, err
	}
	cred, err := s.store.Credential(id)
	if err != nil {
		return CredentialDTO{}, err
	}
	return ToCredentialDTO(cred), nil
}

// Usage reports how many sessions currently reference the credential, for
// the delete-confirm dialog (plan P003 §4.5).
func (s *CredentialService) Usage(id string) (int, error) {
	if err := s.requireUnlocked(); err != nil {
		return 0, err
	}
	if _, err := s.store.Credential(id); err != nil {
		return 0, err
	}
	return s.store.CredentialUsage(id), nil
}

// resolveSessionCredential applies the plan P003 §4.1 "single source of
// truth" rule at connect/test time: when the session references a
// credential, User+Auth are resolved FROM the credential, overriding any
// inline values. The inline fields remain the session's self-contained
// fallback for after the reference is cleared. A dangling reference (the
// store soft-nulls these on delete; this only catches externally-edited
// payloads) falls back to the inline snapshot untouched.
func resolveSessionCredential(st *store.Store, sess model.Session) model.Session {
	if sess.CredentialID == "" {
		return sess
	}
	cred, err := st.Credential(sess.CredentialID)
	if err != nil {
		return sess // inline snapshot fallback
	}
	sess.User = cred.User
	sess.Auth = cred.Auth
	return sess
}

// applyCredentialSnapshot merges a referenced credential into a session
// draft so the saved session stays self-contained and validates even when
// the frontend could not send the secret (passwords never cross IPC):
//   - a blank user is filled from the credential;
//   - the auth is replaced with the credential's (the credential is the
//     authoritative source while referenced, plan P003 §4.1).
//
// It also enforces that the reference exists (store layer does the same;
// this surfaces a nicer field error before store validation runs).
func (s *SessionService) applyCredentialSnapshot(sess *model.Session) error {
	if sess.CredentialID == "" {
		return nil
	}
	cred, err := s.store.Credential(sess.CredentialID)
	if err != nil {
		return &model.ValidationError{Field: "session.credentialId", Rule: "references unknown credential"}
	}
	if sess.User == "" {
		sess.User = cred.User
	}
	sess.Auth = cred.Auth
	return nil
}
