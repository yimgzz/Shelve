package monitor

// Unit tests for plan P004: parser, delta math, formatters and the Manager
// lifecycle (Start/Stop/HandleTabClosed/failure-stop) against a fake
// Dialer + injected execFn — no SSH server needed.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const sampleOut1 = `HN=vm-01
CP=100 50 30 800 10 5 3 0
MEM=8388608 6291456
NET=100000 200000
UP=86412
DFROOT=20971520 4194304 16777216 25% /
DFH_START
Filesystem      Size  Used Avail Use% Mounted on
overlay          20G  4.0G   16G  25% /
tmpfs            64M     0   64M   0% /dev
DFH_END
`

const sampleOut2 = `HN=vm-01
CP=150 80 60 950 20 10 6 1
MEM=8388608 5242880
NET=150000 250000
UP=86414
DFROOT=20971520 4194304 16777216 25% /
DFH_START
Filesystem      Size  Used Avail Use% Mounted on
overlay          20G  4.0G   16G  25% /
tmpfs            64M     0   64M   0% /dev
DFH_END
`

// fakeDialer returns a placeholder client slice for known tabIDs (execFn is
// injected, so the clients themselves are never used).
type fakeDialer struct {
	tabIDs map[string]bool
}

func (f *fakeDialer) DialMonitorClient(tabID string) ([]*ssh.Client, error) {
	if !f.tabIDs[tabID] {
		return nil, errors.New("monitor: unknown tab")
	}
	return []*ssh.Client{nil}, nil
}

// recEmitter records monitor:metrics payloads in order.
type recEmitter struct {
	mu     sync.Mutex
	events []MetricsPayload
}

func (r *recEmitter) Emit(event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event == EventMonitorMetrics {
		if p, ok := payload.(MetricsPayload); ok {
			r.events = append(r.events, p)
		}
	}
}

func (r *recEmitter) snapshot() []MetricsPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]MetricsPayload(nil), r.events...)
}

// newTestManager wires a Manager over a fake dialer with fast knobs.
func newTestManager(t *testing.T, d *fakeDialer, emit *recEmitter) *Manager {
	t.Helper()
	m := New(emit)
	m.Attach(d)
	m.Interval = 50 * time.Millisecond
	m.ExecTimeout = time.Second
	return m
}

// ---------------------------------------------------------------------------
// parseSample
// ---------------------------------------------------------------------------

func TestParseSampleFull(t *testing.T) {
	s, err := parseSample([]byte(sampleOut1))
	if err != nil {
		t.Fatalf("parseSample: %v", err)
	}
	if s.Hostname != "vm-01" {
		t.Fatalf("hostname = %q", s.Hostname)
	}
	wantCP := [8]uint64{100, 50, 30, 800, 10, 5, 3, 0}
	if s.CP != wantCP {
		t.Fatalf("CP = %v, want %v", s.CP, wantCP)
	}
	if s.MemTotal != 8388608 || s.MemAvail != 6291456 {
		t.Fatalf("mem = %d/%d", s.MemTotal, s.MemAvail)
	}
	if s.NetRx != 100000 || s.NetTx != 200000 {
		t.Fatalf("net = %d/%d", s.NetRx, s.NetTx)
	}
	if s.UptimeSec != 86412 {
		t.Fatalf("uptime = %d", s.UptimeSec)
	}
	if s.DiskTotal != 20971520 || s.DiskUsed != 4194304 || s.DiskUsedPct != 25 || s.DiskRoot != "/" {
		t.Fatalf("disk = %d/%d %v%% %q", s.DiskTotal, s.DiskUsed, s.DiskUsedPct, s.DiskRoot)
	}
	if !strings.Contains(s.DfText, "overlay") || !strings.Contains(s.DfText, "tmpfs") {
		t.Fatalf("dfText missing lines: %q", s.DfText)
	}
}

func TestParseSampleEmptyIsError(t *testing.T) {
	if _, err := parseSample(nil); err == nil {
		t.Fatal("empty output must be an error")
	}
}

func TestParseSamplePartialTolerated(t *testing.T) {
	// Only HN present: everything else stays zero, no error.
	s, err := parseSample([]byte("HN=only-host\n"))
	if err != nil {
		t.Fatalf("parseSample: %v", err)
	}
	if s.Hostname != "only-host" || s.DfText != "" {
		t.Fatalf("unexpected partial sample: %+v", s)
	}
}

func TestParseSampleMalformedCPUIsError(t *testing.T) {
	if _, err := parseSample([]byte("HN=x\nCP=not-a-number 1 2 3 4 5 6 7\n")); err == nil {
		t.Fatal("malformed CP line must be an error")
	}
}

// ---------------------------------------------------------------------------
// Delta math
// ---------------------------------------------------------------------------

func TestCpuPercent(t *testing.T) {
	p1, err := parseSample([]byte(sampleOut1))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := parseSample([]byte(sampleOut2))
	if err != nil {
		t.Fatal(err)
	}
	// busy1=198 idle1=800; busy2=327 idle2=950 → busyΔ=129 totalΔ=279.
	got := cpuPercent(p1, p2)
	if got < 46.0 || got > 46.5 {
		t.Fatalf("cpuPercent = %.2f, want ≈46.24", got)
	}
	if cpuPercent(p2, p2) != 0 {
		t.Fatal("identical samples must yield 0%")
	}
}

func TestNetSpeeds(t *testing.T) {
	p1, _ := parseSample([]byte(sampleOut1))
	p2, _ := parseSample([]byte(sampleOut2))
	up, down := netSpeeds(p1, p2, 100*time.Millisecond)
	// rx/tx Δ = 50 000 B per 100 ms → 500 000 B/s each.
	if up != 500000 || down != 500000 {
		t.Fatalf("speeds = %d/%d, want 500000/500000", up, down)
	}
	if u, d := netSpeeds(p1, p2, 0); u != 0 || d != 0 {
		t.Fatalf("zero elapsed must yield 0/0, got %d/%d", u, d)
	}
}

// ---------------------------------------------------------------------------
// Formatters
// ---------------------------------------------------------------------------

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{512 * 1024, "0.5 MB"},
		{1 << 20, "1.0 MB"},
		{1023 << 20, "1023.0 MB"},
		{1 << 30, "1.0 GB"},
		{1 << 40, "1.0 TB"},
	}
	for _, tc := range cases {
		if got := humanizeBytes(tc.in); got != tc.want {
			t.Fatalf("humanizeBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{45, "45s"},
		{34 * 60, "34m"},
		{2*3600 + 15*60, "2h 15m"},
		{3*86400 + 4*3600 + 12*60, "3d 4h 12m"},
	}
	for _, tc := range cases {
		if got := formatUptime(tc.in); got != tc.want {
			t.Fatalf("formatUptime(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Manager lifecycle
// ---------------------------------------------------------------------------

func TestManagerEmitsBaselineThenDeltas(t *testing.T) {
	emit := &recEmitter{}
	m := newTestManager(t, &fakeDialer{tabIDs: map[string]bool{"t1": true}}, emit)

	var calls int
	m.exec = func(context.Context, *ssh.Client, string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(sampleOut1), nil
		}
		return []byte(sampleOut2), nil
	}

	if err := m.Start("t1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("t1")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(emit.snapshot()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	evs := emit.snapshot()
	if len(evs) < 2 {
		t.Fatalf("got %d events, want ≥ 2", len(evs))
	}

	// Baseline event: hostname/mem/disk present, cpu/net zero.
	ev0 := evs[0]
	if ev0.Hostname != "vm-01" || ev0.CPUPercent != 0 || ev0.NetUpBps != 0 || ev0.NetDownBps != 0 {
		t.Fatalf("baseline event = %+v", ev0)
	}
	if ev0.MemTotalBytes != 8388608*1024 || ev0.DiskUsedPct != 25 {
		t.Fatalf("baseline mem/disk = %+v", ev0)
	}

	// Second event: deltas computed.
	ev1 := evs[1]
	if ev1.CPUPercent < 46.0 || ev1.CPUPercent > 46.5 {
		t.Fatalf("delta cpu = %.2f, want ≈46.24", ev1.CPUPercent)
	}
	if ev1.NetUpBps == 0 || ev1.NetDownBps == 0 {
		t.Fatalf("delta net speeds = %d/%d, want > 0", ev1.NetUpBps, ev1.NetDownBps)
	}
	if ev1.MemUsedBytes != (8388608-5242880)*1024 {
		t.Fatalf("mem used = %d", ev1.MemUsedBytes)
	}
	if ev1.UptimeSeconds != 86414 || ev1.DiskRoot != "/" || !strings.Contains(ev1.DfText, "overlay") {
		t.Fatalf("delta payload = %+v", ev1)
	}
}

func TestManagerStopEndsEmissions(t *testing.T) {
	emit := &recEmitter{}
	m := newTestManager(t, &fakeDialer{tabIDs: map[string]bool{"t1": true}}, emit)
	m.exec = func(context.Context, *ssh.Client, string) ([]byte, error) {
		return []byte(sampleOut1), nil
	}
	if err := m.Start("t1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the first event, then stop.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(emit.snapshot()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop("t1")
	before := len(emit.snapshot())
	time.Sleep(250 * time.Millisecond)
	if got := len(emit.snapshot()); got != before {
		t.Fatalf("events after Stop: %d → %d", before, got)
	}
}

func TestManagerStartUnknownTabFails(t *testing.T) {
	m := newTestManager(t, &fakeDialer{tabIDs: map[string]bool{}}, &recEmitter{})
	if err := m.Start("nope"); err == nil {
		t.Fatal("Start on an unknown tab must fail")
	}
}

func TestManagerHandleTabClosedIsIdempotentAndStops(t *testing.T) {
	emit := &recEmitter{}
	m := newTestManager(t, &fakeDialer{tabIDs: map[string]bool{"t1": true}}, emit)
	m.exec = func(context.Context, *ssh.Client, string) ([]byte, error) {
		return []byte(sampleOut1), nil
	}
	if err := m.Start("t1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(emit.snapshot()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.HandleTabClosed("t1") // engine OnTabClosed hook path
	m.HandleTabClosed("t1") // idempotent
	before := len(emit.snapshot())
	time.Sleep(200 * time.Millisecond)
	if got := len(emit.snapshot()); got != before {
		t.Fatalf("events after HandleTabClosed: %d → %d", before, got)
	}
}

func TestManagerStopsSelfAfterConsecutiveFailures(t *testing.T) {
	emit := &recEmitter{}
	m := newTestManager(t, &fakeDialer{tabIDs: map[string]bool{"t1": true}}, emit)
	m.Interval = 10 * time.Millisecond
	m.exec = func(context.Context, *ssh.Client, string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	if err := m.Start("t1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// After maxConsecutiveFailures (5) the ticker stops itself.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, running := m.runs["t1"]
		m.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ticker did not stop itself after consecutive failures")
}

func TestManagerCloseAll(t *testing.T) {
	emit := &recEmitter{}
	m := newTestManager(t, &fakeDialer{tabIDs: map[string]bool{"t1": true}}, emit)
	m.exec = func(context.Context, *ssh.Client, string) ([]byte, error) {
		return []byte(sampleOut1), nil
	}
	if err := m.Start("t1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.CloseAll()
	before := len(emit.snapshot())
	time.Sleep(200 * time.Millisecond)
	if got := len(emit.snapshot()); got != before {
		t.Fatalf("events after CloseAll: %d → %d", before, got)
	}
}
