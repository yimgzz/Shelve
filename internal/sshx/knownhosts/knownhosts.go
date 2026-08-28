// Package knownhosts implements the app-managed OpenSSH known_hosts
// subset (master plan §2 A1, §4, §8.5): a line parser, host lookup,
// TOFU-style add with mismatch detection, clean-line atomic save and
// SHA256 key fingerprints in the ssh-keygen display form.
//
// The supported subset is: plain hostnames/IPs (default port 22) and
// [host]:port, with any key type token (ssh-ed25519, ssh-rsa, ...).
// Out-of-subset lines (comments, blank lines, hashed hosts,
// @cert-authority/@revoked markers, wildcard and multi-host hostspecs)
// are recognized but inert: they are kept neither in the in-memory map
// nor on write-back.
package knownhosts

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"dummy-ssh-manager/internal/config"
)

const defaultPort = 22

// ErrKeyConflict is returned by Add when a different key is presented for
// a (host, port, keyType) that already has one stored: a host-key change
// must be surfaced to the user (TOFU, master plan §8.5), never silently
// overwritten.
var ErrKeyConflict = errors.New("knownhosts: host key changed for existing entry")

// Entry is one parsed known_hosts line in the supported subset.
type Entry struct {
	Host    string
	Port    int
	KeyType string
	Key     string // base64 public key
}

// Manager is an in-memory view of the app's known_hosts file.
// All methods are safe for concurrent use.
type Manager struct {
	mu      sync.Mutex
	path    string
	entries []Entry
	index   map[string]int // entryKey -> position in entries
}

// New opens (or starts empty) the known_hosts file at path.
func New(path string) (*Manager, error) {
	m := &Manager{path: path, index: map[string]int{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if e, ok := ParseLine(line); ok {
			m.addIngest(e)
		}
	}
	return nil
}

// addIngest stores e under the load invariant (no concurrent access):
// first occurrence wins, later duplicates of the same
// (host, port, keyType) are dropped.
func (m *Manager) addIngest(e Entry) {
	k := entryKey(e.Host, e.Port, e.KeyType)
	if _, ok := m.index[k]; ok {
		return
	}
	m.index[k] = len(m.entries)
	m.entries = append(m.entries, e)
}

// ParseLine parses one known_hosts line. ok is false for blank lines,
// comments and out-of-subset lines, which are recognized but inert.
func ParseLine(line string) (Entry, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return Entry{}, false
	}
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return Entry{}, false
	}
	hostspec, keyType, key := parts[0], parts[1], parts[2]
	if strings.HasPrefix(hostspec, "@") {
		return Entry{}, false // marker line (@cert-authority, @revoked, ...)
	}
	if strings.HasPrefix(hostspec, "[") {
		return parseBracketed(hostspec, keyType, key) // [host]:port form
	}
	if strings.ContainsAny(hostspec, "*?,|") {
		return Entry{}, false // wildcard, multi-host list or hashed host
	}
	return Entry{Host: hostspec, Port: defaultPort, KeyType: keyType, Key: key}, true
}

// parseBracketed parses the [host]:port hostspec form.
func parseBracketed(hostspec, keyType, key string) (Entry, bool) {
	if hostspec[0] != '[' {
		return Entry{}, false
	}
	i := strings.Index(hostspec, "]:")
	if i < 0 {
		return Entry{}, false // unclosed bracket
	}
	host := hostspec[1:i]
	portStr := hostspec[i+2:]
	if host == "" || portStr == "" {
		return Entry{}, false
	}
	port, err := strconv.Atoi(portStr) // a stray ']' or non-digit fails here
	if err != nil || port < 1 || port > 65535 {
		return Entry{}, false
	}
	return Entry{Host: host, Port: port, KeyType: keyType, Key: key}, true
}

// Lookup returns all entries stored for host:port. An entry written
// without an explicit port matches the default port 22 only
// (OpenSSH semantics); host matching is case-insensitive.
func (m *Manager) Lookup(host string, port int) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := strings.ToLower(host)
	var out []Entry
	for _, e := range m.entries {
		if strings.ToLower(e.Host) == h && e.Port == port {
			out = append(out, e)
		}
	}
	return out
}

// Has reports whether the exact (host, port, keyType, key) entry is
// stored. The host-key callback (Phase 3) uses it for TOFU mismatch
// detection.
func (m *Manager) Has(host string, port int, keyType, key string) bool {
	for _, e := range m.Lookup(host, port) {
		if strings.EqualFold(e.KeyType, keyType) && e.Key == key {
			return true
		}
	}
	return false
}

// Add stores a host key. Re-adding the identical entry is a no-op;
// adding a different key for the same (host, port, keyType) returns
// ErrKeyConflict.
func (m *Manager) Add(host string, port int, keyType, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := entryKey(host, port, keyType)
	if i, ok := m.index[k]; ok {
		if m.entries[i].Key == key {
			return nil // idempotent
		}
		return fmt.Errorf("%w: %s:%d (%s)", ErrKeyConflict, host, port, keyType)
	}
	m.index[k] = len(m.entries)
	m.entries = append(m.entries, Entry{Host: host, Port: port, KeyType: keyType, Key: key})
	return nil
}

// Save rewrites the file with the clean subset lines only, atomically,
// 0600 (master plan §4).
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, e := range m.entries {
		b.WriteString(entryLine(e))
		b.WriteByte('\n')
	}
	return config.WriteFileAtomic(filepath.Dir(m.path), filepath.Base(m.path), []byte(b.String()))
}

func entryLine(e Entry) string {
	host := e.Host
	if e.Port != defaultPort {
		host = "[" + host + "]:" + strconv.Itoa(e.Port)
	}
	return host + " " + e.KeyType + " " + e.Key
}

// Fingerprint returns the OpenSSH display form "SHA256:<b64>" of a
// base64-encoded public key (standard base64 without padding, as printed
// by `ssh-keygen -lf`).
func Fingerprint(keyB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", fmt.Errorf("knownhosts: invalid base64 key: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "="), nil
}

func entryKey(host string, port int, keyType string) string {
	return strings.ToLower(host) + "|" + strconv.Itoa(port) + "|" + strings.ToLower(keyType)
}
