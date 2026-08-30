// Package sftp provides SFTP operations over an active engine connection
// (list, mkdir, rename, remove, get/put) and the temp-file "edit text file
// with system editor" flow (master plan §2 D4, A5: data streams in Go,
// only paths cross IPC).
//
// Phase 5a implements the per-tab client manager and the browse operations
// (List/Mkdir/Rename/Remove) plus TextLike classification. Upload/download/
// edit land in Phase 5b. The engine stays SFTP-agnostic: the Manager talks
// to it through the TabProvider interface (satisfied structurally by
// sshengine.Manager), so this package never imports the engine.
package sftp

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"golang.org/x/crypto/ssh"
)

// TabProvider exposes an active tab's final-hop SSH client. sshengine.Manager
// satisfies it structurally (master plan §5); sftp must not import the engine.
type TabProvider interface {
	SSHClient(tabID string) (*ssh.Client, error)
}

// Emitter is a minimal Go→JS event sink (master plan §5). Structurally
// satisfied by the wailsvc emitter; implementations must be safe for
// concurrent use and must not block. Phase 5a emits nothing yet but the
// manager keeps the reference for the Phase 5b progress events.
type Emitter interface {
	Emit(event string, payload any)
}

// Typed errors. Messages never contain credentials or key material
// (master plan §8.3).
var (
	// ErrNoProvider: no TabProvider has been attached yet.
	ErrNoProvider = errors.New("sftp: no tab provider attached")
	// ErrNotActive: the tab has no live SFTP client (not connected or
	// already torn down).
	ErrNotActive = errors.New("sftp: tab not active")
	// ErrDirNotEmpty: Remove was asked to delete a non-empty directory.
	ErrDirNotEmpty = errors.New("sftp: directory not empty")
)

// Entry is one listing row (master plan §5). IsDir/Size/ModTime mirror the
// remote stat; TextLike flags files that are safe to open in the text editor
// (known text extension AND ≤ MaxTextSize bytes).
type Entry struct {
	Name     string
	IsDir    bool
	Size     int64
	ModTime  time.Time
	TextLike bool
}

// MaxTextSize is the upper bound for a TextLike file (2 MiB, master plan
// §6 SFTP panel: edit if text-like ≤ 2 MiB).
const MaxTextSize int64 = 2 << 20 // 2 MiB

// textExtensions is the case-insensitive whitelist for TextLike (master
// plan phase 5a task 3).
var textExtensions = map[string]bool{
	"txt": true, "md": true, "sh": true, "yml": true, "yaml": true,
	"json": true, "toml": true, "ini": true, "conf": true, "cfg": true,
	"env": true, "js": true, "ts": true, "css": true, "html": true,
	"go": true, "py": true, "c": true, "h": true, "cpp": true, "hpp": true,
}

// Manager lazily creates one sftp.Client per active tab (cached under the
// tabID), remembers each tab's remote home directory (for ~ resolution), and
// closes its clients when the tab dies or on CloseAll. All fields are
// guarded by mu. Phase 5b adds the per-tab transfer queue (transferChans),
// the remote-text-editor state machine (edits) and the temp-file sweep.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*sftp.Client // tabID → cached client
	homes   map[string]string       // tabID → remote home (Getwd)
	tmpDir  string                  // config tmp dir for Phase 5b edit temps
	emit    Emitter
	prov    TabProvider

	transfersMu   sync.Mutex                   // guards transferChans
	transferChans map[string]chan *transferJob // tabID → FIFO worker queue

	editsMu sync.Mutex            // guards edits
	edits   map[string]*editState // tabID → live text-editor state

	editPoll      time.Duration // watcher poll interval (1 s default)
	editStability time.Duration // mtime-stable window before save (3 s default)
}

