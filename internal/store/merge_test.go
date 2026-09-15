package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"shelve/internal/model"
)

// marshalPayload serializes a model.Payload for Merge (the shape an app
// export's `payload` field carries).
func marshalPayload(t *testing.T, p model.Payload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

// flattenNames maps every tree node name to its ID (test helper).
func flattenNames(nodes []TreeNode) map[string]string {
	out := map[string]string{}
	var walk func([]TreeNode)
	walk = func(ns []TreeNode) {
		for _, n := range ns {
			out[n.Name] = n.ID
			walk(n.Children)
		}
	}
	walk(nodes)
	return out
}

func pwSession(name string) model.Session {
	return model.Session{
		Name: name,
		Host: "host.example.com",
		Port: 22,
		User: "alice",
		Auth: model.Auth{Type: model.AuthPassword, Password: "pw"},
	}
}

// mergeFixture is a small nested import: one credential, one saved jump host,
// two nested folders and three sessions (one root-level), with both reference
// kinds exercised.
func mergeFixture() model.Payload {
	sess1 := pwSession("one")
	sess2 := pwSession("two")
	sessRoot := pwSession("root-sess")
	sess1.ID, sess1.FolderID, sess1.CredentialID = "sess-1", "fold-a", "cred-1"
	sess2.ID, sess2.FolderID, sess2.JumpHostRef = "sess-2", "fold-b", "jump-1"
	sessRoot.ID, sessRoot.FolderID = "sess-root", ""

	return model.Payload{
		Root: []string{"fold-a", "sess-root"},
		Folders: []model.Folder{
			{ID: "fold-a", ParentID: "", Name: "Alpha", Children: []string{"fold-b", "sess-1"}},
			{ID: "fold-b", ParentID: "fold-a", Name: "Beta", Children: []string{"sess-2"}},
		},
		Sessions: []model.Session{sess1, sess2, sessRoot},
		Credentials: []model.Credential{
			{ID: "cred-1", Name: "C1", User: "cu", Auth: model.Auth{Type: model.AuthPassword, Password: "cpw"}},
		},
		SavedJumpHosts: []model.SavedJumpHost{
			{ID: "jump-1", Name: "J1", Host: "jump.example.com", Port: 22, User: "ju", Auth: model.Auth{Type: model.AuthPassword, Password: "jpw"}},
		},
	}
}

// TestMergeRemapsIDsAndReferences covers plan config-export-import §2: fresh
// ULIDs for every entity, references rewritten, nested structure and sibling
// order preserved, imported roots under the new folder, existing data
// untouched.
func TestMergeRemapsIDsAndReferences(t *testing.T) {
	s := New(nil)
	existingFolder, err := s.CreateFolder("", "Existing")
	if err != nil {
		t.Fatal(err)
	}
	existingSession, err := s.CreateSession(existingFolder, testSession("existing-sess"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.Merge(marshalPayload(t, mergeFixture()), "Imported 2026-01-02")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	want := MergeResult{Folders: 2, Sessions: 3, Credentials: 1, SavedJumpHosts: 1, RemappedReferences: 2}
	if res != want {
		t.Fatalf("MergeResult = %+v, want %+v", res, want)
	}

	tree := s.Tree()
	names := flattenNames(tree)
	for _, name := range []string{"Alpha", "Beta", "one", "two", "root-sess"} {
		if names[name] == "" {
			t.Fatalf("node %q missing from tree", name)
		}
	}

	// Existing nodes stay where they were; the wrapper is appended at root.
	if len(tree) != 2 || tree[0].ID != existingFolder || tree[1].Kind != KindFolder {
		t.Fatalf("root = %+v", tree)
	}
	if tree[1].Name != "Imported 2026-01-02" {
		t.Fatalf("wrapper name = %q", tree[1].Name)
	}
	if ids := childIDs(tree, existingFolder); len(ids) != 1 || ids[0] != existingSession {
		t.Fatalf("existing folder children = %v", ids)
	}
	if _, err := s.Session(existingSession); err != nil {
		t.Fatalf("existing session lost: %v", err)
	}

	// Imported top level is reparented into the wrapper, order preserved.
	if ids := childIDs(tree, tree[1].ID); len(ids) != 2 || ids[0] != names["Alpha"] || ids[1] != names["root-sess"] {
		t.Fatalf("wrapper children = %v", ids)
	}
	if ids := childIDs(tree, names["Alpha"]); len(ids) != 2 || ids[0] != names["Beta"] || ids[1] != names["one"] {
		t.Fatalf("Alpha children = %v", ids)
	}
	if ids := childIDs(tree, names["Beta"]); len(ids) != 1 || ids[0] != names["two"] {
		t.Fatalf("Beta children = %v", ids)
	}

	// Every imported ID is fresh.
	for old, name := range map[string]string{"fold-a": "Alpha", "fold-b": "Beta", "sess-1": "one", "sess-2": "two", "sess-root": "root-sess"} {
		if names[name] == old {
			t.Fatalf("imported %q kept its ID %q", name, old)
		}
	}
	for _, old := range []string{"fold-a", "fold-b", "sess-1", "sess-2", "sess-root"} {
		if _, err := s.Session(old); err == nil {
			t.Fatalf("old node id %q resolves in the merged tree", old)
		}
	}

	// Credentials / saved jump hosts are remapped and references rewritten.
	creds := s.ListCredentials()
	if len(creds) != 1 || creds[0].Name != "C1" || creds[0].ID == "cred-1" {
		t.Fatalf("credentials = %+v", creds)
	}
	jumps := s.ListSavedJumpHosts()
	if len(jumps) != 1 || jumps[0].Name != "J1" || jumps[0].ID == "jump-1" {
		t.Fatalf("saved jump hosts = %+v", jumps)
	}
	sess1, err := s.Session(names["one"])
	if err != nil {
		t.Fatal(err)
	}
	if sess1.CredentialID != creds[0].ID {
		t.Fatalf("session.credentialId = %q, want %q", sess1.CredentialID, creds[0].ID)
	}
	sess2, err := s.Session(names["two"])
	if err != nil {
		t.Fatal(err)
	}
	if sess2.JumpHostRef != jumps[0].ID {
		t.Fatalf("session.jumpHostRef = %q, want %q", sess2.JumpHostRef, jumps[0].ID)
	}
	if _, err := s.Credential("cred-1"); err == nil {
		t.Fatal("old credential ID resolved")
	}
	if _, err := s.SavedJumpHost("jump-1"); err == nil {
		t.Fatal("old saved-jump-host ID resolved")
	}
	if _, err := s.Session("sess-1"); err == nil {
		t.Fatal("old session ID resolved")
	}

	// The search index is rebuilt for the imported sessions.
	hits := s.Search("one")
	if len(hits) != 1 || hits[0].ID != names["one"] {
		t.Fatalf("search hits = %+v, want the imported session", hits)
	}
}

func TestMergeRejectsInvalidEntityWithoutMutation(t *testing.T) {
	s := New(nil)
	if _, err := s.CreateFolder("", "Keep"); err != nil {
		t.Fatal(err)
	}
	before, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"invalid folder", marshalPayload(t, model.Payload{Folders: []model.Folder{{ID: "f", Name: ""}}})},
		{"invalid session", marshalPayload(t, model.Payload{Sessions: []model.Session{{ID: "s", Name: "", Host: "h", Port: 22, User: "u", Auth: model.Auth{Type: model.AuthPassword, Password: "p"}}}})},
		{"invalid credential", marshalPayload(t, model.Payload{Credentials: []model.Credential{{ID: "c", Name: "", User: "u", Auth: model.Auth{Type: model.AuthPassword, Password: "p"}}}})},
		{"invalid jump host", marshalPayload(t, model.Payload{SavedJumpHosts: []model.SavedJumpHost{{ID: "j", Name: "J", Host: "", Port: 22, User: "u", Auth: model.Auth{Type: model.AuthPassword, Password: "p"}}}})},
		{"duplicate node id", marshalPayload(t, model.Payload{
			Folders:  []model.Folder{{ID: "x", Name: "F"}},
			Sessions: []model.Session{{ID: "x", Name: "S", Host: "h", Port: 22, User: "u", Auth: model.Auth{Type: model.AuthPassword, Password: "p"}}},
		})},
		{"malformed json", []byte("{not json")},
	} {
		if _, err := s.Merge(tc.raw, "Imported 2026-01-02"); err == nil {
			t.Fatalf("%s: Merge succeeded, want error", tc.name)
		}
		after, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: store mutated by a rejected import", tc.name)
		}
	}
}

