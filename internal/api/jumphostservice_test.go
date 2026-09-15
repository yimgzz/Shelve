package api

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

// TestJumpHostServiceCRUDThroughService covers plan P006: create/list/
// get/update/delete named saved jump hosts behind the vault-unlock gate,
// with secret-free read views (§8 assertions) and the blank-password
// merge rule.
func TestJumpHostServiceCRUDThroughService(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	js := NewJumpHostService(st, v)

	// Locked gate: before the vault exists, CRUD is refused.
	if _, err := js.List(); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("List while locked: %v", err)
	}
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}

	jid, err := js.Create(SavedJumpHostInput{
		Name: "bastion-prod", Host: "jump.example.com", Port: 22, User: "tunnel",
		AuthType: model.AuthPassword, Password: guardPassword,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = js.Create(SavedJumpHostInput{
		Name: "bastion-backup", Host: "10.0.0.2", Port: 2200, User: "tunnel",
		AuthType: model.AuthKey, KeyPath: "/home/t/.ssh/jump1",
	})
	if err != nil {
		t.Fatalf("Create key saved jump host: %v", err)
	}

	// Validation surfaces through the service.
	if _, err := js.Create(SavedJumpHostInput{
		Name: "", Host: "h.example.com", Port: 22, User: "u",
		AuthType: model.AuthPassword, Password: "p",
	}); err == nil {
		t.Fatal("invalid saved jump host accepted")
	}

	list, err := js.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Sorted by name: bastion-backup before bastion-prod.
	if list[0].Name != "bastion-backup" || list[1].Name != "bastion-prod" {
		t.Fatalf("List order = %+v", list)
	}

	// Secret-free read views (master plan §8).
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), guardPassword) {
		t.Fatalf("List JSON leaks saved jump host password: %s", raw)
	}
	if !list[1].HasPassword || list[1].KeyPath != "" || list[0].HasPassword ||
		list[0].KeyPath != "/home/t/.ssh/jump1" {
		t.Fatalf("unexpected saved jump host DTO fields: %+v", list)
	}

	got, err := js.Get(jid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "bastion-prod" || got.Host != "jump.example.com" ||
		got.Port != 22 || got.User != "tunnel" || !got.HasPassword {
		t.Fatalf("Get = %+v", got)
	}

	// Merge rule: an update that leaves the password blank (the read view
	// never carries it) keeps the stored secret.
	upd := got
	upd.Name = "bastion-prod-v2"
	if err := js.Update(SavedJumpHostInput{ID: upd.ID, Name: upd.Name, Host: upd.Host,
		Port: upd.Port, User: upd.User, AuthType: model.AuthPassword}); err != nil {
		t.Fatalf("Update with blank password must keep the stored secret: %v", err)
	}
	got2, _ := js.Get(jid)
	if !got2.HasPassword {
		t.Fatal("blank-password update dropped the stored secret")
	}
	// A fresh password only flows INTO the vault; the read view never
	// echoes it back.
	if err := js.Update(SavedJumpHostInput{ID: upd.ID, Name: upd.Name, Host: upd.Host,
		Port: upd.Port, User: upd.User, AuthType: model.AuthPassword,
		Password: "new-jump-secret"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ = js.Get(jid)
	if got2.Name != "bastion-prod-v2" || !got2.HasPassword {
		t.Fatalf("Get after update = %+v", got2)
	}
	raw2, _ := json.Marshal(got2)
	if strings.Contains(string(raw2), "new-jump-secret") {
		t.Fatalf("Get JSON leaks password: %s", raw2)
	}

	if _, err := js.Delete(jid); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Get(jid); !errors.Is(err, store.ErrSavedJumpHostNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
}

// TestJumpHostSessionResolution covers plan P006: a session created with
// a jumpHostRef gets its inline chain snapshot filled from the saved jump
// host (so it validates and stays self-contained), the read DTO exposes
// only the reference, connect-time resolution replaces the whole inline
// chain with the saved hop, and deleting the saved host soft-nulls the
// reference so the session falls back to its inline snapshot.
func TestJumpHostSessionResolution(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	js := NewJumpHostService(st, v)
	ss := NewSessionService(st, v, newTestEngine(t, &recEmitter{}), &recEmitter{})

	jid, err := js.Create(SavedJumpHostInput{
		Name: "bastion-dev", Host: "jump.dev.example.com", Port: 22, User: "tunnel",
		AuthType: model.AuthPassword, Password: "jump-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The editor sends the reference and possibly stale/empty inline rows;
	// the service replaces the chain from the saved host (single source of
	// truth) so the snapshot is valid and self-contained.
	sid, err := ss.CreateSession(SessionInput{
		Name: "prod-01", Host: "db.example.com", Port: 22,
		User: "alice", AuthType: model.AuthPassword, Password: "pw",
		JumpHosts: []JumpHostInput{ // stale inline rows must be overwritten
			{Host: "stale.example.com", Port: 22, User: "x",
				AuthType: model.AuthPassword, Password: "stale"},
		},
		JumpHostRef: jid,
	})
	if err != nil {
		t.Fatalf("CreateSession with saved jump host ref: %v", err)
	}
	dto, err := ss.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	if dto.JumpHostRef != jid {
		t.Fatalf("session DTO jumpHostRef = %q, want %q", dto.JumpHostRef, jid)
	}
	if len(dto.JumpHosts) != 1 || dto.JumpHosts[0].Host != "jump.dev.example.com" {
		t.Fatalf("inline snapshot not replaced from saved host: %+v", dto.JumpHosts)
	}
	raw, _ := json.Marshal(dto)
	if strings.Contains(string(raw), "jump-secret") {
		t.Fatalf("Session DTO leaks saved jump host password: %s", raw)
	}

	// Connect-time resolution (plan P006): the saved hop wins over any
	// inline rows while the reference is set.
	modelSess, err := st.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveSessionJumpHost(st, modelSess)
	if len(resolved.JumpHosts) != 1 ||
		resolved.JumpHosts[0].Host != "jump.dev.example.com" ||
		resolved.JumpHosts[0].Auth.Password != "jump-secret" {
		t.Fatalf("resolveSessionJumpHost = %+v", resolved.JumpHosts)
	}
	if err := resolved.Validate(); err != nil {
		t.Fatalf("resolved session invalid: %v", err)
	}

	// An update touching nothing else keeps the reference and snapshot.
	if err := ss.UpdateSession(SessionInput{
		ID: sid, FolderID: "", Name: "prod-01", Host: "db.example.com", Port: 22,
		User: "alice", AuthType: model.AuthPassword, JumpHostRef: jid,
	}); err != nil {
		t.Fatalf("UpdateSession with saved jump host ref: %v", err)
	}

	// Usage reflects the referencing session.
	if n, err := js.Usage(jid); err != nil || n != 1 {
		t.Fatalf("Usage = %d, %v; want 1, nil", n, err)
	}

	// Delete saved host → reference nulled, inline snapshot survives and
	// the session still resolves standalone.
	affected, err := js.Delete(jid)
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
	if after.JumpHostRef != "" {
		t.Fatalf("jumpHostRef = %q after delete, want cleared", after.JumpHostRef)
	}
	if len(after.JumpHosts) != 1 || after.JumpHosts[0].Host != "jump.dev.example.com" {
		t.Fatalf("inline snapshot lost: %+v", after.JumpHosts)
	}
	if err := after.Validate(); err != nil {
		t.Fatalf("snapshot session invalid: %v", err)
	}
}

// TestResolveSessionJumpHostDanglingFallback covers the defensive path:
// a session whose reference dangles (externally-edited payload) resolves
// to its inline snapshot untouched.
func TestResolveSessionJumpHostDanglingFallback(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	sess := model.Session{
		Name: "x", Host: "h.example.com", Port: 22, User: "inline-user",
		Auth: model.Auth{Type: model.AuthPassword, Password: "inline-pw"},
		JumpHosts: []model.JumpHost{
			{Host: "j.example.com", Port: 22, User: "t",
				Auth: model.Auth{Type: model.AuthPassword, Password: "j-pw"}},
		},
		JumpHostRef: "ghost",
	}
	resolved := resolveSessionJumpHost(st, sess)
	if len(resolved.JumpHosts) != 1 || resolved.JumpHosts[0].Host != "j.example.com" ||
		resolved.JumpHosts[0].Auth.Password != "j-pw" || resolved.JumpHostRef != "ghost" {
		t.Fatalf("dangling reference altered the session: %+v", resolved)
	}
}
