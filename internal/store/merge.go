package store

import (
	"encoding/json"
	"fmt"

	"shelve/internal/model"
)

// MergeResult summarizes an applied import merge (plan
// config-export-import §2). The counts are the imported entity totals; the
// wrapper folder created for the merge is NOT counted, so a merge and a replace
// of the same payload report comparable numbers. RemappedReferences is the
// number of sessions whose CredentialID/JumpHostRef was rewritten.
type MergeResult struct {
	Folders            int
	Sessions           int
	Credentials        int
	SavedJumpHosts     int
	RemappedReferences int
}

// Merge imports an app-exported model.Payload into the current tree,
// all-or-nothing (plan config-export-import §2): every entity is validated and
// the full ID remap is built in memory BEFORE s.mu is acquired, so a rejected
// import leaves the store byte-for-byte unchanged.
//
// Fresh ULIDs are assigned to every imported credential, saved jump host,
// folder and session; CredentialID/JumpHostRef references are remapped to the
// new IDs. The order graph is rebuilt from Root + Folder.Children exactly like
// Load (dedupe, drop unknown IDs, recover unreferenced nodes into the root).
// When the import has at least one node, a new folder named rootFolderName is
// created under the root and the imported top-level nodes are reparented into
// it with their sibling order preserved. Names are never deduplicated. An
// entirely empty payload is a no-op.
func (s *Store) Merge(payload []byte, rootFolderName string) (MergeResult, error) {
	var p model.Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return MergeResult{}, fmt.Errorf("store: invalid import payload: %w", err)
	}

	// --- validate + index the imported entities (all failures abort) -----
	credByOld := make(map[string]model.Credential, len(p.Credentials))
	credOrder := make([]string, 0, len(p.Credentials))
	for i, c := range p.Credentials {
		if c.ID == "" {
			return MergeResult{}, fmt.Errorf("import.credentials[%d]: missing id", i)
		}
		if err := c.Validate(); err != nil {
			return MergeResult{}, fmt.Errorf("import.credentials[%d] (%q): %w", i, c.Name, err)
		}
		if _, dup := credByOld[c.ID]; dup {
			return MergeResult{}, fmt.Errorf("import.credentials[%d]: duplicate id %q", i, c.ID)
		}
		credByOld[c.ID] = c
		credOrder = append(credOrder, c.ID)
	}

	jumpByOld := make(map[string]model.SavedJumpHost, len(p.SavedJumpHosts))
	for i, jh := range p.SavedJumpHosts {
		if jh.ID == "" {
			return MergeResult{}, fmt.Errorf("import.savedJumpHosts[%d]: missing id", i)
		}
		if err := jh.Validate(); err != nil {
			return MergeResult{}, fmt.Errorf("import.savedJumpHosts[%d] (%q): %w", i, jh.Name, err)
		}
		if _, dup := jumpByOld[jh.ID]; dup {
			return MergeResult{}, fmt.Errorf("import.savedJumpHosts[%d]: duplicate id %q", i, jh.ID)
		}
		jumpByOld[jh.ID] = jh
	}

	folderByOld := make(map[string]model.Folder, len(p.Folders))
	for i, f := range p.Folders {
		if f.ID == "" {
			return MergeResult{}, fmt.Errorf("import.folders[%d]: missing id", i)
		}
		if err := f.Validate(); err != nil {
			return MergeResult{}, fmt.Errorf("import.folders[%d] (%q): %w", i, f.Name, err)
		}
		if _, dup := folderByOld[f.ID]; dup {
			return MergeResult{}, fmt.Errorf("import.folders[%d]: duplicate id %q", i, f.ID)
		}
		folderByOld[f.ID] = f
	}

	sessionByOld := make(map[string]model.Session, len(p.Sessions))
	for i, sess := range p.Sessions {
		if sess.ID == "" {
			return MergeResult{}, fmt.Errorf("import.sessions[%d]: missing id", i)
		}
		if err := sess.Validate(); err != nil {
			return MergeResult{}, fmt.Errorf("import.sessions[%d] (%q): %w", i, sess.Name, err)
		}
		if _, dup := folderByOld[sess.ID]; dup {
			return MergeResult{}, fmt.Errorf("import.sessions[%d]: duplicate id %q", i, sess.ID)
		}
		if _, dup := sessionByOld[sess.ID]; dup {
			return MergeResult{}, fmt.Errorf("import.sessions[%d]: duplicate id %q", i, sess.ID)
		}
		sessionByOld[sess.ID] = sess
	}

	if len(credByOld) == 0 && len(jumpByOld) == 0 && len(folderByOld) == 0 && len(sessionByOld) == 0 {
		return MergeResult{}, nil // empty payload: no-op
	}

	// Dangling references abort the whole import: app-generated exports never
	// contain them (the store soft-nulls references on delete).
	for _, sess := range p.Sessions {
		if sess.CredentialID != "" {
			if _, ok := credByOld[sess.CredentialID]; !ok {
				return MergeResult{}, fmt.Errorf("import.session %q: references unknown credential %q", sess.Name, sess.CredentialID)
			}
		}
		if sess.JumpHostRef != "" {
			if _, ok := jumpByOld[sess.JumpHostRef]; !ok {
				return MergeResult{}, fmt.Errorf("import.session %q: references unknown saved jump host %q", sess.Name, sess.JumpHostRef)
			}
		}
	}

	// --- rebuild the order graph (shared with Load, so they cannot drift) --
	order := buildOrder(p, folderByOld, sessionByOld)

	// --- build the ID remap and rewrite every reference -------------------
	idMap := make(map[string]string, len(credByOld)+len(jumpByOld)+len(folderByOld)+len(sessionByOld))
	for id := range credByOld {
		idMap[id] = model.NewID()
	}
	for id := range jumpByOld {
		idMap[id] = model.NewID()
	}
	for id := range folderByOld {
		idMap[id] = model.NewID()
	}
	for id := range sessionByOld {
		idMap[id] = model.NewID()
	}

	// New-ID space of the order graph.
	orderNew := make(map[string][]string, len(order))
	for oldParent, ids := range order {
		newParent := ""
		if oldParent != "" {
			newParent = idMap[oldParent]
		}
		list := make([]string, 0, len(ids))
		for _, oldChild := range ids {
			list = append(list, idMap[oldChild])
		}
		orderNew[newParent] = list
	}

	folders := make(map[string]model.Folder, len(folderByOld))
	for oldID, f := range folderByOld {
		newID := idMap[oldID]
		f.ID = newID
		f.ParentID = ""
		f.Children = append([]string(nil), orderNew[newID]...)
		folders[newID] = f
	}
	sessions := make(map[string]model.Session, len(sessionByOld))
	remappedRefs := 0
	for oldID, sess := range sessionByOld {
		if sess.CredentialID != "" || sess.JumpHostRef != "" {
			remappedRefs++
		}
		sess.ID = idMap[oldID]
		if sess.CredentialID != "" {
			sess.CredentialID = idMap[sess.CredentialID]
		}
		if sess.JumpHostRef != "" {
			sess.JumpHostRef = idMap[sess.JumpHostRef]
		}
		sess.FolderID = ""
		sessions[sess.ID] = sess
	}
	// Membership fields follow the order graph (single source of truth).
	for parent, ids := range orderNew {
		for _, id := range ids {
			if f, ok := folders[id]; ok {
				f.ParentID = parent
				folders[id] = f
			}
			if sess, ok := sessions[id]; ok {
				sess.FolderID = parent
				sessions[id] = sess
			}
		}
	}

	// --- wrap the imported top level in one new folder ---------------------
	newRootID := ""
	if len(folderByOld) > 0 || len(sessionByOld) > 0 {
		if err := (&model.Folder{Name: rootFolderName}).Validate(); err != nil {
			return MergeResult{}, fmt.Errorf("import: invalid import folder name: %w", err)
		}
		newRootID = model.NewID()
		folders[newRootID] = model.Folder{
			ID:       newRootID,
			ParentID: "",
			Name:     rootFolderName,
			Children: append([]string(nil), orderNew[""]...),
		}
		for _, id := range orderNew[""] {
			if f, ok := folders[id]; ok {
				f.ParentID = newRootID
				folders[id] = f
			}
			if sess, ok := sessions[id]; ok {
				sess.FolderID = newRootID
				sessions[id] = sess
			}
		}
	}

	newCreds := make([]model.Credential, 0, len(credByOld))
	for _, oldID := range credOrder {
		c := credByOld[oldID]
		c.ID = idMap[oldID]
		newCreds = append(newCreds, c)
	}
	newJumps := make([]model.SavedJumpHost, 0, len(jumpByOld))
	for oldID, jh := range jumpByOld {
		jh.ID = idMap[oldID]
		newJumps = append(newJumps, jh)
	}

	// --- apply in one shot (nothing above mutated the store) --------------
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range folders {
		s.folders[f.ID] = f
		s.order[f.ID] = append([]string(nil), f.Children...)
	}
	for _, sess := range sessions {
		s.sessions[sess.ID] = sess
		s.searchIdx[sess.ID] = indexSession(sess)
	}
	for _, c := range newCreds {
		s.credentials[c.ID] = c
	}
	for _, jh := range newJumps {
		s.savedJumpHosts[jh.ID] = jh
	}
	if newRootID != "" {
		s.order[""] = append(s.order[""], newRootID)
	}
	s.scheduleSave()

	return MergeResult{
		Folders:            len(folderByOld),
		Sessions:           len(sessionByOld),
		Credentials:        len(credByOld),
		SavedJumpHosts:     len(jumpByOld),
		RemappedReferences: remappedRefs,
	}, nil
}
