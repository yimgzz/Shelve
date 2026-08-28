package wailsvc

import (
	"dummy-ssh-manager/internal/config"
	"dummy-ssh-manager/internal/store"
	"dummy-ssh-manager/internal/vault"
)

// VaultStatusDTO reports the vault state machine (master plan §4).
type VaultStatusDTO struct {
	State    string `json:"state"` // "create" | "locked" | "unlocked"
	Unlocked bool   `json:"unlocked"`
}

// VaultService exposes the master-password lifecycle to the frontend
// (master plan §5). It gates no other service: SessionService checks
// the vault state itself.
type VaultService struct {
	vault *vault.Vault
	store *store.Store
	path  string
	emit  Emitter
}

// NewVaultService wires the vault lifecycle service.
func NewVaultService(v *vault.Vault, st *store.Store, emit Emitter) *VaultService {
	return &VaultService{
		vault: v,
		store: st,
		path:  config.File(config.VaultFileName),
		emit:  emit,
	}
}

// Status returns the current vault state for the unlock gate.
func (s *VaultService) Status() (VaultStatusDTO, error) {
	st := s.vault.Status()
	return VaultStatusDTO{
		State:    st.String(),
		Unlocked: st == vault.StatusUnlocked,
	}, nil
}

// CreateVault writes the first-run vault (empty tree) and unlocks it.
func (s *VaultService) CreateVault(password string) error {
	if s.vault.IsUnlocked() {
		return nil // nothing to do; already first-run unlocked
	}
	payload, err := s.store.Encode()
	if err != nil {
		return err
	}
	if err := s.vault.Create(s.path, password, payload); err != nil {
		return err
	}
	s.emitVaultState(true)
	return nil
}

// Unlock derives the key from the master password and loads the tree.
// Wrong password / corrupt file return the vault's typed errors; the
// frontend shows them inline.
func (s *VaultService) Unlock(password string) error {
	payload, err := s.vault.Open(s.path, password)
	if err != nil {
		return err
	}
	if err := s.store.Load(payload); err != nil {
		s.vault.Lock() // never stay unlocked on a broken payload
		return err
	}
	s.emitVaultState(true)
	return nil
}

// Lock flushes the tree, zeroizes the key and emits the state change.
// It is a safe no-op when the vault is already locked.
func (s *VaultService) Lock() error {
	if !s.vault.IsUnlocked() {
		return nil
	}
	if err := s.store.Flush(); err != nil {
		return err
	}
	s.vault.Lock()
	s.emitVaultState(false)
	return nil
}

func (s *VaultService) emitVaultState(unlocked bool) {
	s.emit.Emit(EventVaultStateChanged, VaultStatePayload{Unlocked: unlocked})
}
