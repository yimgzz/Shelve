package wailsvc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dummy-ssh-manager/internal/config"
	"dummy-ssh-manager/internal/model"
	"dummy-ssh-manager/internal/sshengine"
	"dummy-ssh-manager/internal/sshx/knownhosts"
	"dummy-ssh-manager/internal/store"
	"dummy-ssh-manager/internal/vault"
)

// guardPassword is a distinguishable secret that must never appear in
// any serialized DTO, settings sample or other plaintext surface
// (master plan §8.1 secret-leak guard).
const guardPassword = "G4rd-P@ssw0rd-xyz"

type recEmitter struct {
	mu     sync.Mutex
	events []string
	states []bool
}

func (r *recEmitter) Emit(event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if p, ok := payload.(VaultStatePayload); ok {
		r.states = append(r.states, p.Unlocked)
	}
}

func (r *recEmitter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recEmitter) vaultStateChanges() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.states...)
}

// isolatedXDG points the config dir at a fresh temp dir.
func isolatedXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// newTestEngine builds a throwaway engine over an isolated known_hosts
// file.
func newTestEngine(t *testing.T, emit Emitter) *sshengine.Manager {
	t.Helper()
	kh, err := knownhosts.New(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	return sshengine.New(emit, kh)
}

func TestVaultServiceFullLifecycle(t *testing.T) {
	isolatedXDG(t)
	emit := &recEmitter{}

	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	eng := newTestEngine(t, emit)
	vs := NewVaultService(v, st, eng, emit)
	ss := NewSessionService(st, v, eng, emit)

	// State machine: no file → create.
	dto, err := vs.Status()
	if err != nil || dto.State != "create" || dto.Unlocked {
		t.Fatalf("initial status = %+v, %v; want create", dto, err)
	}

	// Tree ops are gated while locked.
	if _, err := ss.Tree(); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("Tree while locked: %v", err)
	}
	if _, err := ss.CreateFolder("", "F"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("CreateFolder while locked: %v", err)
	}

	// First-run create.
	if err := vs.CreateVault("correct-horse-battery"); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	dto, _ = vs.Status()
	if dto.State != "unlocked" || !dto.Unlocked {
		t.Fatalf("status after create = %+v", dto)
	}
	if got := emit.vaultStateChanges(); len(got) != 1 || !got[0] {
		t.Fatalf("state events after create = %v, want [true]", got)
	}

	// Build a small tree with a password-auth session.
	fid, err := ss.CreateFolder("", "Production")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	sid, err := ss.CreateSession(SessionInput{
		FolderID: fid,
		Name:     "prod-db-01",
		Host:     "db.example.com",
		Port:     22,
		User:     "alice",
		AuthType: model.AuthPassword,
		Password: guardPassword,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Tree reflects it (secret-free DTO).
	tree, err := ss.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || tree[0].Kind != store.KindFolder || tree[0].Name != "Production" {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	raw, _ := json.Marshal(tree)
	if strings.Contains(string(raw), guardPassword) {
		t.Fatalf("tree JSON leaks password: %s", raw)
	}

	// Lock: flush + zeroize + ONE event; second Lock is a silent no-op.
	if err := vs.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if got := emit.vaultStateChanges(); len(got) != 2 || got[1] != false {
		t.Fatalf("state events after lock = %v, want [true false]", got)
	}
	before := emit.count()
	if err := vs.Lock(); err != nil {
		t.Fatalf("second Lock must be a no-op, got %v", err)
	}
	if emit.count() != before {
		t.Fatal("second Lock emitted an event")
	}
	dto, _ = vs.Status()
	if dto.State != "locked" || dto.Unlocked {
		t.Fatalf("status after lock = %+v", dto)
	}

	// A fresh process: wrong password fails typed, right one unlocks and
	// the tree survives.
	v2 := vault.New()
	if _, err := v2.Probe(config.File(config.VaultFileName)); err != nil {
		t.Fatal(err)
	}
	st2 := store.New(func(p []byte) error { return v2.Save(p) })
	eng2 := newTestEngine(t, emit)
	vs2 := NewVaultService(v2, st2, eng2, emit)
	ss2 := NewSessionService(st2, v2, eng2, emit)

	if err := vs2.Unlock("wrong-password"); !errors.Is(err, vault.ErrWrongPassword) {
		t.Fatalf("wrong pw: %v", err)
	}
	if err := vs2.Unlock("correct-horse-battery"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got := emit.vaultStateChanges(); len(got) != 3 || got[2] != true {
		t.Fatalf("state events after unlock = %v, want [true false true]", got)
	}
	tree2, err := ss2.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree2) != 1 || tree2[0].ID != fid {
		t.Fatalf("tree after reload: %+v", tree2)
	}
	if len(tree2[0].Children) != 1 || tree2[0].Children[0].ID != sid {
		t.Fatalf("session after reload: %+v", tree2[0].Children)
	}
	raw2, _ := json.Marshal(tree2)
	if strings.Contains(string(raw2), guardPassword) {
		t.Fatalf("reloaded tree JSON leaks password: %s", raw2)
	}
}

func TestSessionServiceCRUDThroughService(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc", []byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	ss := NewSessionService(st, v, newTestEngine(t, &recEmitter{}), &recEmitter{})

	f1, err := ss.CreateFolder("", "F1")
	if err != nil {
		t.Fatal(err)
	}
	f2, err := ss.CreateFolder(f1, "F1a")
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.RenameFolder(f1, "F1x"); err != nil {
		t.Fatalf("RenameFolder: %v", err)
	}

	sid, err := ss.CreateSession(SessionInput{
		FolderID: f2, Name: "s", Host: "h.example.com", Port: 22, User: "u",
		AuthType: model.AuthKey, KeyPath: "/home/u/.ssh/id_ed25519",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Validation errors surface through the service.
	badIn := SessionInput{FolderID: f2, Name: "", Host: "bad host", Port: 0,
		User: "", AuthType: model.AuthPassword}
	if _, err := ss.CreateSession(badIn); err == nil {
		t.Fatal("invalid session accepted by service")
	}

	if err := ss.MoveNode(sid, f1, 0); err != nil {
		t.Fatalf("MoveNode: %v", err)
	}
	if _, err := ss.DuplicateSession(sid); err != nil {
		t.Fatalf("DuplicateSession: %v", err)
	}
	n, err := ss.DeleteNode(f1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // F1 + F1a + original session + copy
		t.Fatalf("DeleteNode count = %d, want 4", n)
	}

	upd := SessionInput{
		FolderID: "", Name: "x", Host: "x.example.com", Port: 22, User: "x",
		AuthType: model.AuthPassword, Password: "pp",
	}
	if err := ss.UpdateSession(upd); err == nil {
		t.Fatal("UpdateSession without ID accepted")
	}
}

// TestSessionServiceSearch covers the live flat-search binding (master
// plan §2 A9): Name/Host/User case-insensitive substring with folder path.
func TestSessionServiceSearch(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	ss := NewSessionService(st, v, newTestEngine(t, &recEmitter{}), &recEmitter{})

	fid, err := ss.CreateFolder("", "Production")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []SessionInput{
		{Name: "db-prod-01", Host: "db.example.com", Port: 22, User: "alice",
			AuthType: model.AuthKey, KeyPath: "/home/a/.ssh/id_ed25519"},
		{Name: "db-staging", Host: "10.1.2.3", Port: 22, User: "bob",
			AuthType: model.AuthPassword, Password: "pw"},
		{Name: "web", Host: "host.example.com", Port: 22, User: "carol",
			AuthType: model.AuthKey, KeyPath: "/home/c/.ssh/id_ed25519"},
	} {
		in := s
		in.FolderID = fid
		if _, err := ss.CreateSession(in); err != nil {
			t.Fatal(err)
		}
	}

	// Matches Name ("DB-") and Host ("db.example") — case-insensitive.
	res, err := ss.Search("Db")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("Search(Db) = %d results, want 2: %+v", len(res), res)
	}
	for _, r := range res {
		if r.FolderPath != "Production" {
			t.Fatalf("folder path = %q, want Production", r.FolderPath)
		}
	}
	if res[0].Name != "db-prod-01" || res[0].Host != "db.example.com" || res[0].User != "alice" {
		t.Fatalf("unexpected result fields: %+v", res[0])
	}

	// User match, case-insensitive (A9 scans Name, Host, User).
	byUser, err := ss.Search("ALICE")
	if err != nil {
		t.Fatal(err)
	}
	if len(byUser) != 1 || byUser[0].Name != "db-prod-01" {
		t.Fatalf("Search(ALICE) = %+v, want db-prod-01 only", byUser)
	}

	no, err := ss.Search("zzz-no-match")
	if err != nil {
		t.Fatal(err)
	}
	if len(no) != 0 {
		t.Fatalf("Search(no-match) = %+v, want none", no)
	}
	empty, _ := ss.Search("")
	if len(empty) != 0 {
		t.Fatalf("Search(empty) = %+v, want none", empty)
	}
}

// TestSessionServiceSessionRead proves the Session() read binding returns
// a secret-free DTO (used to prefill the session editor, Phase 4b task 3).
func TestSessionServiceSessionRead(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	ss := NewSessionService(st, v, newTestEngine(t, &recEmitter{}), &recEmitter{})

	sid, err := ss.CreateSession(SessionInput{
		Name: "db", Host: "db.example.com", Port: 2222, User: "alice",
		AuthType: model.AuthKey, KeyPath: "/home/alice/.ssh/id_ed25519",
		ExtraArgs: "-L 8080:localhost:80",
	})
	if err != nil {
		t.Fatal(err)
	}
	dto, err := ss.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	if dto.ID != sid || dto.Name != "db" || dto.Host != "db.example.com" ||
		dto.Port != 2222 || dto.User != "alice" || dto.AuthType != model.AuthKey ||
		dto.KeyPath != "/home/alice/.ssh/id_ed25519" || dto.HasPassword {
		t.Fatalf("Session() = %+v", dto)
	}
}

// TestSessionServiceUpdateSessionPasswordMerge covers the Phase 4b task 5
// merge rule: an empty incoming password on a password-auth update keeps
// the stored credential, for the session and each jump host; switching to
// key auth with a blank password does not resurrect the old one.
func TestSessionServiceUpdateSessionPasswordMerge(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	if err := v.Create(config.File(config.VaultFileName), "correct-pw-abc",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	ss := NewSessionService(st, v, newTestEngine(t, &recEmitter{}), &recEmitter{})

	const secret = "keep-me-secret-42"
	sid, err := ss.CreateSession(SessionInput{
		Name: "pw-sess", Host: "h.example.com", Port: 22, User: "u",
		AuthType: model.AuthPassword, Password: secret,
		JumpHosts: []JumpHostInput{
			{Host: "j.example.com", Port: 22, User: "t",
				AuthType: model.AuthPassword, Password: "jump-keep-me"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Blank password on a password-auth update → preserved (session + jump).
	upd := SessionInput{
		ID: sid, Name: "pw-sess", Host: "h2.example.com", Port: 22, User: "u",
		AuthType: model.AuthPassword, // Password omitted → ""
		JumpHosts: []JumpHostInput{
			{Host: "j2.example.com", Port: 22, User: "t",
				AuthType: model.AuthPassword}, // Password omitted → ""
		},
	}
	if err := ss.UpdateSession(upd); err != nil {
		t.Fatalf("UpdateSession blank pw: %v", err)
	}
	dto, err := ss.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !dto.HasPassword {
		t.Fatal("session password was not preserved across blank update")
	}
	if len(dto.JumpHosts) != 1 || !dto.JumpHosts[0].HasPassword {
		t.Fatalf("jump-host password was not preserved: %+v", dto.JumpHosts)
	}

	// Switching password→key with a blank password must NOT resurrect the
	// stored credential (would break the auth XOR invariant).
	keyUpd := SessionInput{
		ID: sid, Name: "pw-sess", Host: "h3.example.com", Port: 22, User: "u",
		AuthType: model.AuthKey, KeyPath: "/home/u/.ssh/id_ed25519",
		JumpHosts: []JumpHostInput{
			{Host: "j3.example.com", Port: 22, User: "t",
				AuthType: model.AuthKey, KeyPath: "/home/u/.ssh/jump_key"},
		},
	}
	if err := ss.UpdateSession(keyUpd); err != nil {
		t.Fatalf("UpdateSession switch to key: %v", err)
	}
	dto, err = ss.Session(sid)
	if err != nil {
		t.Fatal(err)
	}
	if dto.HasPassword || dto.AuthType != model.AuthKey {
		t.Fatalf("password resurrected after switch to key: %+v", dto)
	}
	if len(dto.JumpHosts) != 1 || dto.JumpHosts[0].HasPassword ||
		dto.JumpHosts[0].AuthType != model.AuthKey {
		t.Fatalf("jump-host password resurrected after switch to key: %+v", dto.JumpHosts)
	}
}

// TestConnection is bound to the engine (phase 3d): a locked vault
// refuses attempts before any network traffic.
func TestSessionServiceTestConnectionGating(t *testing.T) {
	emit := &recEmitter{}
	ss := NewSessionService(store.New(nil), vault.New(), newTestEngine(t, emit), emit)
	err := ss.TestConnection(SessionInput{
		Name: "x", Host: "x.example.com", Port: 22, User: "x",
		AuthType: model.AuthPassword, Password: "pp",
	})
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("TestConnection while locked: %v, want ErrLocked", err)
	}
	if n := emit.count(); n != 0 {
		t.Fatalf("events emitted for a locked-attempt: %d, want 0", n)
	}
}

// TestSessionServiceTestConnectionDialChain proves the phase 3d wiring:
// the placeholder is gone and TestConnection runs the real engine dial,
// surfacing a hop-attributed failure.
func TestSessionServiceTestConnectionDialChain(t *testing.T) {
	isolatedXDG(t)
	v := vault.New()
	st := store.New(func(p []byte) error { return v.Save(p) })
	emit := &recEmitter{}
	eng := newTestEngine(t, emit)
	ss := NewSessionService(st, v, eng, emit)
	if err := v.Create(config.File(config.VaultFileName), "master-pw-01",
		[]byte(`{"root":[],"folders":[],"sessions":[]}`)); err != nil {
		t.Fatal(err)
	}
	err := ss.TestConnection(SessionInput{
		Name: "x", Host: "127.0.0.1", Port: 1, User: "u",
		AuthType: model.AuthPassword, Password: "pp",
	})
	if err == nil {
		t.Fatal("TestConnection to a refused port: want error")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Fatalf("TestConnection error = %q, want target attribution", err)
	}
}

// Real parser errors must surface verbatim through the service: the
// session editor binds to ValidateExtraArgs for inline validation
// (Phase 3a, master plan §6).
func TestSessionServiceValidateExtraArgs(t *testing.T) {
	ss := NewSessionService(store.New(nil), vault.New(), newTestEngine(t, &recEmitter{}), &recEmitter{})

	valid := []string{
		"",
		"   ",
		"-L 8080:localhost:80",
		"-D 1080",
		"-o ServerAliveInterval=30 -o StrictHostKeyChecking=no",
		"ProxyJump=bob@jump:2222",
	}
	for _, spec := range valid {
		if err := ss.ValidateExtraArgs(spec); err != nil {
			t.Fatalf("ValidateExtraArgs(%q) = %v, want nil", spec, err)
		}
	}

	invalid := []struct{ spec, wantErr string }{
		{"-A", "unsupported flag -A"},
		{"-R 8080:db:5432", "unsupported flag -R"},
		{"-L 8080:db", `invalid -L spec: "8080:db" (expected [bind:]localPort:dstHost:dstPort)`},
		{"-o Compression=yes", `unsupported -o key "Compression"`},
		{"-L '8080:db:5432", "unbalanced quote"},
	}
	for _, bad := range invalid {
		err := ss.ValidateExtraArgs(bad.spec)
		if err == nil || err.Error() != bad.wantErr {
			t.Fatalf("ValidateExtraArgs(%q) = %v, want %q", bad.spec, err, bad.wantErr)
		}
	}
}

func TestAppServiceSettings(t *testing.T) {
	isolatedXDG(t)
	a := NewAppService("9.9.9")
	if a.GetVersion() != "9.9.9" {
		t.Fatalf("version = %q", a.GetVersion())
	}

	s, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s != config.DefaultSettings() {
		t.Fatalf("defaults: %+v", s)
	}
	// Save normalizes unknown values.
	s.Theme = "neon"
	s.Terminal.FontSize = 0
	s.SftpBrowserEnabled = true
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != config.ThemeSystem || got.Terminal.FontSize != 13 || !got.SftpBrowserEnabled {
		t.Fatalf("round trip: %+v", got)
	}
	// File exists with 0600.
	fi, err := os.Stat(config.File(config.SettingsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings perms = %o", fi.Mode().Perm())
	}
}

// Master plan §8.1 secret-leak guard: a sample settings.json and the
// serialized Tree()/SessionDTO output must never contain a password.
func TestNoSecretLeakInAnyPlaintextSurface(t *testing.T) {
	sess := SessionInput{
		FolderID: "", Name: "leak-check", Host: "h.example.com", Port: 22, User: "u",
		AuthType: model.AuthPassword, Password: guardPassword,
		JumpHosts: []JumpHostInput{
			{Host: "j.example.com", Port: 22, User: "t",
				AuthType: model.AuthPassword, Password: guardPassword},
		},
	}
	m := sess.toModel()

	t.Run("SessionDTO", func(t *testing.T) {
		raw, err := json.Marshal(ToSessionDTO(m))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), guardPassword) {
			t.Fatalf("SessionDTO JSON leaks password: %s", raw)
		}
		dto := ToSessionDTO(m)
		if !dto.HasPassword || len(dto.JumpHosts) != 1 || !dto.JumpHosts[0].HasPassword {
			t.Fatalf("HasPassword flags wrong: %+v", dto)
		}
	})

	t.Run("TreeNode", func(t *testing.T) {
		st := store.New(nil)
		payload, err := json.Marshal(model.Payload{
			Root:     []string{"f1", "s1"},
			Folders:  []model.Folder{{ID: "f1", Name: "F", Children: []string{}}},
			Sessions: []model.Session{sess.toModel()},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Load(payload); err != nil {
			t.Fatal(err)
		}
		tree := st.Tree()
		if n := countTreeNodes(tree); n != 2 {
			t.Fatalf("tree nodes = %d", n)
		}
		raw, err := json.Marshal(toNodeDTOs(tree))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), guardPassword) {
			t.Fatalf("tree JSON leaks password: %s", raw)
		}
	})

	t.Run("settings sample", func(t *testing.T) {
		settings := config.DefaultSettings()
		settings.Theme = config.ThemeDark
		settings.TextEditorCommand = "/usr/bin/vim"
		raw, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), guardPassword) {
			t.Fatalf("settings JSON leaks password: %s", raw)
		}
	})
}

func countTreeNodes(nodes []store.TreeNode) int {
	n := 0
	var walk func(ns []store.TreeNode)
	walk = func(ns []store.TreeNode) {
		for _, node := range ns {
			n++
			walk(node.Children)
		}
	}
	walk(nodes)
	return n
}

// The vault file on disk must not carry plaintext secrets either.
func TestVaultFileOnDiskLeaksNothing(t *testing.T) {
	isolatedXDG(t)
	path := config.File(config.VaultFileName)
	v := vault.New()
	payload := []byte(`{"root":[],"folders":[],"sessions":[{"id":"s1","folderId":"","name":"leak-check","host":"h","port":22,"user":"u","auth":{"type":0,"password":"` + guardPassword + `"}}]}`)
	if err := v.Create(path, "master-pw-01", payload); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{guardPassword, "master-pw-01", "leak-check"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("vault.json leaks %q", secret)
		}
	}
	// Sanity: the dir was created 0700 by the atomic writer.
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("config dir perms = %o", fi.Mode().Perm())
	}
}
