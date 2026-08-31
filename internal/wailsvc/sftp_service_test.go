package wailsvc

import (
	"errors"
	"testing"

	"shelve/internal/sftp"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// TestSftpServiceGating verifies every SftpService method enforces the
// vault-unlock gate (vault.ErrLocked when locked) and that, once unlocked, the
// browse/transfer methods reach the manager while the dialog-free fallback and
// idempotent cancel behave as documented. No live SSH connection needed.
func TestSftpServiceGating(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	emit := &recEmitter{}
	eng := newTestEngine(t, emit)
	mgr := sftp.New(t.TempDir(), emit) // no provider attached
	vs := NewVaultService(v, st, eng, mgr, emit)
	svc := NewSftpService(v, mgr)

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
	if _, err := svc.OpenRemoteFile("t", "/x"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("OpenRemoteFile while locked = %v, want ErrLocked", err)
	}
	opErrs := []error{
		svc.Upload("t", nil, "/"),
		svc.EditRemoteText("t", "/x"),
		svc.CancelEdit("t"),
	}
	for i, err := range opErrs {
		if !errors.Is(err, vault.ErrLocked) {
			t.Fatalf("op[%d] while locked = %v, want ErrLocked", i, err)
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

	// Transfers need a provider → ErrNoProvider (no connection to stream).
	if err := svc.Upload("t", nil, "/"); !errors.Is(err, sftp.ErrNoProvider) {
		t.Fatalf("Upload = %v, want ErrNoProvider", err)
	}
	if _, err := svc.Download("t", "/x"); !errors.Is(err, sftp.ErrNoProvider) {
		t.Fatalf("Download = %v, want ErrNoProvider", err)
	}
	if _, err := svc.DownloadThenSave("t", "/x"); !errors.Is(err, sftp.ErrNoProvider) {
		t.Fatalf("DownloadThenSave = %v, want ErrNoProvider", err)
	}
	if err := svc.EditRemoteText("t", "/x"); !errors.Is(err, sftp.ErrNoProvider) {
		t.Fatalf("EditRemoteText = %v, want ErrNoProvider", err)
	}
	if _, err := svc.OpenRemoteFile("t", "/x"); !errors.Is(err, sftp.ErrNoProvider) {
		t.Fatalf("OpenRemoteFile = %v, want ErrNoProvider", err)
	}

	// Dialog-free fallback: the picker is unsupported in this build.
	if _, err := svc.PickLocalFiles("t", true); !errors.Is(err, sftp.ErrSftpDialogUnsupported) {
		t.Fatalf("PickLocalFiles = %v, want ErrSftpDialogUnsupported", err)
	}
	// CancelEdit on a tab with nothing editing is an idempotent no-op.
	if err := svc.CancelEdit("t"); err != nil {
		t.Fatalf("CancelEdit = %v, want nil", err)
	}
}
