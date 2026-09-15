package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shelve/internal/config"
	"shelve/internal/model"
	"shelve/internal/sshengine"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// Import modes (plan config-export-import §1 D3).
const (
	// ImportModeMerge assigns fresh IDs and drops the import under a new
	// Imported <date> folder; local settings are untouched.
	ImportModeMerge = "merge"
	// ImportModeReplace swaps the whole tree and applies the imported
	// settings.
	ImportModeReplace = "replace"
)

// maxImportFileSize caps the file the backend reads for an import; beyond it
// the import is rejected instead of allocating.
const maxImportFileSize = 64 << 20 // 64 MiB

// transferReplaceTimeout bounds the engine teardown on a Replace import (the
// same bounded teardown as VaultService.Lock, master plan §5).
const transferReplaceTimeout = 3 * time.Second

// transferContents is the decrypted `.shelve` body: the vault payload plus the
// settings snapshot (plan config-export-import §1 D2/D4).
type transferContents struct {
	Payload  json.RawMessage  `json:"payload"`
	Settings *config.Settings `json:"settings,omitempty"`
}

// TransferService exposes configuration export/import (plan
// config-export-import §3). It is the only service that reads/writes an
// arbitrary user-chosen path; the renderer passes a path, never file bytes
// (master plan A5). Every method requires an unlocked vault.
type TransferService struct {
	store   *store.Store
	vault   *vault.Vault
	engine  *sshengine.Manager
	version string
}

// NewTransferService wires the export/import service. version is recorded in
// the export header only (internal/app.Version).
func NewTransferService(st *store.Store, v *vault.Vault, engine *sshengine.Manager, version string) *TransferService {
	return &TransferService{store: st, vault: v, engine: engine, version: version}
}

// Export writes the encrypted configuration to path (0600, atomic). The export
// passphrase is independent of the master password; it is never logged,
// persisted or returned.
func (s *TransferService) Export(path, password string) error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("export: path is required")
	}
	if password == "" {
		return errors.New("export: passphrase is required")
	}

	payload, err := s.store.Encode()
	if err != nil {
		return err
	}
	settings, _ := config.Load()
	contents, err := json.Marshal(transferContents{Payload: payload, Settings: &settings})
	if err != nil {
		return err
	}
	data, err := vault.EncryptWithPassword(contents, password, s.version)
	if err != nil {
		return err
	}
	// A user-chosen path outside the config dir: atomic 0600 file write, but
	// never re-assert 0700 on (or chmod) the target directory.
	return config.WriteFileAtomicOutside(filepath.Dir(path), filepath.Base(path), data)
}

// Import decrypts, validates and applies an exported configuration. mode is
// "merge" or "replace". It is all-or-nothing: any failure leaves the store
// unchanged (merge) or aborts before applying (replace). The passphrase and
// decrypted bytes are never logged.
func (s *TransferService) Import(path, password, mode string) (ImportResultDTO, error) {
	if !s.vault.IsUnlocked() {
		return ImportResultDTO{}, vault.ErrLocked
	}
	if mode != ImportModeMerge && mode != ImportModeReplace {
		return ImportResultDTO{}, fmt.Errorf("import: unknown mode %q", mode)
	}
	if strings.TrimSpace(path) == "" {
		return ImportResultDTO{}, errors.New("import: path is required")
	}

	data, err := readFileCapped(path, maxImportFileSize)
	if err != nil {
		return ImportResultDTO{}, err
	}
	plaintext, err := vault.DecryptWithPassword(data, password)
	if err != nil {
		return ImportResultDTO{}, err
	}
	var contents transferContents
	if err := json.Unmarshal(plaintext, &contents); err != nil {
		return ImportResultDTO{}, fmt.Errorf("import: invalid configuration file: %w", err)
	}
	if len(contents.Payload) == 0 {
		return ImportResultDTO{}, errors.New("import: file contains no configuration")
	}

	if mode == ImportModeMerge {
		res, err := s.store.Merge(contents.Payload, "Imported "+time.Now().Format("2006-01-02"))
		if err != nil {
			return ImportResultDTO{}, err
		}
		return ImportResultDTO{
			Mode:               ImportModeMerge,
			Folders:            res.Folders,
			Sessions:           res.Sessions,
			Credentials:        res.Credentials,
			SavedJumpHosts:     res.SavedJumpHosts,
			RemappedReferences: res.RemappedReferences,
		}, nil
	}

	// Replace: disconnect every live session first (their IDs are about to
	// vanish), then swap the tree and settings (plan config-export-import D4/D8).
	var p model.Payload
	if err := json.Unmarshal(contents.Payload, &p); err != nil {
		return ImportResultDTO{}, fmt.Errorf("import: invalid configuration data: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), transferReplaceTimeout)
	s.engine.Shutdown(ctx)
	cancel()
	if err := s.store.Load(contents.Payload); err != nil {
		return ImportResultDTO{}, err
	}
	if err := s.store.Flush(); err != nil {
		return ImportResultDTO{}, err
	}
	if contents.Settings != nil {
		if err := contents.Settings.Save(); err != nil {
			return ImportResultDTO{}, err
		}
	}
	return ImportResultDTO{
		Mode:           ImportModeReplace,
		Folders:        len(p.Folders),
		Sessions:       len(p.Sessions),
		Credentials:    len(p.Credentials),
		SavedJumpHosts: len(p.SavedJumpHosts),
	}, nil
}

// readFileCapped reads path, rejecting files larger than max bytes (a corrupt
// or oversized file must never drive an unbounded allocation).
func readFileCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > max {
		return nil, fmt.Errorf("import: file is too large (limit %d bytes)", max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("import: file is too large (limit %d bytes)", max)
	}
	return data, nil
}
