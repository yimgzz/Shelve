// Package monitor provides per-tab remote system monitoring (plan P004): it
// periodically executes a read-only POSIX-shell script over an active tab's
// final-hop SSH connection and emits `monitor:metrics` snapshots (hostname,
// CPU%, RAM, net speeds, uptime, df) for the bottom status bar.
//
// Layering follows master plan §5/§11: the Manager talks to the engine only
// through the structural TabProvider interface (satisfied by
// sshengine.Manager) and never imports wailsvc or the engine. Events flow
// through an Emitter wired by the composition root. Only the ACTIVE tab is
// monitored — the frontend drives lifecycle via Start/Stop (plan P004 D2).
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

// TabProvider exposes an active tab's final-hop SSH client. sshengine.Manager
// satisfies it structurally (master plan §5); monitor must not import the
// engine.
type TabProvider interface {
	SSHClient(tabID string) (*ssh.Client, error)
}

// Emitter is a minimal Go→JS event sink (master plan §5). Structurally
// satisfied by the wailsvc emitter; implementations must be safe for
// concurrent use and must not block — Emit is called from tick goroutines.
type Emitter interface {
	Emit(event string, payload any)
}

// Typed errors. Messages never contain credentials or key material
// (master plan §8.3).
var (
	// ErrNoProvider: no TabProvider has been attached yet.
	ErrNoProvider = errors.New("monitor: no tab provider attached")
)

// execFn runs the collection script on the client and returns the raw
// combined output. Injected in tests (an SSH server is not needed); the
// production default is execScript.
type execFn func(ctx context.Context, client *ssh.Client, script string) ([]byte, error)

// Manager owns the per-tab ticker goroutines. All fields except emit are
// guarded by mu; emit is set at construction and immutable afterwards.
// execFn/Interval/ExecTimeout are test knobs and must be set before Start.
type Manager struct {
	mu   sync.Mutex
	prov TabProvider
	emit Emitter
	runs map[string]context.CancelFunc // tabID → cancel of its tick goroutine
	prev map[string]*rawSample         // tabID → previous sample (delta baseline)

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
		Interval:    DefaultInterval,
		ExecTimeout: DefaultExecTimeout,
		exec:        execScript,
	}
}

// Attach wires the TabProvider (the engine). Idempotent: a later call
// replaces the provider.
func (m *Manager) Attach(p TabProvider) {
	m.mu.Lock()
	m.prov = p
	m.mu.Unlock()
}

// Start begins periodic metric collection for tabID (plan P004 D2): it
// validates the tab through the provider (propagating engine
// ErrUnknownTab/ErrTabNotReady), cancels any existing run for the tab
// (restart resets the delta baselines), and spawns the tick goroutine.
// Idempotent.
func (m *Manager) Start(tabID string) error {
	prov := m.provider()
	if prov == nil {
		return ErrNoProvider
	}
	if _, err := prov.SSHClient(tabID); err != nil {
		return err
	}
	m.mu.Lock()
	if cancel, ok := m.runs[tabID]; ok {
		cancel()
	}
	delete(m.prev, tabID)
	ctx, cancel := context.WithCancel(context.Background())
	m.runs[tabID] = cancel
	m.mu.Unlock()
	go m.tick(ctx, tabID)
	return nil
}

// Stop cancels the tick goroutine for tabID and drops its state. Safe and
// idempotent for an unknown or never-started tab.
func (m *Manager) Stop(tabID string) {
	m.mu.Lock()
	cancel, ok := m.runs[tabID]
	delete(m.runs, tabID)
	delete(m.prev, tabID)
	m.mu.Unlock()
	if ok {
		cancel()
	}
}

// HandleTabClosed is the OnTabClosed hook registered on the engine: it stops
// any running ticker for the tab once its SSH connection is gone.
// Idempotent. This is what the app wiring calls from the composed
// OnTabClosed hook (plan P004 D3).
func (m *Manager) HandleTabClosed(tabID string) {
	m.Stop(tabID)
}

// CloseAll stops every ticker (app-shutdown belt-and-braces; per-tab
// teardown normally happens through HandleTabClosed). Idempotent.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.runs))
	for id, c := range m.runs {
		cancels = append(cancels, c)
		delete(m.runs, id)
		delete(m.prev, id)
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
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

// runExec resolves the tab's client through the provider and runs the
// collection script with the ticker's default execFn.
func (m *Manager) runExec(ctx context.Context, tabID, script string) ([]byte, error) {
	prov := m.provider()
	if prov == nil {
		return nil, ErrNoProvider
	}
	client, err := prov.SSHClient(tabID)
	if err != nil {
		return nil, err
	}
	return m.execFn()(ctx, client, script)
}

func (m *Manager) provider() TabProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prov
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
