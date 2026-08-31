package wailsvc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"shelve/internal/config"
	"shelve/internal/model"
	"shelve/internal/store"
	"shelve/internal/vault"
)

// TestCredentialServiceCRUDThroughService covers plan P003 §4.3: create/
// list/get/update/delete named credentials behind the vault-unlock gate,
// with secret-free read views (§8 assertions).
func TestCredentialServiceCRUDThroughService(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	cs := NewCredentialService(st, v)

	// Locked gate: before the vault exists, CRUD is refused.
	if _, err := cs.List(); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("List while locked: %v", err)
	}
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}

	cid, err := cs.Create(CredentialInput{
		Name: "prod-admin", User: "admin",
		AuthType: model.AuthPassword, Password: guardPassword,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = cs.Create(CredentialInput{
		Name: "backup-key", User: "backup",
		AuthType: model.AuthKey, KeyPath: "/home/b/.ssh/id_ed25519",
	})
	if err != nil {
		t.Fatalf("Create key credential: %v", err)
	}

	// Validation surfaces through the service.
	if _, err := cs.Create(CredentialInput{Name: "", User: "u", AuthType: model.AuthPassword, Password: "p"}); err == nil {
		t.Fatal("invalid credential accepted")
	}

	list, err := cs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Sorted by name: backup-key before prod-admin.
	if list[0].Name != "backup-key" || list[1].Name != "prod-admin" {
		t.Fatalf("List order = %+v", list)
	}

	// Secret-free read views (master plan §8).
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), guardPassword) {
		t.Fatalf("List JSON leaks credential password: %s", raw)
	}
	if list[1].HasPassword == false || list[1].KeyPath != "" || list[0].HasPassword {
		t.Fatalf("unexpected credential DTO fields: %+v", list)
	}

	got, err := cs.Get(cid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "prod-admin" || got.User != "admin" || !got.HasPassword {
		t.Fatalf("Get = %+v", got)
	}

	// Merge rule: an update that leaves the password blank (the read view
	// never carries it) keeps the stored secret.
	upd := got
	upd.Name = "prod-admin-v2"
	if err := cs.Update(CredentialInput{ID: upd.ID, Name: upd.Name, User: upd.User,
		AuthType: model.AuthPassword}); err != nil {
		t.Fatalf("Update with blank password must keep the stored secret: %v", err)
	}
	got2, _ := cs.Get(cid)
	if !got2.HasPassword {
		t.Fatal("blank-password update dropped the stored credential")
	}
	// A fresh password only flows INTO the vault; the read view never
	// echoes it back.
	if err := cs.Update(CredentialInput{ID: upd.ID, Name: upd.Name, User: upd.User,
		AuthType: model.AuthPassword, Password: "new-secret"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ = cs.Get(cid)
	if got2.Name != "prod-admin-v2" || !got2.HasPassword {
		t.Fatalf("Get after update = %+v", got2)
	}
	raw2, _ := json.Marshal(got2)
	if strings.Contains(string(raw2), "new-secret") {
		t.Fatalf("Get JSON leaks password: %s", raw2)
	}

	if _, err := cs.Delete(cid); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get(cid); !errors.Is(err, store.ErrCredentialNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
}

// TestCredentialSessionResolution covers plan P003 §5: a session created
// with a credentialId gets its inline snapshot filled from the credential
// (so it validates and stays self-contained), the read DTO exposes only
// the reference, connect-time resolution yields the credential's
// User+Auth, and deleting the credential soft-nulls the reference so the
// session falls back to its inline snapshot.
func TestCredentialSessionResolution(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	cs := NewCredentialService(st, v)
	ss := NewSessionService(st, v, newTestEngine(t, &recEmitter{}), &recEmitter{})

	cid, err := cs.Create(CredentialInput{
		Name: "ops-user", User: "ops",
		AuthType: model.AuthPassword, Password: "ops-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The editor sends the reference but can never send the credential's
	// password; the service fills the blank auth from the credential.
	sid, err := ss.CreateSession(SessionInput{
		Name: "prod-01", Host: "db.example.com", Port: 22,
		User: "", AuthType: model.AuthPassword, CredentialID: cid,
	})
	if err != nil {
		t.Fatalf("CreateSession with credential ref: %v", err)
	}
	dto, err := ss.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	if dto.CredentialID != cid || dto.User != "ops" || !dto.HasPassword {
		t.Fatalf("session DTO = %+v", dto)
	}
	raw, _ := json.Marshal(dto)
	if strings.Contains(string(raw), "ops-secret") {
		t.Fatalf("Session DTO leaks credential password: %s", raw)
	}

	// Connect-time resolution (plan P003 §4.1): the credential wins over
	// any inline values while the reference is set.
	modelSess, err := st.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveSessionCredential(st, modelSess)
	if resolved.User != "ops" || resolved.Auth.Password != "ops-secret" {
		t.Fatalf("resolveSessionCredential = user %q auth %+v", resolved.User, resolved.Auth)
	}
	if err := resolved.Validate(); err != nil {
		t.Fatalf("resolved session invalid: %v", err)
	}

	// An update touching nothing else keeps the credential and snapshot.
	if err := ss.UpdateSession(SessionInput{
		ID: sid, FolderID: "", Name: "prod-01", Host: "db.example.com", Port: 22,
		User: "ops", AuthType: model.AuthPassword, CredentialID: cid,
	}); err != nil {
		t.Fatalf("UpdateSession with credential ref: %v", err)
	}

	// Delete credential → reference nulled, inline snapshot survives and
	// the session still resolves standalone.
	affected, err := cs.Delete(cid)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("affected sessions = %d, want 1", affected)
	}
	after, err := st.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	if after.CredentialID != "" {
		t.Fatalf("credentialId = %q after delete, want cleared", after.CredentialID)
	}
	if after.Auth.Password != "ops-secret" || after.User != "ops" {
		t.Fatalf("inline snapshot lost: %+v", after)
	}
	if err := after.Validate(); err != nil {
		t.Fatalf("snapshot session invalid: %v", err)
	}
}

// TestResolveSessionCredentialDanglingFallback covers the defensive path:
// a session whose reference dangles (externally-edited payload) resolves
// to its inline snapshot untouched.
func TestResolveSessionCredentialDanglingFallback(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	sess := model.Session{
		Name: "x", Host: "h.example.com", Port: 22, User: "inline-user",
		Auth:         model.Auth{Type: model.AuthPassword, Password: "inline-pw"},
		CredentialID: "ghost",
	}
	resolved := resolveSessionCredential(st, sess)
	if resolved.User != "inline-user" || resolved.Auth.Password != "inline-pw" ||
		resolved.CredentialID != "ghost" {
		t.Fatalf("dangling reference altered the session: %+v", resolved)
	}
}
