// Package config manages the on-disk configuration layout under
// $XDG_CONFIG_HOME/dummy-ssh-manager (master plan §4): directories 0700,
// files 0600, all writes atomic (temp file + rename).
package config

import (
	"os"
	"path/filepath"
)

const (
	// DirName is the app's XDG config directory name.
	DirName = "dummy-ssh-manager"
	// SettingsFileName holds unencrypted user settings.
	SettingsFileName = "settings.json"
	// VaultFileName holds the encrypted credential envelope (Phase 2).
	VaultFileName = "vault.json"
	// KnownHostsFileName holds the app-managed OpenSSH known_hosts subset (Phase 3).
	KnownHostsFileName = "known_hosts"

	// DirPerm is the permission mode for the config directory.
	DirPerm = 0o700
	// FilePerm is the permission mode for config files.
	FilePerm = 0o600
)

// Path returns the app's config directory:
// $XDG_CONFIG_HOME/dummy-ssh-manager, falling back to ~/.config/dummy-ssh-manager.
func Path() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, DirName)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", DirName)
	}
	return ".config/" + DirName
}

// EnsureDir creates the config directory (and parents) with 0700.
// It also re-asserts 0700 on an existing directory.
func EnsureDir() error {
	if err := os.MkdirAll(Path(), DirPerm); err != nil {
		return err
	}
	return os.Chmod(Path(), DirPerm)
}

// File returns the absolute path of a file inside the config directory.
func File(name string) string {
	return filepath.Join(Path(), name)
}

// WriteFile writes data to <configdir>/name atomically with 0600,
// creating the directory if missing. See WriteFileAtomic.
func WriteFile(name string, data []byte) error {
	return WriteFileAtomic(Path(), name, data)
}

// WriteFileAtomic writes data to dir/name via a temp file in dir + rename,
// mode 0600. The target is never truncated in place: on any failure the
// previous file is left untouched (master plan §8.7).
func WriteFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(FilePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	return os.Chmod(dir, DirPerm)
}

// ReadFile reads <configdir>/name.
func ReadFile(name string) ([]byte, error) {
	return os.ReadFile(File(name))
}

// EnsureFile creates <configdir>/name with the given content (0600) if it
// does not exist yet; existing files are left untouched.
func EnsureFile(name string, data []byte) error {
	if _, err := os.Stat(File(name)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return WriteFile(name, data)
}