// New creates an empty Manager. tmpDir is the config-directory tmp/ path
// (master plan §4, §8.8); emit is the Go→JS event sink. Attach must be
// called before any tab operation.
func New(tmpDir string, emit Emitter) *Manager {
	return &Manager{
		clients:       map[string]*sftp.Client{},
		homes:         map[string]string{},
		tmpDir:        tmpDir,
		emit:          emit,
		transferChans: map[string]chan *transferJob{},
		edits:         map[string]*editState{},
		editPoll:      time.Second,
		editStability: 3 * time.Second,
	}
}

// Attach wires the TabProvider (the engine). Idempotent: a later call
// replaces the provider.
func (m *Manager) Attach(p TabProvider) {
	m.mu.Lock()
	m.prov = p
	m.mu.Unlock()
}

// HandleTabClosed is the OnTabClosed hook registered on the engine: it
// releases the tab's cached SFTP client once the underlying SSH connection
// is gone. Idempotent and safe for a never-created tab. This is what the
// app wiring registers as engine.OnTabClosed (master plan phase 5a task 5).
func (m *Manager) HandleTabClosed(tabID string) {
	m.mu.Lock()
	c := m.clients[tabID]
	delete(m.clients, tabID)
	delete(m.homes, tabID)
	m.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (m *Manager) provider() TabProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prov
}

// ClientFor returns the cached SFTP client for tabID, lazily creating it on
// first use via sftp.NewClient over the provider's final-hop SSH client
// (master plan §2 A5: reuse the active connection). The remote home is
// captured via Getwd on first use (for ~ resolution). Errors are typed:
// ErrNoProvider when unattached, or whatever the provider reports
// (ErrUnknownTab / ErrTabNotReady from the engine).
func (m *Manager) ClientFor(tabID string) (*sftp.Client, error) {
	m.mu.Lock()
	if c, ok := m.clients[tabID]; ok {
		m.mu.Unlock()
		return c, nil
	}
	prov := m.prov
	m.mu.Unlock()
	if prov == nil {
		return nil, ErrNoProvider
	}
	sshClient, err := prov.SSHClient(tabID)
	if err != nil {
		return nil, err
	}
	c, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("sftp: connect: %w", err)
	}
	home, err := c.Getwd()
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("sftp: getwd: %w", err)
	}
	// A concurrent ClientFor/HandleTabClosed could have raced us into the
	// cache (or removed the tab); prefer the cached one and release ours.
	m.mu.Lock()
	if existing, ok := m.clients[tabID]; ok {
		m.mu.Unlock()
		_ = c.Close()
		return existing, nil
	}
	m.clients[tabID] = c
	m.homes[tabID] = home
	m.mu.Unlock()
	return c, nil
}

// IsActive reports whether tabID currently has a usable SFTP client: either
// already cached, or the provider reports a ready connection. It never
// creates a client (side-effect free, cheap — used by SftpService.IsActive).
func (m *Manager) IsActive(tabID string) bool {
	m.mu.Lock()
	if _, ok := m.clients[tabID]; ok {
		m.mu.Unlock()
		return true
	}
	prov := m.prov
	m.mu.Unlock()
	if prov == nil {
		return false
	}
	_, err := prov.SSHClient(tabID)
	return err == nil
}

// home returns the tab's remote home directory (resolving it via ClientFor
// on first use so the map is populated).
func (m *Manager) home(tabID string) (string, error) {
	m.mu.Lock()
	if h, ok := m.homes[tabID]; ok {
		m.mu.Unlock()
		return h, nil
	}
	m.mu.Unlock()
	if _, err := m.ClientFor(tabID); err != nil {
		return "", err
	}
	m.mu.Lock()
	h, ok := m.homes[tabID]
	m.mu.Unlock()
	if !ok {
		return "", ErrNotActive
	}
	return h, nil
}

// resolve maps a user-entered path to a POSIX remote path (master plan
// phase 5a task 3): "" and "~" → the remote home; a leading "~/" expands
// the home prefix; everything else is passed through path.Clean unchanged
// (absolute paths stay absolute, relative paths stay relative to the remote
// cwd). A leading "~" is honored only at position 0. ".." traversal is
// ALLOWED — the remote OS user's permissions govern (master plan §2 D4);
// the frontend only ever receives entries returned by these calls (§2 A5).
func (m *Manager) resolve(tabID, userPath string) (string, error) {
	switch {
	case userPath == "" || userPath == "~":
		return m.home(tabID)
	case strings.HasPrefix(userPath, "~/"):
		home, err := m.home(tabID)
		if err != nil {
			return "", err
		}
		return path.Clean(path.Join(home, userPath[2:])), nil
	default:
		return path.Clean(userPath), nil
	}
}

