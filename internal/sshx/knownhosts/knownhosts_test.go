package knownhosts

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const (
	keyEd25519 = "AAAAC3NzaC1lZDI1NTE5AAAAILOC80RQ"
	keyRSA     = "AAAAHHNzLXNhYTU5YkEyNTYAAAADAQABAAABAQ"
)

func TestParseLineMatrix(t *testing.T) {
	cases := []struct {
		line string
		ok   bool
		e    Entry
	}{
		{"# a comment", false, Entry{}},
		{"   ", false, Entry{}},
		{"", false, Entry{}},
		{"example.com ssh-ed25519 " + keyEd25519, true,
			Entry{Host: "example.com", Port: 22, KeyType: "ssh-ed25519", Key: keyEd25519}},
		{"[10.0.0.5]:2222 ssh-ed25519 " + keyEd25519, true,
			Entry{Host: "10.0.0.5", Port: 2222, KeyType: "ssh-ed25519", Key: keyEd25519}},
		{"[myhost]:22 ssh-rsa " + keyRSA, true,
			Entry{Host: "myhost", Port: 22, KeyType: "ssh-rsa", Key: keyRSA}},
		{"example.com ssh-ed25519 " + keyEd25519 + " trailing comment words", true,
			Entry{Host: "example.com", Port: 22, KeyType: "ssh-ed25519", Key: keyEd25519}},
		{"onlytwo tokens", false, Entry{}},
		{"@cert-authority github.com ssh-ed25519 " + keyEd25519, false, Entry{}},
		{"@revoked host ssh-rsa " + keyRSA, false, Entry{}},
		{"|1|abcd|efgh ssh-ed25519 " + keyEd25519, false, Entry{}},  // hashed
		{"*.example.com ssh-ed25519 " + keyEd25519, false, Entry{}}, // wildcard
		{"?foo ssh-ed25519 " + keyEd25519, false, Entry{}},
		{"h1,h2 ssh-ed25519 " + keyEd25519, false, Entry{}}, // multi-host list
		{"[badport ssh-ed25519 " + keyEd25519, false, Entry{}},
		{"[h]:99999 ssh-ed25519 " + keyEd25519, false, Entry{}},
		{"[h]: ssh-ed25519 " + keyEd25519, false, Entry{}},
		{"[]:22 ssh-ed25519 " + keyEd25519, false, Entry{}},
	}
	for _, c := range cases {
		got, ok := ParseLine(c.line)
		if ok != c.ok {
			t.Errorf("ParseLine(%q) ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if ok && got != c.e {
			t.Errorf("ParseLine(%q) = %+v, want %+v", c.line, got, c.e)
		}
	}
}

func readManagerFile(t *testing.T, content string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

func TestLoadSkipsInertLines(t *testing.T) {
	content := strings.Join([]string{
		"# comment line",
		"",
		"example.com ssh-ed25519 " + keyEd25519,
		"|1|hash|hash ssh-ed25519 " + keyEd25519,
		"@cert-authority example.com ssh-rsa " + keyRSA,
		"*.example.com ssh-ed25519 " + keyEd25519,
		"[10.5.5.5]:2222 ssh-rsa " + keyRSA,
	}, "\n")
	m, _ := readManagerFile(t, content)

	got := m.Lookup("example.com", 22)
	if len(got) != 1 || got[0].KeyType != "ssh-ed25519" {
		t.Fatalf("clean entries = %+v, want exactly 1 ed25519", got)
	}
	got = m.Lookup("10.5.5.5", 2222)
	if len(got) != 1 || got[0].KeyType != "ssh-rsa" {
		t.Fatalf("bracket entries = %+v, want exactly 1 rsa", got)
	}
	if n := len(m.Lookup("example.com", 2222)); n != 0 {
		t.Fatalf("port mismatch should not match: %+v", n)
	}
	if n := len(m.Lookup("*.example.com", 22)); n != 0 {
		t.Fatalf("wildcard must be inert: %+v", n)
	}
}

func TestLookupCaseInsensitiveAndPortSemantics(t *testing.T) {
	m, _ := readManagerFile(t, "Example.COM ssh-ed25519 "+keyEd25519+"\n")
	if n := len(m.Lookup("example.com", 22)); n != 1 {
		t.Fatalf("case-insensitive lookup failed: %d", n)
	}
	if n := len(m.Lookup("EXAMPLE.com", 22)); n != 1 {
		t.Fatalf("case-insensitive lookup failed: %d", n)
	}
	if n := len(m.Lookup("example.com", 2222)); n != 0 {
		t.Fatal("default-port entry must not match non-default port")
	}
}

func TestAddIdempotentAndConflict(t *testing.T) {
	dir := t.TempDir()
	m, err := New(filepath.Join(dir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Add("h.example.com", 22, "ssh-ed25519", keyEd25519); err != nil {
		t.Fatal(err)
	}
	// Same key again: no-op.
	if err := m.Add("h.example.com", 22, "ssh-ed25519", keyEd25519); err != nil {
		t.Fatalf("idempotent add failed: %v", err)
	}
	if n := len(m.Lookup("h.example.com", 22)); n != 1 {
		t.Fatalf("entries after duplicate add = %d, want 1", n)
	}
	// Different key, same (host, port, keyType): conflict — TOFU never
	// silently overwrites.
	err = m.Add("h.example.com", 22, "ssh-ed25519", keyRSA)
	if !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("want ErrKeyConflict, got %v", err)
	}
	// Different key TYPE on the same host is a separate entry.
	if err := m.Add("h.example.com", 22, "ssh-rsa", keyRSA); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Lookup("h.example.com", 22)); n != 2 {
		t.Fatalf("entries after second type add = %d, want 2", n)
	}
	if !m.Has("h.example.com", 22, "ssh-ed25519", keyEd25519) {
		t.Fatal("Has failed for stored key")
	}
	if m.Has("h.example.com", 22, "ssh-ed25519", keyRSA) {
		t.Fatal("Has true for unstored key")
	}
}

func TestSaveRewritesCleanLinesOnly(t *testing.T) {
	inert := strings.Join([]string{
		"# comment",
		"|1|h|h ssh-ed25519 " + keyEd25519,
		"*.x ssh-ed25519 " + keyEd25519,
		"@cert-authority x ssh-rsa " + keyRSA,
	}, "\n")
	m, path := readManagerFile(t, inert+"\nexample.com ssh-ed25519 "+keyEd25519+"\n")
	if err := m.Add("10.0.0.9", 2222, "ssh-rsa", keyRSA); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "example.com ssh-ed25519 " + keyEd25519 + "\n[10.0.0.9]:2222 ssh-rsa " + keyRSA + "\n"
	if string(got) != want {
		t.Fatalf("saved file:\n%q\nwant:\n%q", got, want)
	}
	// Perms.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perms = %o, want 0600", fi.Mode().Perm())
	}
	// Re-load: idempotent, same entries.
	m2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got1 := m.Lookup("example.com", 22)
	got2 := m2.Lookup("example.com", 22)
	if !reflect.DeepEqual(got1, got2) {
		t.Fatalf("reloaded entries differ: %+v vs %+v", got1, got2)
	}
	if err := m2.Save(); err != nil {
		t.Fatal(err)
	}
	got3, _ := os.ReadFile(path)
	if string(got3) != want {
		t.Fatal("second save changed content (not idempotent)")
	}
}

func TestSaveCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	m, err := New(filepath.Join(dir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Add("h", 22, "ssh-ed25519", keyEd25519); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "h ssh-ed25519 "+keyEd25519+"\n" {
		t.Fatalf("content = %q", raw)
	}
}

func TestFingerprint(t *testing.T) {
	// Precomputed: sha256 of base64("AAAAC3NzaC1lZDI1NTE5AAAA") bytes.
	got, err := Fingerprint("AAAAC3NzaC1lZDI1NTE5AAAA")
	if err != nil {
		t.Fatal(err)
	}
	const want = "SHA256:uALbfMqe7g4MMaRS5NMJen38dAEHwtxzR0iX0Ymuc80"
	if got != want {
		t.Fatalf("Fingerprint = %q, want %q", got, want)
	}
	// Display format: prefix + 43 unpadded base64 chars.
	re := regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	if !re.MatchString(got) {
		t.Fatalf("format wrong: %q", got)
	}
	if _, err := Fingerprint("!!!not-base64!!!"); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}