func TestMergeRejectsDanglingReferences(t *testing.T) {
	s := New(nil)
	before, _ := s.Encode()

	danglingCred := mergeFixture()
	danglingCred.Credentials = nil
	if _, err := s.Merge(marshalPayload(t, danglingCred), "Imported x"); err == nil {
		t.Fatal("dangling credential reference accepted")
	}

	danglingJump := mergeFixture()
	danglingJump.SavedJumpHosts = nil
	if _, err := s.Merge(marshalPayload(t, danglingJump), "Imported x"); err == nil {
		t.Fatal("dangling jump-host reference accepted")
	}

	after, _ := s.Encode()
	if !bytes.Equal(before, after) {
		t.Fatal("store mutated by a rejected import")
	}
}

func TestMergeEmptyPayloadIsNoop(t *testing.T) {
	s := New(nil)
	if _, err := s.CreateFolder("", "Keep"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Encode()

	for _, raw := range [][]byte{[]byte("{}"), []byte(`{"root":[],"folders":[],"sessions":[],"credentials":[],"savedJumpHosts":[]}`)} {
		res, err := s.Merge(raw, "Imported x")
		if err != nil {
			t.Fatalf("Merge(empty): %v", err)
		}
		if res != (MergeResult{}) {
			t.Fatalf("Merge(empty) = %+v, want zero", res)
		}
	}
	after, _ := s.Encode()
	if !bytes.Equal(before, after) {
		t.Fatal("empty merge mutated the store")
	}
}

func TestMergeAllowsDuplicateNames(t *testing.T) {
	s := New(nil)
	if _, err := s.CreateSession("", testSession("dup")); err != nil {
		t.Fatal(err)
	}
	dup := pwSession("dup")
	dup.ID = "dup-import"
	payload := model.Payload{Sessions: []model.Session{dup}}
	if _, err := s.Merge(marshalPayload(t, payload), "Imported x"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	tree := s.Tree()
	dups := 0
	var walk func([]TreeNode)
	walk = func(ns []TreeNode) {
		for _, n := range ns {
			if n.Name == "dup" {
				dups++
			}
			walk(n.Children)
		}
	}
	walk(tree)
	if dups != 2 {
		t.Fatalf("duplicate-name sessions = %d, want 2", dups)
	}
}

// TestMergeRecoversOrphans keeps parity with Load: a node referenced nowhere is
// recovered (sessions into their declared folder, folders into the root) rather
// than silently dropped.
func TestMergeRecoversOrphans(t *testing.T) {
	s := New(nil)
	payload := model.Payload{
		Folders:  []model.Folder{{ID: "f", Name: "OrphanFolder"}},
		Sessions: []model.Session{pwSession("orphan-sess")},
	}
	payload.Sessions[0].ID = "s-orphan"

	res, err := s.Merge(marshalPayload(t, payload), "Imported 2026-01-02")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Folders != 1 || res.Sessions != 1 {
		t.Fatalf("MergeResult = %+v", res)
	}
	names := flattenNames(s.Tree())
	if names["OrphanFolder"] == "" || names["orphan-sess"] == "" {
		t.Fatalf("orphans not recovered: %v", names)
	}
}

// TestMergeDataOnlyImport documents the empty-node behavior: credentials and
// saved jump hosts import even when there are no tree nodes (no wrapper folder
// is created).
func TestMergeDataOnlyImport(t *testing.T) {
	s := New(nil)
	payload := mergeFixture()
	payload.Root = nil
	payload.Folders = nil
	payload.Sessions = nil

	res, err := s.Merge(marshalPayload(t, payload), "Imported x")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Folders != 0 || res.Sessions != 0 || res.Credentials != 1 || res.SavedJumpHosts != 1 {
		t.Fatalf("MergeResult = %+v", res)
	}
	if len(s.Tree()) != 0 {
		t.Fatalf("data-only merge created tree nodes: %+v", s.Tree())
	}
	if len(s.ListCredentials()) != 1 || len(s.ListSavedJumpHosts()) != 1 {
		t.Fatal("data-only merge dropped credentials/jump hosts")
	}
}
