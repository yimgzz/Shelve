package wailsvc

import (
	"shelve/internal/monitor"
	"shelve/internal/vault"
)

// MonitorService exposes the per-tab system monitor to the frontend (plan
// P004). Only the ACTIVE tab is monitored at a time: the frontend calls
// Start when the active tab turns ready and Stop when it switches/closes or
// the monitoring setting is disabled. The monitor runs over a DEDICATED SSH
// connection to the tab's final hop (engine DialMonitorClient) — fully
// independent of the terminal's PTY channel, so metric execs can never
// stall terminal output; commands are read-only.
type MonitorService struct {
	vault *vault.Vault
	mon   *monitor.Manager
}

// NewMonitorService wires the monitoring service.
func NewMonitorService(v *vault.Vault, mon *monitor.Manager) *MonitorService {
	return &MonitorService{vault: v, mon: mon}
}

// Start begins periodic metric collection for a tab (idempotent; restarts
// reset the delta baselines). Gated by the vault state — a locked vault has
// no live connections to monitor.
func (s *MonitorService) Start(tabID string) error {
	if !s.vault.IsUnlocked() {
		return vault.ErrLocked
	}
	return s.mon.Start(tabID)
}

// Stop ends periodic metric collection for a tab. Safe and idempotent for an
// unknown or never-started tab. Deliberately NOT vault-gated: it is pure
// cleanup invoked on tab close/switch/lock, and a locked vault has no live
// connections anyway (the engine teardown hook already stopped the tickers).
func (s *MonitorService) Stop(tabID string) error {
	s.mon.Stop(tabID)
	return nil
}
