package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"shelve/internal/model"
)

// Sentinel errors.
var (
	// ErrNodeNotFound: the node ID does not exist in the tree.
	ErrNodeNotFound = errors.New("store: node not found")
	// ErrInvalidParent: the referenced parent folder does not exist.
	ErrInvalidParent = errors.New("store: parent folder not found")
	// ErrMoveCycle: a folder is being moved into itself or a
	// descendant folder (would orphan its own subtree).
	ErrMoveCycle = errors.New("store: cannot move a folder into its own descendant")
	// ErrCredentialNotFound: the credential ID does not exist (plan P003).
	ErrCredentialNotFound = errors.New("store: credential not found")
)

// SaveFunc persists an encoded model.Payload (wired to vault.Save by the
// composition root).
type SaveFunc func(payload []byte) error

// saveDebounce is the master plan §4 persistence debounce.
const saveDebounce = 300 * time.Millisecond

// Store is the in-memory session tree (master plan §4/§5): folders +
// sessions maps plus one ordered-ID list per parent ("" = root), and the
// named credentials slice (plan P003 §4.2). A single RWMutex guards all
// state; every mutation schedules a debounced (300 ms) save through the
// SaveFunc callback.
type Store struct {
	mu          sync.RWMutex
	folders     map[string]model.Folder
	sessions    map[string]model.Session
	credentials map[string]model.Credential
	order       map[string][]string // parentID ("" = root) → ordered child node IDs

	timerMu sync.Mutex
	timer   *time.Timer

	save SaveFunc
}

// New creates an empty Store. save may be nil (then saves are no-ops,
// useful in tests).
func New(save SaveFunc) *Store {
	return &Store{
		folders:     map[string]model.Folder{},
		sessions:    map[string]model.Session{},
		credentials: map[string]model.Credential{},
		order:       map[string][]string{},
		save:        save,
	}
}

// ------------------------------------------------------------------ io ---

// Encode serializes the current tree to the model.Payload wire shape.
func (s *Store) Encode() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.encodeLocked()
}

func (s *Store) encodeLocked() ([]byte, error) {
	nodes := make([]model.Folder, 0, len(s.folders))
	for id, f := range s.folders {
		f.Children = append([]string(nil), s.order[id]...)
		nodes = append(nodes, f)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	sessions := make([]model.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })

	credentials := make([]model.Credential, 0, len(s.credentials))
	for _, cred := range s.credentials {
		credentials = append(credentials, cred)
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].ID < credentials[j].ID })

	return json.Marshal(model.Payload{
		Root:        append([]string(nil), s.order[""]...),
		Folders:     nodes,
		Sessions:    sessions,
		Credentials: credentials,
	})
}

// Flush synchronously persists the tree, first canceling any pending
// debounced save (master plan §4: flush on Lock and app exit).
func (s *Store) Flush() error {
	s.timerMu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.timerMu.Unlock()

	if s.save == nil {
		return nil
	}
	payload, err := s.Encode()
	if err != nil {
		return err
	}
	return s.save(payload)
}

// scheduleSave (re)arms the 300 ms debounce: the write lands 300 ms
// after the LAST mutation.
func (s *Store) scheduleSave() {
	s.timerMu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(saveDebounce, func() {
		if err := s.Flush(); err != nil {
			log.Printf("store: autosave failed: %v", err)
		}
	})
	s.timerMu.Unlock()
}

