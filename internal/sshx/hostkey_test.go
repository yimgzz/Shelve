package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"dummy-ssh-manager/internal/sshx/knownhosts"
)

func tcpAddr(port int) *net.TCPAddr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

// dialAddr returns the address string an engine passes to ssh.Dial /
// ssh.NewClientConn, which is exactly what the callback receives as
// its hostname argument.
func dialAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// genRemoteKey generates an ed25519 key and returns it as an
// ssh.PublicKey plus its base64 form (the authorized_keys key part,
// without the key-type prefix, as stored in known_hosts).
func genRemoteKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return hostKeyFromPublicKey(t, pub)
}

func genRSAHostKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return hostKeyFromPublicKey(t, &priv.PublicKey)
}

func hostKeyFromPublicKey(t *testing.T, pub any) (ssh.PublicKey, string) {
	t.Helper()
	remote, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(remote))) // "type b64"
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		t.Fatal("MarshalAuthorizedKey output missing key b64 part")
	}
	return remote, line[i+1:]
}

func newTestManager(t *testing.T) (*knownhosts.Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	m, err := knownhosts.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

type promptCall struct {
	Host        string
	Port        int
	KeyType     string
	KeyB64      string
	Fingerprint string
}

// fakeApprover is a scriptable HostKeyApprover. It records every
// prompt; when no decision channel is scripted it returns a closed
// channel, which the callback treats as rejection.
type fakeApprover struct {
	mu       sync.Mutex
	calls    []promptCall
	decision chan bool
	err      error
}

func (f *fakeApprover) PromptHostKey(host string, port int, keyType, keyB64, fingerprint string) (<-chan bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, promptCall{host, port, keyType, keyB64, fingerprint})
	dec, err := f.decision, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if dec == nil {
		dec = make(chan bool)
		close(dec)
	}
	return dec, nil
}

func (f *fakeApprover) callsSnapshot() []promptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]promptCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func acceptChan() chan bool {
	c := make(chan bool, 1)
	c <- true
	return c
}

func rejectChan() chan bool {
	c := make(chan bool, 1)
	c <- false
	return c
}

func TestHostKeyCallbackKnownGood(t *testing.T) {
	m, _ := newTestManager(t)
	remote, b64 := genRemoteKey(t)
	if err := m.Add("example.com", 22, remote.Type(), b64); err != nil {
		t.Fatal(err)
	}

	app := &fakeApprover{}
	cb := NewHostKeyCallback(m, app)
	if err := cb("example.com:22", tcpAddr(22), remote); err != nil {
		t.Fatalf("known-good key must be accepted silently, got %v", err)
	}
	if n := len(app.callsSnapshot()); n != 0 {
		t.Fatalf("known-good key must not prompt, got %d prompts", n)
	}
}

func TestHostKeyCallbackNonTCPAddrFallsBackToDefaultPort(t *testing.T) {
	m, _ := newTestManager(t)
	remote, b64 := genRemoteKey(t)
	if err := m.Add("example.com", 22, remote.Type(), b64); err != nil {
		t.Fatal(err)
	}

	cb := NewHostKeyCallback(m, &fakeApprover{})
	if err := cb("example.com", &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, remote); err != nil {
		t.Fatalf("port-less addr must fall back to port 22, got %v", err)
	}
}

func TestHostKeyCallbackKnownGoodOverIPv6DialAddress(t *testing.T) {
	m, _ := newTestManager(t)
	remote, b64 := genRemoteKey(t)
	if err := m.Add("::1", 22, remote.Type(), b64); err != nil {
		t.Fatal(err)
	}

	app := &fakeApprover{}
	cb := NewHostKeyCallback(m, app)
	if err := cb("[::1]:22", tcpAddr(22), remote); err != nil {
		t.Fatalf("known-good IPv6 dial address must be accepted, got %v", err)
	}
	if n := len(app.callsSnapshot()); n != 0 {
		t.Fatalf("known-good IPv6 key must not prompt, got %d prompts", n)
	}
}

