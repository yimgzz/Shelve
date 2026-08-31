// Package wailsvc contains the Wails-facing services — the only package
// besides main.go that may import the Wails API (master plan §5, §11).
// Phase 2 adds VaultService, SessionService and the settings surface of
// AppService; TerminalService and SftpService land in Phases 3/5.
//
// Services are plain structs (no Wails import in this package): the
// Wails runtime is wired in main.go through an Emitter callback and
// service registration, so every service stays callable and testable
// headlessly (master plan Phase 2 goal).
package wailsvc

import "shelve/internal/config"

// AppService exposes app-level metadata and user settings to the
// frontend. Bound as "AppService" in the generated JS/TS bindings.
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