// Load rebuilds the tree from a decrypted vault payload, replacing all
// current state. Order lists are the source of truth: Folder.Children
// and the root list determine placement, and the stored
// FolderID/ParentID fields are normalized to agree with them. It does
// not trigger a save.
func (s *Store) Load(payload []byte) error {
	var p model.Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("store: invalid payload: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.folders = map[string]model.Folder{}
	s.sessions = map[string]model.Session{}
	s.credentials = map[string]model.Credential{}
	order := map[string][]string{}

	for _, f := range p.Folders {
		s.folders[f.ID] = f
	}
	for _, sess := range p.Sessions {
		s.sessions[sess.ID] = sess
	}
	for _, cred := range p.Credentials {
		s.credentials[cred.ID] = cred
	}
	// Defensive (plan P003 §4.1): a session whose CredentialID dangles —
	// e.g. the credential was removed outside the store — is downgraded to
	// its inline snapshot instead of failing at connect time.
	for id, sess := range s.sessions {
		if sess.CredentialID == "" {
			continue
		}
		if _, ok := s.credentials[sess.CredentialID]; !ok {
			sess.CredentialID = ""
			s.sessions[id] = sess
		}
	}

	known := func(id string) bool {
		_, ok := s.folders[id]
		if ok {
			return true
		}
		_, ok = s.sessions[id]
		return ok
	}
	keep := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			if !seen[id] && known(id) {
				seen[id] = true
				out = append(out, id)
			}
		}
		return out
	}

	order[""] = keep(p.Root)
	for id := range s.folders {
		order[id] = nil
	}
	for _, f := range p.Folders {
		if _, ok := s.folders[f.ID]; ok {
			order[f.ID] = keep(f.Children)
		}
	}

	// Defensive: nodes referenced nowhere are recovered into the root
	// (sessions: into their declared folder when it exists) instead of
	// being silently lost.
	placed := map[string]bool{}
	for _, ids := range order {
		for _, id := range ids {
			placed[id] = true
		}
	}
	for id, sess := range s.sessions {
		if placed[id] {
			continue
		}
		parent := sess.FolderID
		if parent != "" {
			if _, ok := s.folders[parent]; !ok {
				parent = ""
			}
		}
		order[parent] = append(order[parent], id)
		placed[id] = true
	}
	var orphans []string
	for id := range s.folders {
		if !placed[id] {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)
	order[""] = append(order[""], orphans...)

	// Normalize membership fields from the order lists.
	for parent, ids := range order {
		for _, id := range ids {
			if f, ok := s.folders[id]; ok {
				f.ParentID = parent
				s.folders[id] = f
			}
			if sess, ok := s.sessions[id]; ok {
				sess.FolderID = parent
				s.sessions[id] = sess
			}
		}
	}

	s.order = order
	return nil
}

// ----------------------------------------------------------------- crud ---

