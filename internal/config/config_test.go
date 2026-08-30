package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// isolatedXDG points the config dir at a fresh temp dir and returns it.
func isolatedXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestPathUsesXDGConfigHome(t *testing.T) {
	dir := isolatedXDG(t)
	want := filepath.Join(dir, DirName)
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestPathFallsBackToDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", DirName)
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestEnsureDirCreatesDir0700(t *testing.T) {
	isolatedXDG(t)
	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != DirPerm {
		t.Fatalf("dir perms = %o, want %o", fi.Mode().Perm(), DirPerm)
	}
	// Re-running must be idempotent.
	if err := EnsureDir(); err != nil {
		t.Fatalf("second EnsureDir: %v", err)
	}
}

func TestWriteFileCreates0600Content(t *testing.T) {
	isolatedXDG(t)
	if err := WriteFile(SettingsFileName, []byte(`{"theme":"dark"}`)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := os.Stat(File(SettingsFileName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != FilePerm {
		t.Fatalf("file perms = %o, want %o", fi.Mode().Perm(), FilePerm)
	}
	data, err := os.ReadFile(File(SettingsFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != `{"theme":"dark"}` {
		t.Fatalf("content = %q", data)
	}
}

func TestWriteFileAtomicRenameFailureLeavesStateIntact(t *testing.T) {
	isolatedXDG(t)
	dir := t.TempDir()
	// Make the target a non-empty directory: the final rename must fail
	// (ENOTEMPTY). Note: permission bits are not a usable failure
	// mechanism in this test — CI containers run as root, which ignores
	// them.
	target := filepath.Join(dir, "f")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "inner"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(dir, "f", []byte("new")); err == nil {
		t.Fatal("expected rename to fail when target is a non-empty dir")
	}
	// The previous target is untouched...
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Fatalf("target changed on failed write: %v %v", fi, err)
	}
	// ...and no temp file leaked.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "f" {
			t.Fatalf("leaked entry %q", e.Name())
		}
	}
}

func TestWriteFileAtomicNoTornWrites(t *testing.T) {
	isolatedXDG(t)
	name := "torn"
	payloads := make([][]byte, 16)
	var wg sync.WaitGroup
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("payload-%03d-padding-padding-padding-%03d", i, i))
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			for range 25 {
				if err := WriteFile(name, p); err != nil {
					t.Error(err)
					return
				}
			}
		}(payloads[i])
	}
	wg.Wait()
	data, err := os.ReadFile(File(name))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if bytes.Equal(data, p) {
			return // final file is exactly one whole payload: atomic
		}
	}
	t.Fatalf("torn write: content matches no full payload (%d bytes)", len(data))
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	isolatedXDG(t)
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := DefaultSettings()
	if s != want {
		t.Fatalf("Load() = %+v, want defaults %+v", s, want)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	isolatedXDG(t)
	s := DefaultSettings()
	s.Theme = ThemeDark
	s.AutoLockMinutes = 15
	s.SftpBrowserEnabled = true
	s.Terminal.FontSize = 14
	s.Window.Width = 1440
	s.Window.Height = 900
	s.Window.LeftWidth = 380
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != s {
		t.Fatalf("round trip = %+v, want %+v", got, s)
	}
}

func TestLoadJSONMatchesMasterSchema(t *testing.T) {
	isolatedXDG(t)
	raw := `{
	  "theme": "dark",
	  "autoLockMinutes": 0,
	  "sftpBrowserEnabled": false,
	  "terminal": { "fontFamily": "monospace", "fontSize": 13, "scrollback": 10000 },
	  "textEditorCommand": "xdg-open",
	  "window": { "width": 1280, "height": 800 }
	}`
	if err := os.MkdirAll(Path(), DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(SettingsFileName), []byte(raw), FilePerm); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Theme != ThemeDark || got.Terminal.FontSize != 13 || got.Window.Width != 1280 {
		t.Fatalf("unexpected settings: %+v", got)
	}
}

func TestLoadNormalizesUnknownThemeAndZeros(t *testing.T) {
	isolatedXDG(t)
	raw := `{"theme":"neon","terminal":{"fontSize":0,"scrollback":0},"window":{"width":0,"height":0,"leftWidth":0}}`
	if err := os.MkdirAll(Path(), DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(SettingsFileName), []byte(raw), FilePerm); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want %q", got.Theme, ThemeSystem)
	}
	d := DefaultSettings()
	if got.Terminal.FontSize != d.Terminal.FontSize || got.Terminal.Scrollback != d.Terminal.Scrollback ||
		got.Window.Width != d.Window.Width || got.Window.Height != d.Window.Height ||
		got.Window.LeftWidth != d.Window.LeftWidth {
		t.Fatalf("zeros not normalized: %+v", got)
	}
}

// TestLoadDefaultsLeftWidthZeroToDefault covers the Phase 4a rule: a
// missing/zero window.leftWidth is treated as the 320 px default.
func TestLoadDefaultsLeftWidthZeroToDefault(t *testing.T) {
	isolatedXDG(t)
	raw := `{"window":{"width":1000,"height":700}}` // leftWidth omitted → 0
	if err := os.MkdirAll(Path(), DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(SettingsFileName), []byte(raw), FilePerm); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Window.LeftWidth != 320 {
		t.Fatalf("leftWidth = %d, want default 320", got.Window.LeftWidth)
	}
}

func TestLoadCorruptFileIsErrorNotDefault(t *testing.T) {
	isolatedXDG(t)
	if err := os.MkdirAll(Path(), DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(SettingsFileName), []byte("{not json"), FilePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected error for corrupt settings.json")
	}
}

func TestSaveFileIs0600(t *testing.T) {
	isolatedXDG(t)
	if err := DefaultSettings().Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(File(SettingsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != FilePerm {
		t.Fatalf("perms = %o, want %o", fi.Mode().Perm(), FilePerm)
	}
}

func TestSettingsNeverContainsSecretFields(t *testing.T) {
	// Master plan §8.1 gate: settings.json must not be able to carry
	// credential material. Assert the serialized shape has no such keys.
	data, err := json.Marshal(DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for k := range m {
		switch k {
		case "theme", "autoLockMinutes", "sftpBrowserEnabled", "terminal",
			"textEditorCommand", "window":
		default:
			t.Fatalf("unexpected settings key %q", k)
		}
	}
}
