// Package api is the transport-neutral service layer (master plan §5): plain
// structs with `(T, error)`-shaped methods and JSON DTOs, driven by whichever
// frontend is attached. It must NOT import a GUI toolkit — the loopback bridge
// (internal/bridge) reflects over these services to serve /rpc and implements
// the Emitter for server→client events. Native file dialogs do not belong in
// this process: the Electron main process owns them (E3), so AppService here
// exposes only metadata and settings.
package api

import (
	"shelve/internal/config"
)

// AppService exposes app-level metadata and user settings to the
// frontend. Bound as "AppService" in the RPC registry.
//
// The settings DTO is config.Settings itself: its JSON shape is the
// master plan §4 schema verbatim, and it is documented to never
// contain secrets.
type AppService struct {
	version string
}

// NewAppService creates the AppService bound to the given version string.
func NewAppService(version string) *AppService {
	return &AppService{version: version}
}

// GetVersion returns the application version.
func (s *AppService) GetVersion() string {
	return s.version
}

// GetSettings returns current settings (defaults when the file is
// missing; parse errors bubble up).
func (s *AppService) GetSettings() (config.Settings, error) {
	return config.Load()
}

// SaveSettings validates/normalizes and persists settings atomically.
func (s *AppService) SaveSettings(settings config.Settings) error {
	return settings.Save()
}
