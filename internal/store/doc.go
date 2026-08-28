// Package store implements the in-memory session-tree CRUD, ordering and
// move/delete semantics, and triggers vault persistence on mutation
// (master plan §4): a single sync.RWMutex guards folders/sessions maps
// and per-parent ordered ID lists; mutations schedule a 300 ms debounced
// save through an injected SaveFunc, and Flush writes synchronously.
package store
