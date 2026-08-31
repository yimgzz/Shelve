package sshx

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"shelve/internal/sshx/knownhosts"
)

// defaultSSHPort is the fallback port when neither the dial address
// nor the remote address carries one; it matches the knownhosts
// default (master plan §4).
const defaultSSHPort = 22

// HostKeyApprover renders the vault:hostkey-prompt modal for an
// unknown host key (master plan §5). The 3c engine implements it. The
// returned channel delivers exactly one decision: true = accept,
// false = reject. The approver enforces its own prompt timeout (a
// timeout is delivered as a false decision — or a closed channel —
// which the callback treats as rejection).
type HostKeyApprover interface {
	PromptHostKey(host string, port int, keyType, keyB64, fingerprint string) (<-chan bool, error)
}

// ErrHostKeyRejected is returned to the dial when the user (or the
// prompt timeout) rejects the key for an unknown host.
var ErrHostKeyRejected = errors.New("host key rejected by user")

// ErrHostKeyChanged is the TOFU hard failure (master plan §8.5): an
// entry already exists for (host, port) but none matches the key the
// remote presented. Expected and Actual are knownhosts
// fingerprints ("SHA256:..."), never raw keys — they must not be
// logged with more context than the message already carries.
type ErrHostKeyChanged struct {
	Host     string
	Port     int
	Expected string
	Actual   string
}

func (e *ErrHostKeyChanged) Error() string {
	return fmt.Sprintf("host key changed for %s:%d (expected %s, got %s)",
		e.Host, e.Port, e.Expected, e.Actual)
}

// NewHostKeyCallback returns an ssh.HostKeyCallback implementing TOFU
// (master plan §2 A1) against the app-managed known_hosts manager kh:
//
//   - a stored key matches the remote -> accept silently (no events);
//   - entries exist for (host, port) but none match -> ErrHostKeyChanged;
//   - no entries -> escalate to appr; on accept, persist via
//     kh.Add + kh.Save (a concurrent kh.Add conflict is treated as a
//     host-key change); on reject/timeout, ErrHostKeyRejected.
//
// The hostname argument is the dial address exactly as passed to
// ssh.Dial/NewClientConn (per the ssh.HostKeyCallback contract, this
// is "host[:port]", e.g. "example.com:22" or "[::1]:22"), so it is
// normalized to a bare host plus port before lookup: known_hosts
// entries are keyed by (host, port) and stored without the port when
// it is 22. The port is taken from the dial address, falling back to
// the remote *net.TCPAddr port, then to 22. keyB64 is the base64 key
// part (without the key-type prefix), matching knownhosts.Entry.
// appr must be non-nil.
//
// The callback is safe for concurrent calls across connections: kh is
// internally locked and the callback holds no other mutable state.
// One in-flight prompt per (host, port) is NOT guaranteed here — the
// 3c engine serializes prompts per tab.
func NewHostKeyCallback(kh *knownhosts.Manager, appr HostKeyApprover) ssh.HostKeyCallback {
	return func(address string, remote net.Addr, remoteKey ssh.PublicKey) error {
		host, port := normalizeHostPort(address, remote)
		keyType := remoteKey.Type()
		keyB64 := authorizedKeyB64(remoteKey)

		if kh.Has(host, port, keyType, keyB64) {
			return nil // known-good
		}

		if entries := kh.Lookup(host, port); len(entries) > 0 {
			entry := matchingEntry(entries, keyType)
			return &ErrHostKeyChanged{
				Host:     host,
				Port:     port,
				Expected: keyFingerprint(entry.Key),
				Actual:   keyFingerprint(keyB64),
			}
		}

		decision, err := appr.PromptHostKey(host, port, keyType, keyB64, keyFingerprint(keyB64))
		if err != nil {
			return err
		}
		accepted, open := <-decision
		if !open || !accepted {
			return ErrHostKeyRejected
		}

		if err := kh.Add(host, port, keyType, keyB64); err != nil {
			if errors.Is(err, knownhosts.ErrKeyConflict) {
				// A concurrent dial stored a different key for the same
				// (host, port, keyType) between the prompt and the add.
				return hostKeyChangedFromLatest(kh, host, port, keyType, keyB64)
			}
			return err
		}
		return kh.Save()
	}
}

// normalizeHostPort splits the dial address ("host[:port]", per the
// ssh.HostKeyCallback contract) into a bare host and port. If the
// address carries no parseable port, the remote address' port is used,
// falling back to defaultSSHPort.
func normalizeHostPort(address string, remote net.Addr) (host string, port int) {
	host = address
	port = 0
	if h, p, err := net.SplitHostPort(address); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err == nil && n >= 1 && n <= 65535 {
			port = n
		}
	}
	if port == 0 {
		if tcp, ok := remote.(*net.TCPAddr); ok {
			port = tcp.Port
		} else {
			port = defaultSSHPort
		}
	}
	return host, port
}

// matchingEntry returns the stored entry whose key type matches the
// presented key's type (so a host-key changed error reports the
// same-algorithm fingerprint), falling back to the first stored entry
// when none matches.
func matchingEntry(entries []knownhosts.Entry, keyType string) knownhosts.Entry {
	for _, e := range entries {
		if strings.EqualFold(e.KeyType, keyType) {
			return e
		}
	}
	return entries[0]
}

// authorizedKeyB64 extracts the base64 part of the key's
// authorized_keys form. ssh.MarshalAuthorizedKey renders "type b64";
// the known_hosts manager stores the two halves separately
// (knownhosts.Entry{KeyType, Key}).
func authorizedKeyB64(key ssh.PublicKey) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return line[i+1:]
	}
	return line
}

// keyFingerprint returns the "SHA256:..." display form of a base64
// public key, degrading to a placeholder instead of ever surfacing raw
// key material on a corrupt stored entry.
func keyFingerprint(b64 string) string {
	fp, err := knownhosts.Fingerprint(b64)
	if err != nil {
		return "(unreadable)"
	}
	return fp
}

// hostKeyChangedFromLatest rebuilds an ErrHostKeyChanged from whatever
// kh currently stores for (host, port), used when Add raced into a
// conflict after the user accepted the prompt. The conflict can only
// occur when an entry exists for (host, port, keyType) and Manager
// never removes entries, so the lookup is guaranteed non-empty.
func hostKeyChangedFromLatest(kh *knownhosts.Manager, host string, port int, keyType, actualB64 string) error {
	entry := matchingEntry(kh.Lookup(host, port), keyType)
	return &ErrHostKeyChanged{
		Host:     host,
		Port:     port,
		Expected: keyFingerprint(entry.Key),
		Actual:   keyFingerprint(actualB64),
	}
}
