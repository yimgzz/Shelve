//go:build integration

package monitor

// Plan P004 integration test (master plan §9): the monitor Manager exercised
// against a REAL openssh server via testcontainers (docker/sshd). Run with
// `make test-integration` (Go tag `integration`; Docker socket mounted).
// Credentials and fixtures live ONLY inside docker/sshd.

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"golang.org/x/crypto/ssh"
)

// liveDialer is a Dialer backed by a real SSH client to the test box.
type liveDialer struct {
	cli *ssh.Client
}

func (d *liveDialer) DialMonitorClient(string) ([]*ssh.Client, error) {
	return []*ssh.Client{d.cli}, nil
}

// boxEmitter records monitor:metrics payloads in order (integration-local
// copy; the unit-test recEmitter lives in the non-tagged _test file).
type boxEmitter struct {
	mu     sync.Mutex
	events []MetricsPayload
}

func (b *boxEmitter) Emit(event string, payload any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if event == EventMonitorMetrics {
		if p, ok := payload.(MetricsPayload); ok {
			b.events = append(b.events, p)
		}
	}
}

func (b *boxEmitter) snapshot() []MetricsPayload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]MetricsPayload(nil), b.events...)
}

// startSshd builds the docker/sshd image and starts it, returning its
// published address (same helper shape as the 5c SFTP integration suite).
func startSshd(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    "../../docker/sshd",
			Dockerfile: "Dockerfile",
		},
		ExposedPorts: []string{"22/tcp"},
		WaitingFor:   wait.ForListeningPort("22/tcp").WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start sshd: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("sshd host: %v", err)
	}
	port, err := c.MappedPort(ctx, "22/tcp")
	if err != nil {
		t.Fatalf("sshd port: %v", err)
	}
	return net.JoinHostPort(host, port.Port())
}

// TestMonitorCollectsRealMetrics connects to the test box, starts the ticker
// and asserts a plausible metrics snapshot arrives within a couple of ticks.
func TestMonitorCollectsRealMetrics(t *testing.T) {
	addr := startSshd(t)
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("test")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // integration-test fixture only
		Timeout:         15 * time.Second,
	}
	cli, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	emit := &boxEmitter{}
	m := New(emit)
	m.Attach(&liveDialer{cli: cli})
	m.Interval = 200 * time.Millisecond
	m.ExecTimeout = 5 * time.Second

	if err := m.Start("box"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("box")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n := len(emit.snapshot()); n >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	evs := emit.snapshot()
	if len(evs) < 2 {
		t.Fatalf("got %d metrics events, want ≥ 2", len(evs))
	}
	last := evs[len(evs)-1]
	if last.Hostname == "" {
		t.Fatal("hostname is empty")
	}
	if last.CPUPercent < 0 || last.CPUPercent > 100 {
		t.Fatalf("cpuPercent = %v, want [0,100]", last.CPUPercent)
	}
	if last.MemTotalBytes == 0 || last.MemUsedBytes == 0 {
		t.Fatalf("mem = %d/%d, want > 0", last.MemUsedBytes, last.MemTotalBytes)
	}
	if last.UptimeSeconds == 0 {
		t.Fatal("uptime is 0")
	}
	if last.DiskUsedPct <= 0 || last.DiskRoot == "" {
		t.Fatalf("disk = %v%% on %q, want > 0 and mount set", last.DiskUsedPct, last.DiskRoot)
	}
	if !strings.Contains(last.DfText, "Filesystem") {
		t.Fatalf("dfText looks wrong: %q", last.DfText)
	}

	// After Stop no further events arrive.
	m.Stop("box")
	before := len(emit.snapshot())
	time.Sleep(500 * time.Millisecond)
	if got := len(emit.snapshot()); got != before {
		t.Fatalf("events after Stop: %d → %d", before, got)
	}
}
