// Package monitor provides per-tab remote system monitoring (plan P004): it
// periodically executes a read-only POSIX-shell script over a DEDICATED SSH
// connection to the active tab's final hop and emits `monitor:metrics`
// snapshots (hostname, CPU%, RAM, net speeds, uptime, df) for the bottom
// status bar.
//
// The dedicated connection (dialed once per Start via the structural Dialer,
// satisfied by sshengine.Manager.DialMonitorClient) is fully independent of
// the tab's PTY channel, so monitor execs can never contend with terminal
// output — a 2 s exec on the live connection previously stalled `tail -f`
// style output for up to seconds (P004 fix).
//
// Layering follows master plan §5: the Manager talks to the engine only
// through the structural Dialer interface and never imports the service or
// bridge packages. Events flow through an Emitter wired by the composition
// root. Only the ACTIVE tab is monitored — the frontend drives lifecycle via
// Start/Stop (plan P004 D2).
package monitor

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Event names (plan P004, Go→JS). The single source of truth for the
// monitor's side of the event contract; the payload struct's JSON field
// names match the frontend shape verbatim.
const EventMonitorMetrics = "monitor:metrics"

// MetricsPayload is the payload of EventMonitorMetrics: one snapshot of the
// remote host for one tab. CPU percent and net speeds are deltas between the
// current and the previous sample (0 on the first sample after Start).
// Memory and disk values are raw numbers; the frontend formats them
// (MB/GB/TB, percentages) — Go sends data, not presentation.
type MetricsPayload struct {
	TabID         string  `json:"tabID"`
	Hostname      string  `json:"hostname"`
	CPUPercent    float64 `json:"cpuPercent"`
	MemUsedBytes  uint64  `json:"memUsedBytes"`
	MemTotalBytes uint64  `json:"memTotalBytes"`
	NetUpBps      uint64  `json:"netUpBps"`
	NetDownBps    uint64  `json:"netDownBps"`
	UptimeSeconds uint64  `json:"uptimeSeconds"`
	DiskUsedPct   float64 `json:"diskUsedPct"`
	DiskRoot      string  `json:"diskRoot"`
	DfText        string  `json:"dfText"`
}

// Sampling knobs (plan P004 D4): fixed 2 s tick, 5 s per-exec timeout, and
// a consecutive-failure ceiling after which the ticker stops itself
// (defensive — the tab is likely dead and the frontend restarts on the next
// ready state).
const (
	DefaultInterval        = 2 * time.Second
	DefaultExecTimeout     = 5 * time.Second
	maxConsecutiveFailures = 5
)

// Dialer opens a DEDICATED SSH connection chain for a tab's monitoring.
// sshengine.Manager satisfies it structurally (DialMonitorClient); monitor
// must not import the engine (master plan §5). All returned clients must be
// closed together when monitoring stops.
type Dialer interface {
	// DialMonitorClient dials a NEW connection to the tab's final hop with
	// the same auth/host-key/key-passphrase paths as the live tab, but no
	// PTY. Returns every hop client (last = final hop), fully independent
	// of the tab's terminal channel.
	DialMonitorClient(tabID string) ([]*ssh.Client, error)
}

// Emitter is a minimal backend→renderer event sink (master plan §5).
// Structurally satisfied by the bridge emitter; implementations must be safe
// for concurrent use and must not block — Emit is called from tick
// goroutines.
type Emitter interface {
	Emit(event string, payload any)
}

// Typed errors. Messages never contain credentials or key material
// (master plan §8.3).
var (
	// ErrNoProvider: no Dialer has been attached yet.
	ErrNoProvider = errors.New("monitor: no dialer attached")
)

// execFn runs the collection script on the client and returns the raw
// combined output. Injected in tests (an SSH server is not needed); the
// production default is execScript.
type execFn func(ctx context.Context, client *ssh.Client, script string) ([]byte, error)

// Manager owns the per-tab ticker goroutines and their dedicated SSH
// connections. All fields except emit are guarded by mu; emit is set at
// construction and immutable afterwards. execFn/Interval/ExecTimeout are
// test knobs and must be set before Start.
type Manager struct {
	mu      sync.Mutex
	dial    Dialer
	emit    Emitter
	runs    map[string]context.CancelFunc // tabID → cancel of its tick goroutine
	prev    map[string]*rawSample         // tabID → previous sample (delta baseline)
	clients map[string][]*ssh.Client      // tabID → dedicated hop clients (last = target)

	Interval    time.Duration
	ExecTimeout time.Duration
	exec        execFn
}

// New creates an empty Manager over the given event sink.
func New(emit Emitter) *Manager {
	return &Manager{
		emit:        emit,
		runs:        map[string]context.CancelFunc{},
		prev:        map[string]*rawSample{},
		clients:     map[string][]*ssh.Client{},
		Interval:    DefaultInterval,
		ExecTimeout: DefaultExecTimeout,
		exec:        execScript,
	}
}

// Attach wires the Dialer (the engine). Idempotent: a later call replaces
// the dialer.
func (m *Manager) Attach(d Dialer) {
	m.mu.Lock()
	m.dial = d
	m.mu.Unlock()
}

