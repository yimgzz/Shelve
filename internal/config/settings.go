package config

import (
	"encoding/json"
	"os"
	"strings"
)

// Theme selection values for Settings.Theme.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// TerminalSettings holds terminal appearance/behaviour settings.
type TerminalSettings struct {
	FontFamily string `json:"fontFamily"`
	FontSize   int    `json:"fontSize"`
	Scrollback int    `json:"scrollback"`
}

// WindowSettings holds the remembered main-window geometry (master plan A7).
// LeftWidth is the persisted left-panel width; 0 = not set → treated as 320.
// SftpWidth is the persisted SFTP right-panel width; 0 = not set → 320.
type WindowSettings struct {
	Width     int `json:"width"`
	Height    int `json:"height"`
	LeftWidth int `json:"leftWidth"`
	SftpWidth int `json:"sftpWidth"`
}

// Settings is the on-disk shape of settings.json (master plan §4).
// It must never contain secrets.
type Settings struct {
	Theme              string           `json:"theme"`        // "system" | "light" | "dark" — the mode
	ThemeVariant       string           `json:"themeVariant"` // "" = family default; concrete palette id
	AutoLockMinutes    int              `json:"autoLockMinutes"`
	SftpBrowserEnabled bool             `json:"sftpBrowserEnabled"`
	MonitoringEnabled  bool             `json:"monitoringEnabled"` // bottom-bar system monitor (plan P004)
	Terminal           TerminalSettings `json:"terminal"`
	TextEditorCommand  string           `json:"textEditorCommand"`
	SftpInitialPath    string           `json:"sftpInitialPath"` // global SFTP browser start path ("~" default)
	SftpOpenCommand    string           `json:"sftpOpenCommand"` // local handler for Open (xdg-open default)
	Window             WindowSettings   `json:"window"`
}

// DefaultSettings returns the schema defaults from master plan §4 (phase 5d
// D5d-4: the SFTP browser is on by default; an explicit `false` wins).
// Plan P004: the system monitor is ON by default too (same presence-aware
// rule — an explicit `false` wins, see Load).
func DefaultSettings() Settings {
	return Settings{
		Theme:              ThemeSystem,
		ThemeVariant:       "",
		AutoLockMinutes:    0,
		SftpBrowserEnabled: true,
		MonitoringEnabled:  true,
		Terminal: TerminalSettings{
			FontFamily: "monospace",
			FontSize:   13,
			Scrollback: 10000,
		},
		TextEditorCommand: "xdg-open",
		SftpInitialPath:   "~",
		SftpOpenCommand:   "xdg-open",
		Window: WindowSettings{
			Width:     1280,
			Height:    800,
			LeftWidth: 320,
			SftpWidth: 320,
		},
	}
}

// Load reads settings.json from the config directory. A missing file yields
// the defaults; unreadable or unparsable files return an error.
// Zero/unknown values are normalized to safe fallbacks.
//
// Presence-aware default for sftpBrowserEnabled (phase 5d D5d-4): a
// settings.json that never persisted the flag (fresh installs AND existing
// configs written before the flag existed) is treated as enabled. An
// explicit `false` the user wrote is respected.
func Load() (Settings, error) {
	s := DefaultSettings()
	data, err := os.ReadFile(File(SettingsFileName))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return s, err
	}
	if _, ok := keys["sftpBrowserEnabled"]; !ok {
		s.SftpBrowserEnabled = true
	}
	// Plan P004: same presence-aware rule for monitoringEnabled — a fresh
	// install or a pre-P004 settings.json (no key persisted) is treated as
	// enabled; an explicit `false` the user wrote is respected.
	if _, ok := keys["monitoringEnabled"]; !ok {
		s.MonitoringEnabled = true
	}
	s.normalize()
	return s, nil
}

// Save normalizes unknown/zero values and writes settings.json
// atomically (0600), creating the config directory if needed.
func (s Settings) Save() error {
	s.normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(SettingsFileName, data)
}

// normalize applies safe fallbacks for unknown or zero values.
func (s *Settings) normalize() {
	switch s.Theme {
	case ThemeLight, ThemeDark, ThemeSystem:
	default:
		s.Theme = ThemeSystem
	}
	// ThemeVariant: "" = the family default. The concrete palette ids live
	// only in the frontend catalog (frontend/src/ui/themes.ts); cross-family
	// mismatches are resolved there (master plan §4.1). Here we only trim.
	s.ThemeVariant = strings.TrimSpace(s.ThemeVariant)
	if s.TextEditorCommand == "" {
		s.TextEditorCommand = "xdg-open"
	}
	if s.SftpInitialPath == "" {
		s.SftpInitialPath = "~"
	}
	if s.SftpOpenCommand == "" {
		s.SftpOpenCommand = "xdg-open"
	}
	if s.Terminal.FontFamily == "" {
		s.Terminal.FontFamily = "monospace"
	}
	if s.Terminal.FontSize <= 0 {
		s.Terminal.FontSize = 13
	}
	if s.Terminal.Scrollback <= 0 {
		s.Terminal.Scrollback = 10000
	}
	if s.Window.Width <= 0 {
		s.Window.Width = 1280
	}
	if s.Window.Height <= 0 {
		s.Window.Height = 800
	}
	if s.Window.LeftWidth <= 0 {
		s.Window.LeftWidth = 320
	}
	if s.Window.SftpWidth <= 0 {
		s.Window.SftpWidth = 320
	}
}
