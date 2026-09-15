package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"shelve/internal/config"
	"shelve/internal/model"
	"shelve/internal/sftp"
	"shelve/internal/store"
	"shelve/internal/vault"
)

const (
	testMasterPW  = "master-password-1"
	testExportPW  = "export-passphrase-1"
	guardPassText = "P@ssw0rd-should-never-appear"
)

// newTransferHarness builds an unlocked vault over an isolated config dir and
// returns the export/import service plus its store.
func newTransferHarness(t *testing.T) (*TransferService, *store.Store) {
	t.Helper()
	isolatedXDG(t)
	emit := &recEmitter{}
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	eng := newTestEngine(t, emit)
	sft := sftp.New(t.TempDir(), emit)
	vs := NewVaultService(v, st, eng, sft, emit)
	if err := vs.CreateVault(testMasterPW); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	return NewTransferService(st, v, eng, "0.1.0"), st
}

// seedTransferTree populates a store with a credential, a saved jump host and a
// two-session tree referencing both.
func seedTransferTree(t *testing.T, st *store.Store) {
	t.Helper()
	credID, err := st.CreateCredential(model.Credential{
		Name: "prod-admin",
		User: "admin",
		Auth: model.Auth{Type: model.AuthPassword, Password: guardPassText},
	})
	if err != nil {
		t.Fatal(err)
	}
	jumpID, err := st.CreateSavedJumpHost(model.SavedJumpHost{
		Name: "bastion", Host: "jump.example.com", Port: 22, User: "tunnel",
		Auth: model.Auth{Type: model.AuthPassword, Password: guardPassText},
	})
	if err != nil {
		t.Fatal(err)
	}
	folderID, err := st.CreateFolder("", "Prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession(folderID, model.Session{
		Name: "prod-db", Host: "db.example.com", Port: 22, User: "admin",
		Auth:         model.Auth{Type: model.AuthPassword, Password: guardPassText},
		CredentialID: credID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession("", model.Session{
		Name: "edge", Host: "edge.example.com", Port: 22, User: "ops",
		Auth:        model.Auth{Type: model.AuthPassword, Password: guardPassText},
		JumpHostRef: jumpID,
	}); err != nil {
		t.Fatal(err)
	}
}

// payloadCounts unmarshals an encoded store payload and returns its entity
// totals.
func payloadCounts(t *testing.T, st *store.Store) model.Payload {
	t.Helper()
	data, err := st.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var p model.Payload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTransferExportWritesEncryptedFile(t *testing.T) {
	svc, st := newTransferHarness(t)
	seedTransferTree(t, st)

	// Export into a user-chosen directory that is NOT the config dir and has a
	// non-0700 mode: the file must be 0600 and the directory mode untouched.
	outDir := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outDir, "shelve-config.shelve")
	if err := svc.Export(path, testExportPW); err != nil {
		t.Fatalf("Export: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("export perms = %o, want 600", info.Mode().Perm())
	}
	di, err := os.Stat(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o755 {
		t.Fatalf("export dir perms = %o, want 755 (untouched)", di.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// No plaintext secret (session/credential/jump passwords or the master
	// password) may appear anywhere in the file (master plan §8.1).
	for _, secret := range []string{guardPassText, testMasterPW} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("export file contains plaintext %q", secret)
		}
	}

	var env vault.ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("export envelope: %v", err)
	}
	if env.Format != "shelve-export" || env.V != vault.ExportVersion || env.App != "0.1.0" {
		t.Fatalf("envelope header = %+v", env)
	}
	if env.KDF.Alg != "argon2id" || env.Enc.Alg != "aes-256-gcm" {
		t.Fatalf("envelope algorithms = %+v / %+v", env.KDF, env.Enc)
	}

	// The ciphertext decrypts back to the payload + settings snapshot.
	plaintext, err := vault.DecryptWithPassword(data, testExportPW)
	if err != nil {
		t.Fatalf("DecryptWithPassword: %v", err)
	}
	var contents struct {
		Payload  json.RawMessage  `json:"payload"`
		Settings *config.Settings `json:"settings"`
	}
	if err := json.Unmarshal(plaintext, &contents); err != nil {
		t.Fatalf("contents: %v", err)
	}
	if len(contents.Payload) == 0 || contents.Settings == nil {
		t.Fatalf("contents missing payload/settings: %+v", contents)
	}
}

func TestTransferMergeRoundTrip(t *testing.T) {
	svc, st := newTransferHarness(t)
	seedTransferTree(t, st)
	before := payloadCounts(t, st)

	path := filepath.Join(t.TempDir(), "roundtrip.shelve")
	if err := svc.Export(path, testExportPW); err != nil {
		t.Fatalf("Export: %v", err)
	}
	res, err := svc.Import(path, testExportPW, ImportModeMerge)
	if err != nil {
		t.Fatalf("Import(merge): %v", err)
	}
	if res.Mode != ImportModeMerge {
		t.Fatalf("mode = %q", res.Mode)
	}
	if res.Folders != len(before.Folders) || res.Sessions != len(before.Sessions) ||
		res.Credentials != len(before.Credentials) || res.SavedJumpHosts != len(before.SavedJumpHosts) {
		t.Fatalf("merge counts = %+v, want %d/%d/%d/%d", res, len(before.Folders), len(before.Sessions), len(before.Credentials), len(before.SavedJumpHosts))
	}
	if res.RemappedReferences != 2 {
		t.Fatalf("remappedReferences = %d, want 2", res.RemappedReferences)
	}

	// The import landed under a new wrapper folder; the original data is intact.
	after := payloadCounts(t, st)
	wantFolders := len(before.Folders) + len(before.Folders) + 1 // existing + imported + wrapper
	if len(after.Folders) != wantFolders {
		t.Fatalf("folders after merge = %d, want %d", len(after.Folders), wantFolders)
	}
}

func TestTransferWrongPassphrase(t *testing.T) {
	svc, st := newTransferHarness(t)
	seedTransferTree(t, st)

	path := filepath.Join(t.TempDir(), "cfg.shelve")
	if err := svc.Export(path, testExportPW); err != nil {
		t.Fatalf("Export: %v", err)
	}
	before := payloadCounts(t, st)

	if _, err := svc.Import(path, "wrong-passphrase", ImportModeMerge); !errors.Is(err, vault.ErrExportWrongPassword) {
		t.Fatalf("wrong passphrase: %v, want ErrExportWrongPassword", err)
	}
	if after := payloadCounts(t, st); len(after.Sessions) != len(before.Sessions) || len(after.Folders) != len(before.Folders) {
		t.Fatal("store mutated by a failed import")
	}
}

func TestTransferReplaceAppliesPayloadAndSettings(t *testing.T) {
	svc, st := newTransferHarness(t)
	seedTransferTree(t, st)

	exported := config.DefaultSettings()
	exported.Theme = config.ThemeDark
	exported.AutoLockMinutes = 42
	exported.ThemeVariant = ""
	if err := exported.Save(); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	path := filepath.Join(t.TempDir(), "replace.shelve")
	if err := svc.Export(path, testExportPW); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Mutate the local state so the replace has something to overwrite.
	if _, err := st.CreateFolder("", "WillBeReplaced"); err != nil {
		t.Fatal(err)
	}
	changed := config.DefaultSettings()
	changed.Theme = config.ThemeLight
	if err := changed.Save(); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Import(path, testExportPW, ImportModeReplace)
	if err != nil {
		t.Fatalf("Import(replace): %v", err)
	}
	if res.Mode != ImportModeReplace || res.Sessions != 2 || res.Folders != 1 || res.Credentials != 1 || res.SavedJumpHosts != 1 {
		t.Fatalf("replace counts = %+v", res)
	}

	tree := st.Tree()
	names := map[string]bool{}
	var walk func(nodes []store.TreeNode)
	walk = func(nodes []store.TreeNode) {
		for _, n := range nodes {
			names[n.Name] = true
			walk(n.Children)
		}
	}
	walk(tree)
	if names["WillBeReplaced"] {
		t.Fatal("replace kept local data")
	}
	if !names["Prod"] || !names["prod-db"] || !names["edge"] {
		t.Fatalf("replace did not apply the payload: %v", names)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != config.ThemeDark || loaded.AutoLockMinutes != 42 {
		t.Fatalf("settings after replace = %+v, want dark/42", loaded)
	}
}

func TestTransferUnknownMode(t *testing.T) {
	svc, _ := newTransferHarness(t)
	if _, err := svc.Import(filepath.Join(t.TempDir(), "missing.shelve"), testExportPW, "bogus"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestTransferLockedVault(t *testing.T) {
	isolatedXDG(t)
	emit := &recEmitter{}
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	eng := newTestEngine(t, emit)
	svc := NewTransferService(st, v, eng, "0.1.0")

	path := filepath.Join(t.TempDir(), "cfg.shelve")
	if err := svc.Export(path, testExportPW); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("Export while locked: %v, want ErrLocked", err)
	}
	if _, err := svc.Import(path, testExportPW, ImportModeMerge); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("Import while locked: %v, want ErrLocked", err)
	}
}

func TestTransferExportValidation(t *testing.T) {
	svc, _ := newTransferHarness(t)
	path := filepath.Join(t.TempDir(), "cfg.shelve")
	if err := svc.Export("", testExportPW); err == nil {
		t.Fatal("empty path accepted")
	}
	if err := svc.Export(path, ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}

func TestTransferRejectsOversizedFile(t *testing.T) {
	svc, _ := newTransferHarness(t)
	path := filepath.Join(t.TempDir(), "huge.shelve")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxImportFileSize+1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(path, testExportPW, ImportModeMerge); err == nil {
		t.Fatal("oversized import accepted")
	}
}
