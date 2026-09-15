package api

import "sync"

// Event contract names (master plan §5, backend→frontend).
const (
	EventVaultStateChanged = "vault:state-changed"
)

// Emitter emits backend→frontend events. The transport (internal/bridge)
// implements it; service construction happens earlier (chicken-and-egg), so
// services hold a LateEmitter.
type Emitter interface {
	Emit(event string, payload any)
}

// FuncEmitter is an Emitter backed by a plain function.
type FuncEmitter func(event string, payload any)

// Emit implements Emitter.
func (f FuncEmitter) Emit(event string, payload any) { f(event, payload) }

// LateEmitter forwards to the Emitter set later (the bridge is constructed
// after the composition root). Events emitted before Set are dropped — that
// window is before any frontend has connected, so nothing UI-relevant is
// lost.
type LateEmitter struct {
	mu sync.RWMutex
	e  Emitter
}

// Emit implements Emitter (no-op until Set).
func (l *LateEmitter) Emit(event string, payload any) {
	l.mu.RLock()
	e := l.e
	l.mu.RUnlock()
	if e != nil {
		e.Emit(event, payload)
	}
}

// Set installs the real Emitter (called once from the entry point).
func (l *LateEmitter) Set(e Emitter) {
	l.mu.Lock()
	l.e = e
	l.mu.Unlock()
}

// VaultStatePayload is the payload of EventVaultStateChanged.
type VaultStatePayload struct {
	Unlocked bool `json:"unlocked"`
}
