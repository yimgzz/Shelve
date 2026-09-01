// Package sshengine is the SSH session engine (master plan §5): one
// Manager owns every live terminal tab. Phase 3c provides the dial
// chain (structured jump hosts + Extra Args ProxyJump), the PTY session,
// the batched output pump, the host-key / key-passphrase prompt flows,
// the tab state machine with status/exit events, and Write/Resize/
// Disconnect/Reconnect/Shutdown. Local port forwards (ssh:forward) and
// TestConnection land in Phase 3d.
//
// The engine must NOT import internal/wailsvc: wailsvc imports the
// engine and adapts its Emitter to the Wails runtime (master plan §11).
package sshengine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
	"shelve/internal/sshx"
	"shelve/internal/sshx/knownhosts"
)

// Event names (master plan §5, Go→JS). The single source of truth for
// the engine's side of the event contract; the payload structs' JSON
// field names match §5 verbatim.
const (
	EventTerminalStatus     = "terminal:status"
	EventTerminalData       = "terminal:data"
	EventTerminalExit       = "terminal:exit"
	EventVaultHostkeyPrompt = "vault:hostkey-prompt"
	EventVaultKeyPrompt     = "vault:key-prompt"
	EventForward            = "ssh:forward"
	EventAppToast           = "app:toast"
)

// Tab states carried by EventTerminalStatus (master plan §5).
const (
	StateConnecting = "connecting"
	StateReady      = "ready"
	StateError      = "error"
	StateClosed     = "closed"
)

// Port-forward lifecycle states carried by ForwardPayload.State
// (master plan §5).
const (
	ForwardListening = "listening"
	ForwardClosed    = "closed"
	ForwardFailed    = "failed"
)

// Toast levels carried by ToastPayload.Level (master plan §5).
const (
	ToastInfo  = "info"
	ToastError = "error"
)

// TerminalStatusPayload is the payload of EventTerminalStatus.
type TerminalStatusPayload struct {
	TabID   string `json:"tabID"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// TerminalDataPayload is the payload of EventTerminalData. Data is
// base64 output batched per master plan §2 A6 (≤50 ms / ≤16 KB).
type TerminalDataPayload struct {
	TabID string `json:"tabID"`
	Data  string `json:"data"`
}

// TerminalExitPayload is the payload of EventTerminalExit. ExitStatus
// is set only when the remote shell reported an exit status.
type TerminalExitPayload struct {
	TabID      string `json:"tabID"`
	ExitStatus *int   `json:"exitStatus,omitempty"`
}

// HostKeyPromptPayload is the payload of EventVaultHostkeyPrompt: an
// unknown host key escalated through TOFU (master plan A1).
type HostKeyPromptPayload struct {
	ConnID      string `json:"connID"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	KeyType     string `json:"keyType"`
	KeyB64      string `json:"keyB64"`
	Fingerprint string `json:"fingerprint"`
}

// KeyPromptPayload is the payload of EventVaultKeyPrompt: an encrypted
// SSH key file that needs its passphrase (master plan A2).
type KeyPromptPayload struct {
	ConnID  string `json:"connID"`
	KeyPath string `json:"keyPath"`
}

