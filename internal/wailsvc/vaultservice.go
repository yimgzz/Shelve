package wailsvc

import (
	"context"
	"log"
	"sync"
	"time"

	"shelve/internal/config"
	"shelve/internal/sftp"
	"shelve/internal/sshengine"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// lockShutdownTimeout bounds the engine teardown inside Lock
// (master plan §5: disconnect all before zeroizing the key).
const lockShutdownTimeout = 3 * time.Second

// VaultStatusDTO reports the vault state machine (master plan §4).
type VaultStatusDTO struct {
	State    string `json:"state"` // "create" | "locked" | "unlocked"
	Unlocked bool   `json:"unlocked"`
}

// VaultService exposes the master-password lifecycle and the
// connection prompt flows to the frontend (master plan §5). It gates
// no other service: SessionService checks the vault state itself.
type VaultService struct {
	vault  *vault.Vault
	store  *store.Store
	engine *sshengine.Manager
	sftp   *sftp.Manager
	path   string
	emit   Emitter

	// unlockMu serializes the master-password transitions (create/unlock)
	// so racing submits never run parallel Argon2id derivations — each
	// run saturates the CPU for seconds on slower machines, and a second
	// concurrent run would only multiply the stall.
	unlockMu sync.Mutex
}

// NewVaultService wires the vault lifecycle service. sftp is the shared SFTP
// manager, cleaned up (editors killed, tmp/ swept) on Lock — the same place
// the engine is shut down (master plan phase 5b task 2).
func NewVaultService(v *vault.Vault, st *store.Store, engine *sshengine.Manager, sftp *sftp.Manager, emit Emitter) *VaultService {
	return &VaultService{
		vault:  v,
		store:  st,
		engine: engine,
		sftp:   sftp,
		path:   config.File(config.VaultFileName),
		emit:   emit,
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
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()
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
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()
	if s.vault.IsUnlocked() {
		return nil // a concurrent call already unlocked the vault
	}
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

// Lock disconnects every live session, flushes the tree, zeroizes the
// key and emits the state change (master plan §5: disconnect all
// before zeroizing). It is a safe no-op when the vault is already
// locked.
func (s *VaultService) Lock() error {
	if !s.vault.IsUnlocked() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockShutdownTimeout)
	s.engine.Shutdown(ctx)
	cancel()
	// Kill live editor process groups and sweep tmp/ (master plan §8.8).
	if err := s.sftp.Cleanup(); err != nil {
		log.Printf("vault: sftp cleanup on lock: %v", err)
	}
	if err := s.store.Flush(); err != nil {
		return err
	}
	s.vault.Lock()
	s.emitVaultState(false)
	return nil
}

// ApproveHostKey accepts the pending host-key prompt for connID (the
// vault:hostkey-prompt "Accept and connect" button). A locked vault has
// no live prompts; a stale connID → ErrNoPendingPrompt.
func (s *VaultService) ApproveHostKey(connID string) error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return s.engine.ApproveHostKey(connID)
}

// RejectHostKey rejects the pending host-key prompt for connID.
func (s *VaultService) RejectHostKey(connID string) error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return s.engine.RejectHostKey(connID)
}

// SubmitKeyPassphrase submits the entered passphrase for the pending
// key-passphrase prompt on connID. The password is handed to the dial,
// cached in the engine's process-memory cache and never logged (master
// plan §8.3).
func (s *VaultService) SubmitKeyPassphrase(connID, passphrase string) error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return s.engine.SubmitKeyPassphrase(connID, passphrase)
}

func (s *VaultService) emitVaultState(unlocked bool) {
	s.emit.Emit(EventVaultStateChanged, VaultStatePayload{Unlocked: unlocked})
}
