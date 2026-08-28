package config

import (
	"encoding/json"
	"os"
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
type WindowSettings struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Settings is the on-disk shape of settings.json (master plan §4).
// It must never contain secrets.
type Settings struct {
	Theme              string           `json:"theme"` // "system" | "light" | "dark"
	AutoLockMinutes    int              `json:"autoLockMinutes"`
	SftpBrowserEnabled bool             `json:"sftpBrowserEnabled"`
	Terminal           TerminalSettings `json:"terminal"`
	TextEditorCommand  string           `json:"textEditorCommand"`
	Window             WindowSettings   `json:"window"`
}

// DefaultSettings returns the schema defaults from master plan §4.
func DefaultSettings() Settings {
	return Settings{
		Theme:              ThemeSystem,
		AutoLockMinutes:    0,
		SftpBrowserEnabled: false,
		Terminal: TerminalSettings{
			FontFamily: "monospace",
			FontSize:   13,
			Scrollback: 10000,
		},
		TextEditorCommand: "xdg-open",
		Window: WindowSettings{
			Width:  1280,
			Height: 800,
		},
	}
}

// Load reads settings.json from the config directory. A missing file yields
// the defaults; unreadable or unparsable files return an error.
// Zero/unknown values are normalized to safe fallbacks.
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
	if s.TextEditorCommand == "" {
		s.TextEditorCommand = "xdg-open"
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
}