// ForwardPayload is the payload of EventForward: the lifecycle of one
// local port forward (-L/-D) on a tab (master plan §5). State is one of
// ForwardListening | ForwardClosed | ForwardFailed. LocalAddr is set for
// a successful bind; Error carries a bind failure message.
type ForwardPayload struct {
	TabID     string `json:"tabID"`
	Spec      string `json:"spec"`
	State     string `json:"state"`
	LocalAddr string `json:"localAddr,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ToastPayload is the payload of EventAppToast: a transient UI toast
// (master plan §5). Level is ToastInfo or ToastError.
type ToastPayload struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Emitter delivers Go→JS events. Structurally satisfied by the
// wailsvc emitter types; the engine never imports wailsvc (dependency
// direction, master plan §11). Implementations must be safe for
// concurrent use and must not block: they are called from engine
// goroutines.
type Emitter interface {
	Emit(event string, payload any)
}

// TerminalDataSink receives raw terminal output bytes instead of the
// terminal:data event (plan P005). Set once at startup by the composition
// root when a dedicated transport is wired; nil keeps the legacy emitter
// path (headless tests unchanged). It MAY block — blocking is the intended
// transport flow control that propagates backpressure to the pump.
type TerminalDataSink interface {
	OnTerminalData(tabID string, data []byte)
}

// Typed errors (master plan phase 3c). Error messages never contain
// passwords or key material (master plan §8.3).
var (
	// ErrUnknownTab: the tabID is not a known tab record.
	ErrUnknownTab = errors.New("sshengine: unknown tab")
	// ErrTabNotReady: Write on a tab that is not in the ready state.
	ErrTabNotReady = errors.New("sshengine: tab not ready")
	// ErrNoPendingPrompt: Approve/Reject/Submit called with no pending
	// prompt (or a prompt of a different kind) for that connID.
	ErrNoPendingPrompt = errors.New("sshengine: no pending prompt")
)

const (
	// DefaultPromptTimeout is the default prompt wait (phase 3c); tests
	// override it. A timed-out key-passphrase prompt fails the tab; a
	// timed-out host-key prompt rejects (sshx host-key contract).
	DefaultPromptTimeout = 120 * time.Second
	// defaultDialTimeout applies when Extra Args sets no ConnectTimeout
	// (or sets 0).
	defaultDialTimeout = 10 * time.Second
)

// Manager is the sshengine.SessionManager (master plan §5): it holds
// every live tab record and the process-memory key-passphrase cache
// (master plan A2: prompted once, cached for the app session). All
// fields are guarded by mu; user-facing calls (emitting, closing
// connections) are never made while holding it.
type Manager struct {
	mu          sync.Mutex
	conns       map[string]*liveConn   // tabID → record
	prompts     map[string]*promptSlot // connID → pending prompt
	passphrases map[string]string      // keyPath → passphrase (A2 cache)
	emit        Emitter
	// dataSink is the plan P005 raw terminal-output transport (nil: the
	// pump emits terminal:data events through emit instead).
	dataSink TerminalDataSink
	kh       *knownhosts.Manager
	// PromptTimeout bounds a pending prompt; zero disables the
	// host-key prompt timer. Default DefaultPromptTimeout.
	PromptTimeout time.Duration
	// onTabClosed is an optional hook invoked after a tab's resources are
	// torn down (Disconnect / remote exit / Shutdown). The engine stays
	// SFTP-agnostic (master plan §5): a co-resident SFTP manager registers
	// here so it can close its per-tab client, which rides on the same
	// final-hop SSH connection that teardown is about to release.
	onTabClosed func(tabID string)
}

// New creates a Manager over the app-managed known_hosts manager.
func New(emit Emitter, kh *knownhosts.Manager) *Manager {
	return &Manager{
		conns:         map[string]*liveConn{},
		prompts:       map[string]*promptSlot{},
		passphrases:   map[string]string{},
		emit:          emit,
		kh:            kh,
		PromptTimeout: DefaultPromptTimeout,
	}
}

// SetDataSink installs the raw terminal-output transport (plan P005); nil
// restores the terminal:data emitter path.
func (m *Manager) SetDataSink(s TerminalDataSink) {
	m.mu.Lock()
	m.dataSink = s
	m.mu.Unlock()
}

// TabInfo is a snapshot of one tab record (state introspection).
type TabInfo struct {
	TabID   string
	State   string // StateConnecting | StateReady | StateError | StateClosed
	Message string
}

// Tabs returns a deterministic snapshot of all tab records (sorted by
// TabID). The record is kept after dial failure and shell exit
// (master plan A4); only Disconnect/Shutdown remove it.
func (m *Manager) Tabs() []TabInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TabInfo, 0, len(m.conns))
	for id, l := range m.conns {
		out = append(out, TabInfo{TabID: id, State: l.state.String(), Message: l.message})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TabID < out[j].TabID })
	return out
}

// Connect starts a terminal session for a stored session: it registers
// a new tab record (ULID tabID, master plan A10), emits the initial
// "connecting" status and returns immediately; the dial runs in the
// per-tab goroutine and any failure is surfaced through the record
// (state "error" + message), keeping the tab for Retry/Close
// (master plan A4). The only errors returned are programming errors
// (nil session).
func (m *Manager) Connect(session *model.Session) (string, error) {
	if session == nil {
		return "", errors.New("sshengine: nil session")
	}
	tabID := model.NewID()
	m.startConnect(tabID, *session)
	return tabID, nil
}

// startConnect registers a fresh tab record under tabID and starts its
// run goroutine. The "connecting" status is emitted BEFORE the dial
// goroutine starts so the event order is deterministic.
func (m *Manager) startConnect(tabID string, sess model.Session) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &liveConn{
		m:       m,
		tabID:   tabID,
		connID:  tabID, // prompts are keyed by connID; Connect uses the tabID
		session: sess,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	m.mu.Lock()
	m.conns[tabID] = l
	m.mu.Unlock()
	m.emitStatus(tabID, StateConnecting, "")
	go l.run()
}

// Write decodes dataB64 and feeds the tab's pty stdin. Typed
// ErrUnknownTab / ErrTabNotReady (connecting, error, closed).
func (m *Manager) Write(tabID, dataB64 string) error {
	data, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("sshengine: invalid base64 terminal input: %v", err)
	}
	return m.WriteRaw(tabID, data)
}

// WriteRaw feeds raw bytes to the tab's pty stdin (plan P005: the termws
// transport calls this directly, bypassing base64 over IPC).
func (m *Manager) WriteRaw(tabID string, data []byte) error {
	l := m.getTab(tabID)
	if l == nil {
		return ErrUnknownTab
	}
	m.mu.Lock()
	st, in := l.state, l.ptyIn
	m.mu.Unlock()
	if st != stateReady {
		return fmt.Errorf("%w: tab is %s", ErrTabNotReady, st)
	}
	if _, err := in.Write(data); err != nil {
		return fmt.Errorf("sshengine: write to terminal: %v", err)
	}
	return nil
}

// Resize applies WindowChange to the tab's pty. No-op (nil) when the
// tab is not ready; typed ErrUnknownTab for an unknown tab.
func (m *Manager) Resize(tabID string, cols, rows int) error {
	l := m.getTab(tabID)
	if l == nil {
		return ErrUnknownTab
	}
	m.mu.Lock()
	st, pty := l.state, l.pty
	m.mu.Unlock()
	if st != stateReady {
		return nil
	}
	return pty.WindowChange(rows, cols)
}

// Disconnect closes a tab: pty session first, then the chain clients
// last→first (master plan §5), emits terminal:status "closed" and
// removes the record. Unknown tab → typed ErrUnknownTab.
func (m *Manager) Disconnect(tabID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), disconnectWait)
	defer cancel()
	return m.disconnect(tabID, ctx)
}

// shutdownWait bounds each tab's teardown wait inside Shutdown; the
// caller's ctx (master plan: 3 s for Lock) is the overall budget.
const disconnectWait = 10 * time.Second

func (m *Manager) disconnect(tabID string, ctx context.Context) error {
	l := m.getTab(tabID)
	if l == nil {
		return ErrUnknownTab
	}
	m.mu.Lock()
	l.disconnected = true
	m.abortConnPrompt(l)
	m.mu.Unlock()
	l.cancel()
	m.teardown(l)
	select {
	case <-l.done:
	case <-ctx.Done():
		// Budget exhausted: the cancellation and socket closes are
		// already issued, the tab record is removed anyway.
	}
	m.mu.Lock()
	st := l.state
	delete(m.conns, tabID)
	m.mu.Unlock()
	if st != stateClosed {
		// finishExit already emitted "closed" for a racing shell exit.
		m.emitStatus(tabID, StateClosed, "")
	}
	return nil
}

// Reconnect re-dials a tab from its stored session under the same tabID
// (master plan A4 Retry button). Allowed from error and closed records
// (and ready: disconnect + redial). A tab still connecting is rejected
// with ErrTabNotReady. Prompts may re-appear; already-accepted host
// keys and cached passphrases skip theirs.
func (m *Manager) Reconnect(tabID string) error {
	m.mu.Lock()
	l, ok := m.conns[tabID]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownTab
	}
	st := l.state
	if st == stateConnecting {
		m.mu.Unlock()
		return fmt.Errorf("%w: tab is connecting", ErrTabNotReady)
	}
	sess := l.session
	m.mu.Unlock()

	switch st {
	case stateReady:
		ctx, cancel := context.WithTimeout(context.Background(), disconnectWait)
		_ = m.disconnect(tabID, ctx) // teardown + "closed" event + record removal
		cancel()
	default: // stateError, stateClosed: the run goroutine already (or is about to) exit
		l.cancel()
		select {
		case <-l.done:
		case <-time.After(disconnectWait):
		}
		m.mu.Lock()
		delete(m.conns, tabID)
		m.mu.Unlock()
	}
	m.startConnect(tabID, sess) // re-emits "connecting"
	return nil
}

// Shutdown disconnects every open tab (master plan §5: Lock and app
// exit). It is bounded by ctx — each tab's teardown wait uses the
// remaining budget — and is idempotent. The key-passphrase cache is
// zeroized as part of the Lock/exit flow.
func (m *Manager) Shutdown(ctx context.Context) {
	for _, id := range m.tabIDs() {
		_ = m.disconnect(id, ctx)
	}
	m.clearPassphrases()
}

func (m *Manager) tabIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) getTab(tabID string) *liveConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conns[tabID]
}

// OnTabClosed registers a hook invoked after a tab's resources are torn
// down (Disconnect / remote exit / Shutdown). The hook receives the
// tabID; the engine stays SFTP-agnostic (master plan §5) — the caller
// closes whatever co-resident client (e.g. an SFTP client) rides on the
// tab's final-hop SSH connection. Passing nil clears the hook. At most
// one hook is held.
func (m *Manager) OnTabClosed(f func(tabID string)) {
	m.mu.Lock()
	m.onTabClosed = f
	m.mu.Unlock()
}

// SSHClient returns the FINAL-hop *ssh.Client of a ready tab, which SFTP
// needs to open its subsystem channel (master plan §5: the SFTP client
// reuses the active session's connection). Typed errors: ErrUnknownTab
// for an unknown/removed tab, ErrTabNotReady while it is connecting, in
// error state, or closed.
func (m *Manager) SSHClient(tabID string) (*ssh.Client, error) {
	l := m.getTab(tabID)
	if l == nil {
		return nil, ErrUnknownTab
	}
	m.mu.Lock()
	st := l.state
	clients := l.clients
	m.mu.Unlock()
	if st != stateReady || len(clients) == 0 {
		return nil, fmt.Errorf("%w: tab is %s", ErrTabNotReady, st)
	}
	return clients[len(clients)-1], nil
}

func (m *Manager) emitStatus(tabID, state, message string) {
	m.emit.Emit(EventTerminalStatus, TerminalStatusPayload{TabID: tabID, State: state, Message: message})
}

// ------------------------------------------------------------------
// Host-key prompts (TOFU, master plan A1) — the engine side of
// sshx.HostKeyApprover, bound per connID.
// ------------------------------------------------------------------

// HostKeyCallback returns an ssh.HostKeyCallback for dials under
// connID: known keys pass silently, changed keys hard-fail, unknown
// keys emit vault:hostkey-prompt for connID and resolve through
// ApproveHostKey / RejectHostKey (or the PromptTimeout rejection).
// Phase 3d's TestConnection reuses this with an ephemeral connID.
func (m *Manager) HostKeyCallback(connID string) ssh.HostKeyCallback {
	return sshx.NewHostKeyCallback(m.kh, &connApprover{m: m, connID: connID})
}

// connApprover is a per-connection sshx.HostKeyApprover.
type connApprover struct {
	m      *Manager
	connID string
}

func (a *connApprover) PromptHostKey(host string, port int, keyType, keyB64, fingerprint string) (<-chan bool, error) {
	return a.m.beginHostKeyPrompt(a.connID, host, port, keyType, keyB64, fingerprint), nil
}

type promptKind int

const (
	promptHostKey promptKind = iota
	promptKeyPassphrase
)

// promptSlot is one in-flight prompt, keyed by connID: at most one
// pending prompt per connID (master plan phase 3c).
type promptSlot struct {
	kind  promptKind
	timer *time.Timer // host-key timeout; nil for key-passphrase prompts
	// The resolver sends on exactly one channel (buffered 1: the
	// resolver never blocks). The host-key channel is consumed by the
	// sshx host-key callback; the key-passphrase channel by the dial.
	chBool chan bool
	chStr  chan string
}

func (m *Manager) beginHostKeyPrompt(connID, host string, port int, keyType, keyB64, fingerprint string) chan bool {
	m.mu.Lock()
	// A conn dials sequentially, so a live slot here is a race left
	// over from a torn-down dial: resolve it (rejection) and replace.
	if old, ok := m.prompts[connID]; ok {
		delete(m.prompts, connID)
		if old.timer != nil {
			old.timer.Stop()
		}
		if old.chBool != nil {
			select {
			case old.chBool <- false:
			default:
			}
		}
	}
	ch := make(chan bool, 1)
	slot := &promptSlot{kind: promptHostKey, chBool: ch}
	if t := m.PromptTimeout; t > 0 {
		slot.timer = time.AfterFunc(t, func() {
			// Timeout = rejection (sshx host-key contract).
			_ = m.resolvePrompt(connID, promptHostKey, func(s *promptSlot) { s.chBool <- false })
		})
	}
	m.prompts[connID] = slot
	m.mu.Unlock()
	m.emit.Emit(EventVaultHostkeyPrompt, HostKeyPromptPayload{
		ConnID:      connID,
		Host:        host,
		Port:        port,
		KeyType:     keyType,
		KeyB64:      keyB64,
		Fingerprint: fingerprint,
	})
	return ch
}

func (m *Manager) beginKeyPrompt(connID, keyPath string) (chan string, *promptSlot) {
	m.mu.Lock()
	ch := make(chan string, 1)
	slot := &promptSlot{kind: promptKeyPassphrase, chStr: ch}
	m.prompts[connID] = slot
	m.mu.Unlock()
	m.emit.Emit(EventVaultKeyPrompt, KeyPromptPayload{ConnID: connID, KeyPath: keyPath})
	return ch, slot
}

// resolvePrompt resolves the pending prompt of kind `want` for connID,
// identity-checked so a stale resolver (timer firing after the user
// already decided, a re-applied prompt) is a no-op: it removes the
// slot, stops the timer and applies the resolution (a non-blocking
// buffered send). No slot, or a different kind → ErrNoPendingPrompt.
func (m *Manager) resolvePrompt(connID string, want promptKind, apply func(*promptSlot)) error {
	m.mu.Lock()
	slot, ok := m.prompts[connID]
	if !ok || slot.kind != want {
		m.mu.Unlock()
		return ErrNoPendingPrompt
	}
	delete(m.prompts, connID)
	if slot.timer != nil {
		slot.timer.Stop()
	}
	m.mu.Unlock()
	apply(slot)
	return nil
}

// ApproveHostKey accepts the pending host-key prompt for connID
// (vault:hostkey-prompt → accept-and-connect).
func (m *Manager) ApproveHostKey(connID string) error {
	return m.resolvePrompt(connID, promptHostKey, func(s *promptSlot) { s.chBool <- true })
}

// RejectHostKey rejects the pending host-key prompt for connID.
func (m *Manager) RejectHostKey(connID string) error {
	return m.resolvePrompt(connID, promptHostKey, func(s *promptSlot) { s.chBool <- false })
}

// SubmitKeyPassphrase resolves the pending key-passphrase prompt for
// connID with the entered passphrase. The passphrase is handed to the
// dial, cached in the A2 process-memory cache on success, and never
// logged (master plan §8.3).
func (m *Manager) SubmitKeyPassphrase(connID, passphrase string) error {
	return m.resolvePrompt(connID, promptKeyPassphrase, func(s *promptSlot) { s.chStr <- passphrase })
}

// dropHostKeyPrompt removes a host-key slot a finished chain left
// behind. Every prompt resolution (Approve/Reject/timeout/abort)
// already removes its slot, so this is a defensive invariant: after a
// chain ends, no prompt slot may remain for its connID.
func (m *Manager) dropHostKeyPrompt(connID string) {
	m.mu.Lock()
	slot, ok := m.prompts[connID]
	if ok && slot.kind == promptHostKey {
		delete(m.prompts, connID)
		if slot.timer != nil {
			slot.timer.Stop()
		}
	}
	m.mu.Unlock()
}

// discardPrompt removes a pending slot if it is still the current one
// for connID (the dial timed out or was aborted).
func (m *Manager) discardPrompt(connID string, slot *promptSlot) {
	m.mu.Lock()
	if cur, ok := m.prompts[connID]; ok && cur == slot {
		delete(m.prompts, connID)
	}
	m.mu.Unlock()
}

// abortConnPrompt resolves whatever prompt (any kind) is pending for a
// tearing-down conn, so a handshake blocked in the host-key callback
// unblocks (master plan: Lock/Disconnect while a prompt is open).
// Must be called with m.mu held.
func (m *Manager) abortConnPrompt(l *liveConn) {
	slot, ok := m.prompts[l.connID]
	if !ok {
		return
	}
	delete(m.prompts, l.connID)
	if slot.timer != nil {
		slot.timer.Stop()
	}
	if slot.chBool != nil {
		select {
		case slot.chBool <- false:
		default:
		}
	}
}

// ------------------------------------------------------------------
// Key-file passphrase cache (master plan A2): prompt once, keep in
// process memory for the app session; zeroized by Shutdown (Lock/exit).
// ------------------------------------------------------------------

func (m *Manager) passphraseFor(auth model.Auth) *string {
	if auth.Type != model.AuthKey || auth.KeyPath == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pw, ok := m.passphrases[auth.KeyPath]; ok {
		return &pw
	}
	return nil
}

func (m *Manager) cachePassphrase(keyPath, passphrase string) {
	m.mu.Lock()
	m.passphrases[keyPath] = passphrase
	m.mu.Unlock()
}

func (m *Manager) clearPassphrases() {
	m.mu.Lock()
	for k := range m.passphrases {
		delete(m.passphrases, k)
	}
	m.mu.Unlock()
}

func (m *Manager) promptTimeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.PromptTimeout
}
