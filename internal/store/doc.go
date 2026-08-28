// Package store implements the in-memory session-tree CRUD, ordering and
// move/delete semantics, and triggers vault persistence on mutation
// (master plan §4). Protected by a single sync.RWMutex.
//
// Populated in Phase 2.
package store