// CreateFolder adds a folder under parentID ("" = root) and returns its ID.
func (s *Store) CreateFolder(parentID, name string) (string, error) {
	f := model.Folder{Name: name}
	if err := f.Validate(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if parentID != "" {
		if _, ok := s.folders[parentID]; !ok {
			return "", fmt.Errorf("%w: %q", ErrInvalidParent, parentID)
		}
	}
	f.ID = model.NewID()
	f.ParentID = parentID
	s.folders[f.ID] = f
	s.order[parentID] = append(s.order[parentID], f.ID)
	s.scheduleSave()
	return f.ID, nil
}

// RenameFolder renames an existing folder.
func (s *Store) RenameFolder(id, name string) error {
	f := model.Folder{ID: id, Name: name}
	if err := f.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.folders[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}
	f.Name = name
	s.folders[id] = f
	s.scheduleSave()
	return nil
}

// CreateSession adds a session under folderID ("" = root) and returns
// its ID. The session is fully validated (master plan §4).
func (s *Store) CreateSession(folderID string, session model.Session) (string, error) {
	sess := session
	sess.ID = model.NewID()
	sess.FolderID = folderID
	if err := sess.Validate(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if folderID != "" {
		if _, ok := s.folders[folderID]; !ok {
			return "", fmt.Errorf("%w: %q", ErrInvalidParent, folderID)
		}
	}
	if err := s.checkCredentialRefLocked(sess.CredentialID); err != nil {
		return "", err
	}
	s.sessions[sess.ID] = sess
	s.order[folderID] = append(s.order[folderID], sess.ID)
	s.scheduleSave()
	return sess.ID, nil
}

// UpdateSession replaces a session's editable fields. The session ID
// and placement (folder + position) are unchanged — use MoveNode to
// move. A non-empty ID is required.
func (s *Store) UpdateSession(session model.Session) error {
	if session.ID == "" {
		return fmt.Errorf("%w: empty ID", ErrNodeNotFound)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.sessions[session.ID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNodeNotFound, session.ID)
	}
	sess := session
	sess.FolderID = old.FolderID
	if err := sess.Validate(); err != nil {
		return err
	}
	if err := s.checkCredentialRefLocked(sess.CredentialID); err != nil {
		return err
	}
	s.sessions[sess.ID] = sess
	s.scheduleSave()
	return nil
}

// DuplicateSession copies a session into the same folder, right after
// the original, with the name suffixed " (copy)". Returns the new ID.
func (s *Store) DuplicateSession(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sessions[id]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}
	dst := src
	dst.ID = model.NewID()
	dst.Name = src.Name + " (copy)"
	dst.JumpHosts = append([]model.JumpHost(nil), src.JumpHosts...)
	s.sessions[dst.ID] = dst

	parent := src.FolderID
	list := s.order[parent]
	newList := make([]string, 0, len(list)+1)
	placed := false
	for _, x := range list {
		newList = append(newList, x)
		if x == id {
			newList = append(newList, dst.ID)
			placed = true
		}
	}
	if !placed { // defensive: original missing from its order list
		newList = append(newList, dst.ID)
	}
	s.order[parent] = newList
	s.scheduleSave()
	return dst.ID, nil
}

// --------------------------------------------------------- credentials ---

// CreateCredential adds a named credential (plan P003 §4.2) and returns
// its ID. Fully validated (name, user, auth XOR) like sessions.
func (s *Store) CreateCredential(cred model.Credential) (string, error) {
	cred.ID = model.NewID()
	if err := cred.Validate(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials[cred.ID] = cred
	s.scheduleSave()
	return cred.ID, nil
}

// UpdateCredential replaces a credential's editable fields. A non-empty
// ID is required. Referencing sessions keep their inline snapshot; the
// credential itself remains authoritative at connect time (plan P003).
func (s *Store) UpdateCredential(cred model.Credential) error {
	if cred.ID == "" {
		return fmt.Errorf("%w: empty ID", ErrCredentialNotFound)
	}
	if err := cred.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.credentials[cred.ID]; !ok {
		return fmt.Errorf("%w: %q", ErrCredentialNotFound, cred.ID)
	}
	s.credentials[cred.ID] = cred
	s.scheduleSave()
	return nil
}

// DeleteCredential removes a credential and soft-nulls CredentialID on
// every session referencing it (plan P003 §4.2): those sessions keep
// connecting with their inline snapshot. Returns the number of sessions
// whose reference was cleared (for the A8-style confirm dialog).
func (s *Store) DeleteCredential(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.credentials[id]; !ok {
		return 0, fmt.Errorf("%w: %q", ErrCredentialNotFound, id)
	}
	delete(s.credentials, id)
	affected := 0
	for sessID, sess := range s.sessions {
		if sess.CredentialID != id {
			continue
		}
		sess.CredentialID = ""
		s.sessions[sessID] = sess
		affected++
	}
	s.scheduleSave()
	return affected, nil
}

// ListCredentials returns a copy of every credential, sorted by display
// name for a stable dropdown/manager order.
func (s *Store) ListCredentials() []model.Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Credential, 0, len(s.credentials))
	for _, cred := range s.credentials {
		out = append(out, cred)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Credential returns a copy of the credential with the given ID.
func (s *Store) Credential(id string) (model.Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cred, ok := s.credentials[id]
	if !ok {
		return model.Credential{}, fmt.Errorf("%w: %q", ErrCredentialNotFound, id)
	}
	return cred, nil
}

// CredentialUsage returns how many sessions currently reference the
// credential (for the A8-style delete-confirm dialog, plan P003 §4.5).
func (s *Store) CredentialUsage(id string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, sess := range s.sessions {
		if sess.CredentialID == id {
			n++
		}
	}
	return n
}

// checkCredentialRefLocked enforces the plan P003 §4.1 invariant that a
// session's CredentialID, when set, references an existing credential.
// Requires s.mu.
func (s *Store) checkCredentialRefLocked(id string) error {
	if id == "" {
		return nil
	}
	if _, ok := s.credentials[id]; !ok {
		return fmt.Errorf("session.credentialId: references unknown credential %q", id)
	}
	return nil
}

// DeleteNode removes a session, or a folder together with its whole
// subtree. Returns the number of removed nodes (sessions + folders).
func (s *Store) DeleteNode(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	isFolder, isSession := s.exists(id)
	if !isFolder && !isSession {
		return 0, fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}

	var parent string
	if isFolder {
		parent = s.folders[id].ParentID
	} else {
		parent = s.sessions[id].FolderID
	}

	ids := []string{id}
	if isFolder {
		ids = s.subtree(id)
	}
	for _, x := range ids {
		if _, isF := s.folders[x]; isF {
			delete(s.order, x) // a folder's order list goes away with it
		}
		delete(s.folders, x)
		delete(s.sessions, x)
	}
	s.order[parent] = removeID(s.order[parent], id)
	s.scheduleSave()
	return len(ids), nil
}

// MoveNode re-positions a node under newParentID ("" = root) at sibling
// position index (negative clamps to 0, beyond the end clamps to the
// end). Moving a folder into itself or one of its descendants is
// rejected.
func (s *Store) MoveNode(id, newParentID string, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	isFolder, isSession := s.exists(id)
	if !isFolder && !isSession {
		return fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}
	if newParentID == id {
		return fmt.Errorf("%w: %q (self)", ErrMoveCycle, id)
	}
	if isFolder {
		for _, d := range s.subtree(id)[1:] {
			if d == newParentID {
				return fmt.Errorf("%w: %q", ErrMoveCycle, newParentID)
			}
		}
	}
	if newParentID != "" {
		if _, ok := s.folders[newParentID]; !ok {
			return fmt.Errorf("%w: %q", ErrInvalidParent, newParentID)
		}
	}

	var oldParent string
	if isFolder {
		oldParent = s.folders[id].ParentID
	} else {
		oldParent = s.sessions[id].FolderID
	}

	s.order[oldParent] = removeID(s.order[oldParent], id)
	if index < 0 {
		index = 0
	}
	list := s.order[newParentID]
	if index > len(list) {
		index = len(list)
	}
	placed := make([]string, 0, len(list)+1)
	placed = append(placed, list[:index]...)
	placed = append(placed, id)
	placed = append(placed, list[index:]...)
	s.order[newParentID] = placed

	if isFolder {
		f := s.folders[id]
		f.ParentID = newParentID
		s.folders[id] = f
	} else {
		sess := s.sessions[id]
		sess.FolderID = newParentID
		s.sessions[id] = sess
	}
	s.scheduleSave()
	return nil
}

// --------------------------------------------------------------- views ---

// Tree returns an ordered snapshot of the tree (root level first).
func (s *Store) Tree() []TreeNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.build("")
}

func (s *Store) build(parent string) []TreeNode {
	ids := s.order[parent]
	out := make([]TreeNode, 0, len(ids))
	for _, id := range ids {
		if f, ok := s.folders[id]; ok {
			out = append(out, TreeNode{
				Kind:     KindFolder,
				ID:       id,
				Name:     f.Name,
				Children: s.build(id),
			})
		} else if sess, ok := s.sessions[id]; ok {
			out = append(out, TreeNode{
				Kind:     KindSession,
				ID:       id,
				Name:     sess.Name,
				Children: []TreeNode{},
			})
		}
	}
	return out
}

// Session returns a copy of the session with the given ID.
func (s *Store) Session(id string) (model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return model.Session{}, fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}
	return sess, nil
}