// Regression for the TOFU bypass: with multiple key types stored for
// the same (host, port), a presented key that matches no entry must
// hard-fail with ErrHostKeyChanged over the real dial-address form
// ("host:port"), never fall through to the prompt path, and the
// "expected" fingerprint must come from the stored entry of the SAME
// key type (not blindly entries[0]).
func TestHostKeyCallbackMismatchOverDialAddressIsHardFail(t *testing.T) {
	const host = "example.com"
	m, _ := newTestManager(t)
	edStored, edStoredB64 := genRemoteKey(t) // ssh-ed25519, stored first
	rsaStored, rsaStoredB64 := genRSAHostKey(t)
	rsaAttacker, rsaAttackerB64 := genRSAHostKey(t)
	if err := m.Add(host, 22, edStored.Type(), edStoredB64); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(host, 22, rsaStored.Type(), rsaStoredB64); err != nil {
		t.Fatal(err)
	}

	app := &fakeApprover{}
	cb := NewHostKeyCallback(m, app)
	err := cb(dialAddr(host, 22), tcpAddr(22), rsaAttacker)

	var chg *ErrHostKeyChanged
	if !errors.As(err, &chg) {
		t.Fatalf("changed key over dial address must be a hard fail, got %v", err)
	}
	wantExpected, fErr := knownhosts.Fingerprint(rsaStoredB64)
	if fErr != nil {
		t.Fatal(fErr)
	}
	wantActual, fErr := knownhosts.Fingerprint(rsaAttackerB64)
	if fErr != nil {
		t.Fatal(fErr)
	}
	if chg.Expected != wantExpected {
		t.Fatalf("Expected = %s (the ssh-ed25519 entry was reported), want the ssh-rsa entry %s", chg.Expected, wantExpected)
	}
	if chg.Actual != wantActual {
		t.Fatalf("Actual = %s, want %s", chg.Actual, wantActual)
	}
	if n := len(app.callsSnapshot()); n != 0 {
		t.Fatalf("a mismatch must never prompt, got %d prompts", n)
	}
}

func TestHostKeyCallbackKnownDifferent(t *testing.T) {
	const host = "example.com"
	m, _ := newTestManager(t)
	known, knownB64 := genRemoteKey(t)
	other, otherB64 := genRemoteKey(t)
	if err := m.Add(host, 22, known.Type(), knownB64); err != nil {
		t.Fatal(err)
	}

	app := &fakeApprover{}
	cb := NewHostKeyCallback(m, app)
	err := cb(dialAddr(host, 22), tcpAddr(22), other)

	var chg *ErrHostKeyChanged
	if !errors.As(err, &chg) {
		t.Fatalf("want *ErrHostKeyChanged, got %v", err)
	}
	wantExpected, err := knownhosts.Fingerprint(knownB64)
	if err != nil {
		t.Fatal(err)
	}
	wantActual, err := knownhosts.Fingerprint(otherB64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(wantExpected, "SHA256:") || !strings.HasPrefix(wantActual, "SHA256:") {
		t.Fatalf("fingerprints must be in SHA256: form, got %q / %q", wantExpected, wantActual)
	}
	if chg.Host != host || chg.Port != 22 {
		t.Fatalf("host/port = %s:%d, want %s:22", chg.Host, chg.Port, host)
	}
	if chg.Expected != wantExpected {
		t.Fatalf("Expected = %s, want %s", chg.Expected, wantExpected)
	}
	if chg.Actual != wantActual {
		t.Fatalf("Actual = %s, want %s", chg.Actual, wantActual)
	}
	if n := len(app.callsSnapshot()); n != 0 {
		t.Fatalf("a host-key change must fail hard without prompting, got %d prompts", n)
	}
}

func TestHostKeyCallbackUnknownAcceptPersists(t *testing.T) {
	const host = "example.com"
	m, path := newTestManager(t)
	remote, b64 := genRemoteKey(t)

	app := &fakeApprover{decision: acceptChan()}
	cb := NewHostKeyCallback(m, app)
	if err := cb(dialAddr(host, 22), tcpAddr(22), remote); err != nil {
		t.Fatalf("accepted unknown key must be accepted, got %v", err)
	}

	calls := app.callsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 prompt, got %d", len(calls))
	}
	c := calls[0]
	if c.Host != host || c.Port != 22 {
		t.Fatalf("prompt host/port = %s:%d, want %s:22", c.Host, c.Port, host)
	}
	if c.KeyType != remote.Type() || c.KeyB64 != b64 {
		t.Fatalf("prompt key = %s %s, want %s %s", c.KeyType, c.KeyB64, remote.Type(), b64)
	}
	if !strings.HasPrefix(c.Fingerprint, "SHA256:") {
		t.Fatalf("prompt fingerprint = %q, want SHA256: prefix", c.Fingerprint)
	}

	// The acceptance must have been persisted to disk.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("known_hosts not written after accept: %v", err)
	}
	want := host + " " + remote.Type() + " " + b64 + "\n"
	if string(data) != want {
		t.Fatalf("known_hosts = %q, want %q", data, want)
	}

	// A reconnect now passes without another prompt.
	if err := cb(dialAddr(host, 22), tcpAddr(22), remote); err != nil {
		t.Fatalf("reconnect after accept must pass, got %v", err)
	}
	if n := len(app.callsSnapshot()); n != 1 {
		t.Fatalf("reconnect must not prompt again, got %d prompts total", n)
	}
}

