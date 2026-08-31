package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"shelve/internal/model"
)

func testSession(name string) model.Session {
	return model.Session{
		Name: name,
		Host: "host.example.com",
		Port: 22,
		User: "alice",
		Auth: model.Auth{Type: model.AuthPassword, Password: "pw"},
	}
}

func countNodes(trees []TreeNode) int {
	n := 0
	var walk func(ns []TreeNode)
	walk = func(ns []TreeNode) {
		for _, node := range ns {
			n++
			walk(node.Children)
		}
	}
	walk(trees)
	return n
}

func topLevelIDs(tree []TreeNode) []string {
	out := make([]string, 0, len(tree))
	for _, n := range tree {
		out = append(out, n.ID)
	}
	return out
}

// childIDs finds the node with id in tree and returns its direct child IDs.
func childIDs(tree []TreeNode, id string) []string {
	var out []string
	var rec func(ns []TreeNode)
	rec = func(ns []TreeNode) {
		for _, n := range ns {
			if n.ID == id {
				for _, c := range n.Children {
					out = append(out, c.ID)
				}
				return
			}
			rec(n.Children)
		}
	}
	rec(tree)
	return out
}

func TestCreateFolderAndSessionOrdering(t *testing.T) {
	s := New(nil)
	f1, err := s.CreateFolder("", "F1")
	if err != nil {
		t.Fatal(err)
	}
	f2, err := s.CreateFolder("", "F2")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateFolder(f1, "F1a")
	if err != nil {
		t.Fatal(err)
	}
	s1, err := s.CreateSession(f1, testSession("s1"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := s.CreateSession(sub, testSession("s2"))
	if err != nil {
		t.Fatal(err)
	}
	s3, err := s.CreateSession("", testSession("s3"))
	if err != nil {
		t.Fatal(err)
	}

	tree := s.Tree()
	if got := topLevelIDs(tree); !reflect.DeepEqual(got, []string{f1, f2, s3}) {
		t.Fatalf("root order = %v, want [%v %v %v]", got, f1, f2, s3)
	}
	if got := childIDs(tree, f1); !reflect.DeepEqual(got, []string{sub, s1}) {
		t.Fatalf("F1 order = %v, want [F1a s1]", got)
	}
	if got := childIDs(tree, sub); !reflect.DeepEqual(got, []string{s2}) {
		t.Fatalf("F1a order = %v, want [s2]", got)
	}

	if tree[0].Kind != KindFolder || tree[2].Kind != KindSession {
		t.Fatalf("kinds wrong: %+v", tree)
	}
	if tree[0].Name != "F1" || tree[2].Name != "s3" {
		t.Fatalf("names wrong: %+v", tree)
	}

	// Validation gates.
	if _, err := s.CreateFolder("", "  "); err == nil {
		t.Fatal("empty folder name accepted")
	}
	if _, err := s.CreateFolder("nope", "X"); err == nil {
		t.Fatal("unknown parent accepted")
	}
	if _, err := s.CreateSession("nope", testSession("x")); err == nil {
		t.Fatal("unknown session folder accepted")
	}
	bad := testSession("x")
	bad.Port = 99999
	if _, err := s.CreateSession(f1, bad); err == nil {
		t.Fatal("invalid session accepted")
	}
}

func TestRenameFolder(t *testing.T) {
	s := New(nil)
	f, _ := s.CreateFolder("", "Before")
	if err := s.RenameFolder(f, "After"); err != nil {
		t.Fatal(err)
	}
	tree := s.Tree()
	if tree[0].Name != "After" {
		t.Fatalf("name = %q, want After", tree[0].Name)
	}
	if err := s.RenameFolder("nope", "X"); err == nil {
		t.Fatal("renaming unknown folder accepted")
	}
	if err := s.RenameFolder(f, "  "); err == nil {
		t.Fatal("empty folder name accepted on rename")
	}
}

func TestMoveNode(t *testing.T) {
	s := New(nil)
	f1, _ := s.CreateFolder("", "F1")
	f2, _ := s.CreateFolder("", "F2")
	sub, _ := s.CreateFolder(f1, "F1a")
	s1, _ := s.CreateSession(f1, testSession("s1"))
	s2, _ := s.CreateSession(f2, testSession("s2"))
	rootS, _ := s.CreateSession("", testSession("root-s"))
	_ = sub

	// Session into a folder at index 0.
	if err := s.MoveNode(s2, f1, 0); err != nil {
		t.Fatal(err)
	}
	if got := childIDs(s.Tree(), f1); !reflect.DeepEqual(got, []string{s2, sub, s1}) {
		t.Fatalf("F1 after move = %v, want [s2 F1a s1]", got)
	}
	// Folder moved to end of root (index out of range clamps).
	if err := s.MoveNode(f2, "", 999); err != nil {
		t.Fatal(err)
	}
	root := topLevelIDs(s.Tree())
	if root[len(root)-1] != f2 {
		t.Fatalf("F2 not last at root: %v", root)
	}
	// Negative index clamps to 0.
	if err := s.MoveNode(rootS, f1, -5); err != nil {
		t.Fatal(err)
	}
	if got := childIDs(s.Tree(), f1); len(got) == 0 || got[0] != rootS {
		t.Fatalf("rootS not first in F1: %v", got)
	}
	// Moving within the same folder re-indexes correctly.
	if err := s.MoveNode(s2, f1, 999); err != nil {
		t.Fatal(err)
	}
	if got := childIDs(s.Tree(), f1); got[len(got)-1] != s2 {
		t.Fatalf("s2 not last in F1: %v", got)
	}

	// Unknown node / unknown parent.
	if err := s.MoveNode("nope", f1, 0); err == nil {
		t.Fatal("moving unknown node accepted")
	}
	if err := s.MoveNode(s2, "nope", 0); err == nil {
		t.Fatal("moving to unknown parent accepted")
	}
}

func TestMoveFolderIntoOwnDescendantRejected(t *testing.T) {
	s := New(nil)
	f1, _ := s.CreateFolder("", "F1")
	sub1, _ := s.CreateFolder(f1, "F1a")
	sub2, _ := s.CreateFolder(sub1, "F1a2")

	if err := s.MoveNode(f1, sub1, 0); err == nil {
		t.Fatal("moving folder into its child accepted")
	}
	if err := s.MoveNode(f1, sub2, 0); err == nil {
		t.Fatal("moving folder into grandchild accepted")
	}
	if err := s.MoveNode(f1, f1, 0); err == nil {
		t.Fatal("moving folder into itself accepted")
	}
	// Sessions may move into the same subtree.
	sess, _ := s.CreateSession(sub2, testSession("s"))
	if err := s.MoveNode(sess, f1, 0); err != nil {
		t.Fatalf("legal session move rejected: %v", err)
	}
}

func TestDuplicateSession(t *testing.T) {
	s := New(nil)
	f, _ := s.CreateFolder("", "F")
	orig := testSession("prod-db")
	orig.JumpHosts = []model.JumpHost{
		{Host: "jmp.example.com", Port: 22, User: "t",
			Auth: model.Auth{Type: model.AuthPassword, Password: "jpw"}},
	}
	id, err := s.CreateSession(f, orig)
	if err != nil {
		t.Fatal(err)
	}
	copyID, err := s.DuplicateSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if copyID == id {
		t.Fatal("duplicate got the same ID")
	}

	src, _ := s.Session(id)
	dst, _ := s.Session(copyID)
	if dst.Name != "prod-db (copy)" {
		t.Fatalf("copy name = %q", dst.Name)
	}
	if dst.FolderID != f {
		t.Fatalf("copy folder = %q, want %q", dst.FolderID, f)
	}
	if dst.Host != src.Host || dst.Auth.Password != src.Auth.Password || dst.Port != src.Port {
		t.Fatalf("copy did not preserve data: %+v vs %+v", dst, src)
	}

	// Positioned right after the original.
	if got := childIDs(s.Tree(), f); !reflect.DeepEqual(got, []string{id, copyID}) {
		t.Fatalf("order = %v, want [orig copy]", got)
	}

	// Deep copy: mutating the copy's jump host must not touch the original.
	mutated := dst
	mutated.JumpHosts[0].Host = "mutated.example.com"
	if err := s.UpdateSession(mutated); err != nil {
		t.Fatal(err)
	}
	src2, _ := s.Session(id)
	if src2.JumpHosts[0].Host != "jmp.example.com" {
		t.Fatal("duplicate shares jump-host slice with original")
	}
	if _, err := s.DuplicateSession("nope"); err == nil {
		t.Fatal("duplicating unknown session accepted")
	}
}

func TestUpdateSession(t *testing.T) {
	s := New(nil)
	f, _ := s.CreateFolder("", "F")
	id, _ := s.CreateSession(f, testSession("before"))

	upd := testSession("after")
	upd.ID = id
	upd.Host = "other.example.com"
	if err := s.UpdateSession(upd); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Session(id)
	if got.Name != "after" || got.Host != "other.example.com" {
		t.Fatalf("update not applied: %+v", got)
	}
	if got.FolderID != f {
		t.Fatal("update moved the session")
	}
	// Validation applies.
	bad := upd
	bad.Port = 0
	if err := s.UpdateSession(bad); err == nil {
		t.Fatal("invalid update accepted")
	}
	none := testSession("x")
	none.ID = "nope"
	if err := s.UpdateSession(none); err == nil {
		t.Fatal("update of unknown session accepted")
	}
	if err := s.UpdateSession(testSession("x")); err == nil {
		t.Fatal("update without ID accepted")
	}
}

func TestDeleteNodeCounts(t *testing.T) {
	s := New(nil)
	f1, _ := s.CreateFolder("", "F1")
	sub, _ := s.CreateFolder(f1, "F1a")
	deep, _ := s.CreateFolder(sub, "F1a2")
	sA, _ := s.CreateSession(deep, testSession("deep-s"))
	_, _ = s.CreateSession(sub, testSession("sub-s"))
	_, _ = s.CreateSession(f1, testSession("f1-s"))
	rootS, _ := s.CreateSession("", testSession("root-s"))

	// Single session delete.
	n, err := s.DeleteNode(rootS)
	if err != nil || n != 1 {
		t.Fatalf("delete session: n=%d err=%v", n, err)
	}
	// Recursive folder delete: F1 + F1a + F1a2 + 3 sessions = 6.
	n, err = s.DeleteNode(f1)
	if err != nil || n != 6 {
		t.Fatalf("delete folder: n=%d err=%v, want 6", n, err)
	}
	if got := countNodes(s.Tree()); got != 0 {
		t.Fatalf("tree not empty after deletes: %d nodes", got)
	}
	if _, err := s.DeleteNode(f1); err == nil {
		t.Fatal("deleting already-deleted node accepted")
	}
	if _, err := s.DeleteNode("nope"); err == nil {
		t.Fatal("deleting unknown node accepted")
	}
	_ = sA
}

func TestEncodeLoadRoundTrip(t *testing.T) {
	s := New(nil)
	f1, _ := s.CreateFolder("", "F1")
	f2, _ := s.CreateFolder("", "F2")
	sub, _ := s.CreateFolder(f1, "F1a")
	s1, _ := s.CreateSession(f1, testSession("s1"))
	s2, _ := s.CreateSession(sub, testSession("s2"))
	s3, _ := s.CreateSession(f2, testSession("s3"))
	_ = s1
	// Interleave: pull s2 to the root level between the folders.
	if err := s.MoveNode(s2, "", 1); err != nil {
		t.Fatal(err)
	}

	payload, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}

	s2nd := New(nil)
	if err := s2nd.Load(payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s2nd.Tree(), s.Tree()) {
		t.Fatalf("tree mismatch after reload:\n got %+v\nwant %+v", s2nd.Tree(), s.Tree())
	}
	// Session placement survives the round trip.
	got, err := s2nd.Session(s3)
	if err != nil || got.FolderID != f2 {
		t.Fatalf("session placement after reload: %+v, %v", got, err)
	}
}

func TestLoadRecoversOrphans(t *testing.T) {
	p := model.Payload{
		Root: []string{"f1", "ghost-folder"},
		Folders: []model.Folder{
			{ID: "f1", Name: "F1"},
			// "ghost-folder" declared in Root but absent from Folders: must be dropped.
		},
		Sessions: []model.Session{
			{ID: "s1", FolderID: "f1", Name: "s1", Host: "h", Port: 22, User: "u",
				Auth: model.Auth{Type: model.AuthPassword, Password: "p"}},
			// Orphan: not in any order list, declared folder missing → root.
			{ID: "s2", FolderID: "missing-folder", Name: "s2", Host: "h", Port: 22, User: "u",
				Auth: model.Auth{Type: model.AuthKey, KeyPath: "/k"}},
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil)
	if err := s.Load(raw); err != nil {
		t.Fatal(err)
	}
	root := topLevelIDs(s.Tree())
	found := map[string]bool{}
	for _, id := range root {
		found[id] = true
	}
	if !found["f1"] || !found["s2"] {
		t.Fatalf("orphan not recovered to root: %v", root)
	}
	if found["ghost-folder"] {
		t.Fatal("ghost folder survived")
	}
	// Session's declared folder was normalized to the real placement.
	sess, _ := s.Session("s2")
	if sess.FolderID != "" {
		t.Fatalf("orphan session FolderID = %q, want root", sess.FolderID)
	}
}

func TestLoadRejectsInvalidPayload(t *testing.T) {
	s := New(nil)
	if err := s.Load([]byte("{not-json")); err == nil {
		t.Fatal("invalid payload accepted")
	}
}

func TestStore300SessionsFixturePerf(t *testing.T) {
	p := model.GenerateFixture(300)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil)
	if err := s.Load(raw); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()
	tree := s.Tree()
	n := countNodes(tree)
	if n != 330 { // 300 sessions + 30 folders
		t.Fatalf("fixture node count = %d, want 330", n)
	}
	// A batch of search-free ops on the 330-node tree.
	firstFolder := tree[0]
	leaf := firstFolder
	for len(leaf.Children) > 0 {
		leaf = leaf.Children[0]
	}
	for i := 0; i < 10; i++ {
		if err := s.MoveNode(leaf.ID, s.Tree()[0].ID, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DuplicateSession(leaf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteNode(leaf.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.Tree(); len(got) == 0 {
		t.Fatal("tree empty after ops")
	}
	if el := time.Since(t0); el > 50*time.Millisecond {
		t.Fatalf("tree ops took %v, budget 50 ms", el)
	}
}

func TestStore300SessionsFullPayloadRoundTrip(t *testing.T) {
	p := model.GenerateFixture(300)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil)
	if err := s.Load(raw); err != nil {
		t.Fatal(err)
	}
	encoded, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var got model.Payload
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 300 || len(got.Folders) != 30 {
		t.Fatalf("payload after round trip: %d sessions / %d folders", len(got.Sessions), len(got.Folders))
	}
}

type recSaver struct {
	mu    sync.Mutex
	calls int
	last  []byte
	ch    chan []byte
}

func (r *recSaver) Save(p []byte) error {
	r.mu.Lock()
	r.calls++
	r.last = p
	r.mu.Unlock()
	if r.ch != nil {
		r.ch <- p
	}
	return nil
}

func (r *recSaver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// debounceDelay is the store's documented 300 ms, plus slack for the
// assertion window.
const debounceDelay = 300 * time.Millisecond

func TestDebouncedSave(t *testing.T) {
	ch := make(chan []byte, 8)
	s := New(func(p []byte) error { ch <- p; return nil })

	t0 := time.Now()
	if _, err := s.CreateFolder("", "A"); err != nil {
		t.Fatal(err)
	}
	// No immediate save.
	select {
	case <-ch:
		t.Fatalf("saved too early after %v", time.Since(t0))
	case <-time.After(80 * time.Millisecond):
	}
	// A second mutation resets the debounce; one write total.
	if _, err := s.CreateFolder("", "B"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		el := time.Since(t0)
		if el < 250*time.Millisecond {
			t.Fatalf("debounced save too early: %v", el)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no debounced save within 3 s")
	}
	select {
	case <-ch:
		t.Fatal("more than one debounced save for one burst")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestFlushIsSynchronous(t *testing.T) {
	rec := &recSaver{ch: make(chan []byte, 4)}
	s := New(rec.Save)
	if _, err := s.CreateFolder("", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if rec.callCount() != 1 {
		t.Fatalf("flush calls = %d, want 1", rec.callCount())
	}
	// Flush clears the pending debounce: no second write follows.
	time.Sleep(debounceDelay + 100*time.Millisecond)
	if rec.callCount() != 1 {
		t.Fatalf("post-flush autosave fired: calls = %d", rec.callCount())
	}
}

func TestConcurrentMutationsRaceFree(t *testing.T) {
	s := New(nil)
	f, _ := s.CreateFolder("", "F")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				id, err := s.CreateSession(f, testSession(fmt.Sprintf("s-%d-%d", g, i)))
				if err != nil {
					return
				}
				if _, err := s.DuplicateSession(id); err != nil {
					return
				}
				if err := s.MoveNode(id, f, -1); err != nil {
					return
				}
				_ = s.Tree()
			}
		}(g)
	}
	wg.Wait()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countNodes(s.Tree()); n != 1+8*20*2 {
		t.Fatalf("node count = %d, want %d", n, 1+8*20*2)
	}
}