// Start begins periodic metric collection for tabID (plan P004 D2): it
// opens the tab's DEDICATED monitoring connection (propagating engine
// ErrUnknownTab/ErrTabNotReady and dial/auth errors), cancels any existing
// run for the tab (restart resets the delta baselines), and spawns the tick
// goroutine. Idempotent.
func (m *Manager) Start(tabID string) error {
	d := m.dialer()
	if d == nil {
		return ErrNoProvider
	}
	clients, err := d.DialMonitorClient(tabID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	old := m.clients[tabID]
	if cancel, ok := m.runs[tabID]; ok {
		cancel()
	}
	m.clients[tabID] = clients
	delete(m.prev, tabID)
	ctx, cancel := context.WithCancel(context.Background())
	m.runs[tabID] = cancel
	m.mu.Unlock()
	// Release the previous dedicated connection. The old tick goroutine
	// sees its canceled ctx (tick checks ctx before counting failures) and
	// exits without touching the new run.
	if len(old) > 0 {
		go closeClients(old)
	}
	go m.tick(ctx, tabID)
	return nil
}

// Stop cancels the tick goroutine for tabID, drops its state and closes the
// dedicated connection. Safe and idempotent for an unknown or never-started
// tab.
func (m *Manager) Stop(tabID string) {
	m.mu.Lock()
	cancel, ok := m.runs[tabID]
	delete(m.runs, tabID)
	delete(m.prev, tabID)
	cs := m.clients[tabID]
	delete(m.clients, tabID)
	m.mu.Unlock()
	if ok {
		cancel()
	}
	closeClients(cs)
}

// HandleTabClosed is the OnTabClosed hook registered on the engine: it stops
// any running ticker for the tab once its SSH connection is gone.
// Idempotent. This is what the app wiring calls from the composed
// OnTabClosed hook (plan P004 D3).
func (m *Manager) HandleTabClosed(tabID string) {
	m.Stop(tabID)
}

// CloseAll stops every ticker and closes every dedicated connection
// (app-shutdown belt-and-braces; per-tab teardown normally happens through
// HandleTabClosed). Idempotent.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.runs))
	all := make([][]*ssh.Client, 0, len(m.clients))
	for id, c := range m.runs {
		cancels = append(cancels, c)
		delete(m.runs, id)
		delete(m.prev, id)
	}
	for id, cs := range m.clients {
		all = append(all, cs)
		delete(m.clients, id)
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	for _, cs := range all {
		closeClients(cs)
	}
}

// tick is the per-tab sampling loop (plan P004 D5): an immediate first
// sample establishes the delta baseline, then one sample per Interval. A
// failed/malformed exec skips the tick (no emit); after maxConsecutiveFailures
// in a row the ticker stops itself. Emits happen without holding mu.
func (m *Manager) tick(ctx context.Context, tabID string) {
	interval := m.interval()
	timeout := m.execTimeout()
	failures := 0
	var (
		prev   *rawSample
		prevAt time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		out, err := m.runExec(execCtx, tabID, collectScript)
		cancel()
		switch {
		case err != nil:
			// A Stop/restart canceled ctx while the exec was in flight
			// (e.g. the old dedicated connection was released): exit
			// without counting a failure — never let a stale run kill a
			// newer one via the self-stop path.
			select {
			case <-ctx.Done():
				return
			default:
			}
			failures++
			if failures >= maxConsecutiveFailures {
				m.Stop(tabID)
				return
			}
		default:
			cur, perr := parseSample(out)
			if perr != nil {
				break // malformed output: skip this tick
			}
			failures = 0
			now := time.Now()
			p := buildPayload(tabID, cur, prev, now.Sub(prevAt))
			prev, prevAt = cur, now
			m.emit.Emit(EventMonitorMetrics, p)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// runExec runs the collection script over the tab's DEDICATED connection
// with the ticker's default execFn.
func (m *Manager) runExec(ctx context.Context, tabID, script string) ([]byte, error) {
	cs := m.clientsFor(tabID)
	if len(cs) == 0 {
		return nil, errors.New("monitor: dedicated connection missing")
	}
	return m.execFn()(ctx, cs[len(cs)-1], script)
}

func (m *Manager) dialer() Dialer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dial
}

func (m *Manager) clientsFor(tabID string) []*ssh.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clients[tabID]
}

// closeClients closes a dedicated hop chain last→first (target first).
// Guards nil entries (unit-test placeholder clients).
func closeClients(cs []*ssh.Client) {
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i] != nil {
			cs[i].Close()
		}
	}
}

func (m *Manager) execFn() execFn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exec != nil {
		return m.exec
	}
	return execScript
}

func (m *Manager) interval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Interval > 0 {
		return m.Interval
	}
	return DefaultInterval
}

func (m *Manager) execTimeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ExecTimeout > 0 {
		return m.ExecTimeout
	}
	return DefaultExecTimeout
}

// buildPayload maps the current sample (and the previous one for CPU/net
// deltas) into the frontend payload. With no previous sample (first tick
// after Start/restart) CPU% and net speeds are 0.
func buildPayload(tabID string, cur, prev *rawSample, elapsed time.Duration) MetricsPayload {
	p := MetricsPayload{
		TabID:         tabID,
		Hostname:      cur.Hostname,
		UptimeSeconds: cur.UptimeSec,
		DiskUsedPct:   cur.DiskUsedPct,
		DiskRoot:      cur.DiskRoot,
		DfText:        cur.DfText,
		MemTotalBytes: cur.MemTotal * 1024, // /proc/meminfo is in KiB
	}
	if cur.MemTotal >= cur.MemAvail {
		p.MemUsedBytes = (cur.MemTotal - cur.MemAvail) * 1024
	}
	if prev != nil && elapsed > 0 {
		p.CPUPercent = cpuPercent(prev, cur)
		p.NetUpBps, p.NetDownBps = netSpeeds(prev, cur, elapsed)
	}
	return p
}
