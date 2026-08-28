// Package wailsvc contains the Wails-facing services — the only package
// besides main.go that may import the Wails API (master plan §5, §11).
// Later phases add VaultService, SessionService, TerminalService,
// SftpService and (JSON-only) DTOs in dto.go.
package wailsvc

// AppService exposes app-level metadata to the frontend.
// Bound as "AppService" in the generated JS/TS bindings.
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