// List returns the sorted contents of the directory at userPath (master
// plan phase 5a task 3): directories first, then case-insensitive name
// sort. Self/parent entries ("." and "..") are never present (the server
// omits them).
func (m *Manager) List(tabID, userPath string) ([]Entry, error) {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return nil, err
	}
	abs, err := m.resolve(tabID, userPath)
	if err != nil {
		return nil, err
	}
	infos, err := c.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("sftp: list %s: %w", abs, err)
	}
	out := make([]Entry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, Entry{
			Name:     fi.Name(),
			IsDir:    fi.IsDir(),
			Size:     fi.Size(),
			ModTime:  fi.ModTime(),
			TextLike: m.TextLike(fi.Name(), fi.Size()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // directories first
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Mkdir creates the single directory at userPath (no recursive parents;
// a missing parent fails on the server, master plan phase 5a task 3).
func (m *Manager) Mkdir(tabID, userPath string) error {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return err
	}
	abs, err := m.resolve(tabID, userPath)
	if err != nil {
		return err
	}
	if err := c.Mkdir(abs); err != nil {
		return fmt.Errorf("sftp: mkdir %s: %w", abs, err)
	}
	return nil
}

// Rename moves/renames from→to (master plan phase 5a task 3). Whether an
// existing target is overwritten depends on the remote server (POSIX
// rename semantics); callers should treat success as "the target now
// exists" and not assume the source still does.
func (m *Manager) Rename(tabID, from, to string) error {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return err
	}
	src, err := m.resolve(tabID, from)
	if err != nil {
		return err
	}
	dst, err := m.resolve(tabID, to)
	if err != nil {
		return err
	}
	if err := c.Rename(src, dst); err != nil {
		return fmt.Errorf("sftp: rename %s → %s: %w", src, dst, err)
	}
	return nil
}

// Remove deletes the file or EMPTY directory at userPath. A non-empty
// directory maps to ErrDirNotEmpty (master plan phase 5a task 3).
func (m *Manager) Remove(tabID, userPath string) error {
	c, err := m.ClientFor(tabID)
	if err != nil {
		return err
	}
	abs, err := m.resolve(tabID, userPath)
	if err != nil {
		return err
	}
	st, err := c.Stat(abs)
	if err != nil {
		return fmt.Errorf("sftp: stat %s: %w", abs, err)
	}
	if st.IsDir() {
		children, err := c.ReadDir(abs)
		if err != nil {
			return fmt.Errorf("sftp: readdir %s: %w", abs, err)
		}
		if len(children) > 0 {
			return fmt.Errorf("%w: %s", ErrDirNotEmpty, abs)
		}
		if err := c.RemoveDirectory(abs); err != nil {
			return fmt.Errorf("sftp: rmdir %s: %w", abs, err)
		}
		return nil
	}
	if err := c.Remove(abs); err != nil {
		return fmt.Errorf("sftp: remove %s: %w", abs, err)
	}
	return nil
}

// TextLike classifies a remote file as text-editable: it must have a
// whitelisted extension (case-insensitive, lowercased) and a size in
// [0, MaxTextSize]. A file with no extension is never TextLike (master
// plan phase 5a task 3). A pure function; safe to call concurrently.
func (m *Manager) TextLike(name string, size int64) bool {
	if size < 0 || size > MaxTextSize {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	return ext != "" && textExtensions[ext]
}

// CloseAll closes every cached SFTP client (idempotent). Used on app exit;
// per-tab teardown normally happens through HandleTabClosed.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*sftp.Client{}
	m.homes = map[string]string{}
	m.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}
