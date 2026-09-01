// Package wailsvc contains the Wails-facing services — the only package
// besides main.go that may import the Wails API (master plan §5, §11).
// Phase 2 adds VaultService, SessionService and the settings surface of
// AppService; TerminalService and SftpService land in Phases 3/5.
//
// Services are plain structs: the Wails runtime is wired in main.go
// through an Emitter callback and service registration, so every service
// stays callable and testable headlessly (master plan Phase 2 goal). The
// one exception is AppService.PickFile, which opens the NATIVE file
// dialog through the Wails runtime directly (a modal dialog has no
// headless equivalent; §11 permits the Wails import in this package).
package wailsvc

import (
	"errors"
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"

	"shelve/internal/config"
)

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

// ErrPickerUnavailable is returned by PickFile when the Wails runtime (or
// its dialog manager) is not ready — e.g. the method was invoked during
// startup before the runtime was created.
var ErrPickerUnavailable = errors.New("file picker is unavailable")

// PickFile opens the native OS file chooser and returns the FULL path of
// the selected file; an empty string means the user cancelled the dialog.
//
// This is the authoritative source for SSH key paths: WebKitGTK file
// inputs expose only the file's basename (the non-standard File.path is
// not provided by the webview), which would break relative-to-CWD key
// lookups with "ssh key file not found: <basename>".
func (s *AppService) PickFile() (string, error) {
	a := application.Get()
	if a == nil || a.Dialog == nil {
		return "", ErrPickerUnavailable
	}
	dlg := a.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		Title:          "Select an SSH private key",
		CanChooseFiles: true,
	})
	path, err := dlg.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("pick file: %w", err)
	}
	return path, nil
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