// SearchHit is one flat search result (master plan §2 A9): the session
// plus the slash-joined path of the folder that contains it ("" for a
// root-level session).
type SearchHit struct {
	ID         string
	Name       string
	Host       string
	User       string
	FolderPath string
}

// Search returns every session whose Name, Host or User contains q as a
// case-insensitive substring (master plan §2 A9), with its folder path
// for display context. Results are sorted by ID for a stable order. The
// flat scan over ≤300 nodes is well inside the master-plan §6 budget.
func (s *Store) Search(q string) []SearchHit {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []SearchHit
	for id, sess := range s.sessions {
		if !containsFold(sess.Name, q) && !containsFold(sess.Host, q) && !containsFold(sess.User, q) {
			continue
		}
		out = append(out, SearchHit{
			ID:         id,
			Name:       sess.Name,
			Host:       sess.Host,
			User:       sess.User,
			FolderPath: s.folderPathLocked(sess.FolderID),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), needle)
}

// folderPathLocked returns the slash-joined path of the folder with the
// given ID ("" for the root). Requires s.mu.
func (s *Store) folderPathLocked(folderID string) string {
	var parts []string
	cur := folderID
	for cur != "" {
		f, ok := s.folders[cur]
		if !ok {
			break
		}
		parts = append([]string{f.Name}, parts...)
		cur = f.ParentID
	}
	return strings.Join(parts, "/")
}

// ---------------------------------------------- internal helpers (lock) ---

// exists must be called with s.mu held.
func (s *Store) exists(id string) (isFolder, isSession bool) {
	_, isFolder = s.folders[id]
	_, isSession = s.sessions[id]
	return isFolder, isSession
}

// subtree returns id and every descendant node ID. Requires s.mu.
func (s *Store) subtree(id string) []string {
	out := []string{id}
	for _, child := range s.order[id] {
		if _, isF := s.folders[child]; isF {
			out = append(out, s.subtree(child)...)
		} else {
			out = append(out, child)
		}
	}
	return out
}

func removeID(list []string, id string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}