func TestHostKeyCallbackUnknownAcceptNonDefaultPort(t *testing.T) {
	const host = "10.0.0.5"
	m, path := newTestManager(t)
	remote, b64 := genRemoteKey(t)

	app := &fakeApprover{decision: acceptChan()}
	cb := NewHostKeyCallback(m, app)
	if err := cb(dialAddr(host, 2222), tcpAddr(2222), remote); err != nil {
		t.Fatalf("accepted unknown key must be accepted, got %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("known_hosts not written after accept: %v", err)
	}
	want := "[" + host + "]:2222 " + remote.Type() + " " + b64 + "\n"
	if string(data) != want {
		t.Fatalf("known_hosts = %q, want %q", data, want)
	}
}

func TestHostKeyCallbackUnknownReject(t *testing.T) {
	const host = "example.com"
	m, path := newTestManager(t)
	remote, _ := genRemoteKey(t)

	app := &fakeApprover{decision: rejectChan()}
	cb := NewHostKeyCallback(m, app)
	err := cb(dialAddr(host, 22), tcpAddr(22), remote)

	if !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("want ErrHostKeyRejected, got %v", err)
	}
	if err.Error() != "host key rejected by user" {
		t.Fatalf("error = %q, want %q", err, "host key rejected by user")
	}
	if _, serr := os.Stat(path); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("reject must not persist the key (stat err = %v)", serr)
	}

	// The key is still unknown: a later prompt-and-accept flow works.
	app2 := &fakeApprover{decision: acceptChan()}
	cb2 := NewHostKeyCallback(m, app2)
	if err := cb2(dialAddr(host, 22), tcpAddr(22), remote); err != nil {
		t.Fatalf("prompt after reject must still work, got %v", err)
	}
}

func TestHostKeyCallbackApproverErrorPropagates(t *testing.T) {
	const host = "example.com"
	m, _ := newTestManager(t)
	remote, b64 := genRemoteKey(t)
	boom := errors.New("app: prompt dialog failed")

	app := &fakeApprover{err: boom}
	cb := NewHostKeyCallback(m, app)
	err := cb(dialAddr(host, 22), tcpAddr(22), remote)

	if !errors.Is(err, boom) {
		t.Fatalf("approver error must propagate unchanged, got %v", err)
	}
	if m.Has(host, 22, remote.Type(), b64) {
		t.Fatal("key must not be stored when the prompt fails")
	}
}

func TestHostKeyCallbackConcurrentKnownGood(t *testing.T) {
	const host = "example.com"
	m, _ := newTestManager(t)
	remote, b64 := genRemoteKey(t)
	if err := m.Add(host, 22, remote.Type(), b64); err != nil {
		t.Fatal(err)
	}

	app := &fakeApprover{}
	cb := NewHostKeyCallback(m, app)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := cb(dialAddr(host, 22), tcpAddr(22), remote); err != nil {
				t.Errorf("concurrent known-good callback failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := len(app.callsSnapshot()); n != 0 {
		t.Fatalf("concurrent known-good callbacks must not prompt, got %d prompts", n)
	}
}
