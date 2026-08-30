package wailsvc

import (
	"errors"
	"path/filepath"
	"testing"

	"dummy-ssh-manager/internal/sftp"
	"dummy-ssh-manager/internal/store"
	"dummy-ssh-manager/internal/vault"
)

// TestSftpServiceGatingAndStubs verifies every SftpService method enforces
// the vault-unlock gate (vault.ErrLocked when locked), the browse methods
// reach the manager (ErrNoProvider when unattached), and the Phase 5b stubs
// return ErrSftpNotImplemented once unlocked. No live SSH connection needed.
func TestSftpServiceGatingAndStubs(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	emit := &recEmitter{}
	eng := newTestEngine(t, emit)
	vs := NewVaultService(v, st, eng, emit)

	tmpDir := filepath.Join(t.TempDir(), "tmp")
	mgr := sftp.New(tmpDir, emit) // no provider attached
	svc := NewSftpService(v, mgr, tmpDir)

	// While locked every method is gated.
	if svc.IsActive("t") {
		t.Fatal("IsActive must be false while vault locked")
	}
	if _, err := svc.List("t", "/"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("List while locked = %v, want ErrLocked", err)
	}
	browseErrs := []error{
		svc.Mkdir("t", "/x"),
		svc.Rename("t", "/a", "/b"),
		svc.Remove("t", "/x"),
	}
	for i, err := range browseErrs {
		if !errors.Is(err, vault.ErrLocked) {
			t.Fatalf("browse[%d] while locked = %v, want ErrLocked", i, err)
		}
	}
	if _, err := svc.Download("t", "/x"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("Download while locked = %v, want ErrLocked", err)
	}
	if _, err := svc.DownloadThenSave("t", "/x"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("DownloadThenSave while locked = %v, want ErrLocked", err)
	}
	if _, err := svc.PickLocalFiles("t", false); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("PickLocalFiles while locked = %v, want ErrLocked", err)
	}
	stubErrs := []error{
		svc.Upload("t", nil, "/"),
		svc.EditRemoteText("t", "/x"),
		svc.CancelEdit("t"),
	}
	for i, err := range stubErrs {
		if !errors.Is(err, vault.ErrLocked) {
			t.Fatalf("stub[%d] while locked = %v, want ErrLocked", i, err)
		}
	}

	// Unlock, then re-check the surface.
	if err := vs.CreateVault("correct-horse-battery"); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}

	// Browse with no provider attached surfaces the manager's typed error.
	if _, err := svc.List("t", "/"); !errors.Is(err, sftp.ErrNoProvider) {
		t.Fatalf("List unlocked (no provider) = %v, want ErrNoProvider", err)
	}

	// Stubs now return the typed not-implemented error.
	if _, err := svc.PickLocalFiles("t", true); !errors.Is(err, ErrSftpNotImplemented) {
		t.Fatalf("PickLocalFiles = %v, want ErrSftpNotImplemented", err)
	}
	if err := svc.Upload("t", nil, "/"); !errors.Is(err, ErrSftpNotImplemented) {
		t.Fatalf("Upload = %v, want ErrSftpNotImplemented", err)
	}
	if _, err := svc.Download("t", "/x"); !errors.Is(err, ErrSftpNotImplemented) {
		t.Fatalf("Download = %v, want ErrSftpNotImplemented", err)
	}
	if _, err := svc.DownloadThenSave("t", "/x"); !errors.Is(err, ErrSftpNotImplemented) {
		t.Fatalf("DownloadThenSave = %v, want ErrSftpNotImplemented", err)
	}
	if err := svc.EditRemoteText("t", "/x"); !errors.Is(err, ErrSftpNotImplemented) {
		t.Fatalf("EditRemoteText = %v, want ErrSftpNotImplemented", err)
	}
	if err := svc.CancelEdit("t"); !errors.Is(err, ErrSftpNotImplemented) {
		t.Fatalf("CancelEdit = %v, want ErrSftpNotImplemented", err)
	}
}
