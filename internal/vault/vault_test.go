package vault

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testPassword = "hunter2-master-pw"
	testPayload1 = `{"root":["f1"],"folders":[{"id":"f1","parentId":"","name":"F","children":[]}],"sessions":[{"id":"s1","folderId":"f1","name":"S","host":"h","port":22,"user":"u","auth":{"type":0,"password":"pW-s3cret-mark-1"}}]}`
	testPayload2 = `{"root":["f1","s2"],"folders":[{"id":"f1","parentId":"","name":"F","children":[]}],"sessions":[{"id":"s2","folderId":"","name":"S2","host":"h2","port":22,"user":"u2","auth":{"type":1,"keyPath":"/k"}}]}`
)

func tmpVaultPath(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	return dir, filepath.Join(dir, "vault.json")
}

func createVault(t *testing.T, path, pw string, payload []byte) *Vault {
	t.Helper()
	v := New()
	if err := v.Create(path, pw, payload); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return v
}

func TestCreateOpenRoundTrip(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	v := New()
	if st, err := v.Probe(path); err != nil || st != StatusLocked {
		t.Fatalf("Probe = %v, %v; want locked", st, err)
	}
	out, err := v.Open(path, testPassword)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(out) != testPayload1 {
		t.Fatalf("payload mismatch:\n got %s\nwant %s", out, testPayload1)
	}
	if !v.IsUnlocked() {
		t.Fatal("not unlocked after open")
	}
}

func TestCreateFailsWhenFileExists(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))
	if err := New().Create(path, testPassword, []byte(testPayload2)); err != ErrVaultExists {
		t.Fatalf("want ErrVaultExists, got %v", err)
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := New().Open("/nonexistent-dir-xyz/vault.json", testPassword); err != ErrVaultNotFound {
		t.Fatalf("want ErrVaultNotFound, got %v", err)
	}
}

func TestProbeStateMachine(t *testing.T) {
	dir, path := tmpVaultPath(t)
	_ = dir
	v := New()
	st, err := v.Probe(path)
	if err != nil || st != StatusCreateNeeded {
		t.Fatalf("no file: Probe = %v, %v; want create", st, err)
	}
	createVault(t, path, testPassword, []byte(testPayload1))
	if st, err := New().Probe(path); err != nil || st != StatusLocked {
		t.Fatalf("file present: Probe = %v, %v; want locked", st, err)
	}
}

func TestWrongPasswordFailsTyped(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	v := New()
	_, err := v.Open(path, "completely-wrong")
	if err != ErrWrongPassword {
		t.Fatalf("want ErrWrongPassword, got %v", err)
	}
	if v.IsUnlocked() {
		t.Fatal("vault unlocked after wrong password")
	}
	// The correct password must still work afterwards.
	if _, err := v.Open(path, testPassword); err != nil {
		t.Fatalf("correct password after wrong attempt: %v", err)
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	ct, err := base64.StdEncoding.DecodeString(env.Ct)
	if err != nil {
		t.Fatal(err)
	}
	ct[5] ^= 0x01 // flip one ciphertext byte
	env.Ct = base64.StdEncoding.EncodeToString(ct)
	raw, _ = json.Marshal(env)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	v := New()
	_, err = v.Open(path, testPassword)
	if err == nil {
		t.Fatal("tampered ciphertext must not decrypt")
	}
	if err != ErrWrongPassword && err != ErrCorruptVault {
		t.Fatalf("want a typed failure, got %v", err)
	}
	if v.IsUnlocked() {
		t.Fatal("vault unlocked after tamper")
	}
}

func TestCorruptEnvelopeFailsTyped(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	mutate := func(fn func(*Envelope)) []byte {
		t.Helper()
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		fn(&env)
		out, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cases := map[string][]byte{
		"not-json":          []byte("{definitely not json"),
		"wrong-version":     mutate(func(e *Envelope) { e.V = 9 }),
		"wrong-kdf-alg":     mutate(func(e *Envelope) { e.KDF.Alg = "argon2ne" }),
		"wrong-enc-alg":     mutate(func(e *Envelope) { e.Enc.Alg = "chacha20" }),
		"bad-salt":          mutate(func(e *Envelope) { e.KDF.Salt = "!!!" }),
		"short-nonce":       mutate(func(e *Envelope) { e.Enc.Nonce = "AAA" }),
		"absurd-kdf-memory": mutate(func(e *Envelope) { e.KDF.Memory = 999999999 }),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := New().Open(path, testPassword)
			if err != ErrCorruptVault {
				t.Fatalf("want ErrCorruptVault, got %v", err)
			}
		})
	}
}

func TestSaveWhileLockedFails(t *testing.T) {
	_, path := tmpVaultPath(t)
	v := createVault(t, path, testPassword, []byte(testPayload1))
	before, _ := os.ReadFile(path)

	v.Lock()
	if err := v.Save([]byte(testPayload2)); err != ErrLocked {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file changed by a failed save")
	}
	// No temp file leaked into the dir.
	for _, e := range mustReadDir(t, filepath.Dir(path)) {
		if e.Name() != "vault.json" {
			t.Fatalf("leaked %q after failed save", e.Name())
		}
	}
}

func TestSaveFreshNoncePerWrite(t *testing.T) {
	_, path := tmpVaultPath(t)
	v := createVault(t, path, testPassword, []byte(testPayload1))
	first := readEnvelope(t, path)

	if err := v.Save([]byte(testPayload2)); err != nil {
		t.Fatal(err)
	}
	second := readEnvelope(t, path)
	if first.Enc.Nonce == second.Enc.Nonce {
		t.Fatal("nonce was reused across writes")
	}
	if first.KDF.Salt != second.KDF.Salt {
		t.Fatal("salt changed across writes (must be stable per file)")
	}

	v3 := New()
	out, err := v3.Open(path, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != testPayload2 {
		t.Fatalf("payload after save: %s", out)
	}
}

// Regression: after a successful Open, the derived key must still be the
// real key (not a zeroized one) when Save re-encrypts. A deferred
// zeroization capturing the key at registration time would silently
// encrypt with an all-zero key.
func TestSaveAfterOpenUsesDerivedKey(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	v := New()
	out, err := v.Open(path, testPassword)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(out) != testPayload1 {
		t.Fatal("payload mismatch after open")
	}
	if err := v.Save([]byte(testPayload2)); err != nil {
		t.Fatalf("Save after Open: %v", err)
	}
	v3 := New()
	out2, err := v3.Open(path, testPassword)
	if err != nil {
		t.Fatalf("re-Open after Save: %v", err)
	}
	if string(out2) != testPayload2 {
		t.Fatal("payload after save mismatch")
	}
}

func TestLockZeroizesKey(t *testing.T) {
	_, path := tmpVaultPath(t)
	v := createVault(t, path, testPassword, []byte(testPayload1))
	v.Lock()

	if v.IsUnlocked() {
		t.Fatal("unlocked after Lock")
	}
	if st := v.Status(); st != StatusLocked {
		t.Fatalf("Status = %v, want locked", st)
	}
	// The in-memory key is gone: Save must fail...
	if err := v.Save([]byte(testPayload2)); err != ErrLocked {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	// ...but the password still re-unlocks (key re-derived).
	if _, err := v.Open(path, testPassword); err != nil {
		t.Fatalf("re-open after lock: %v", err)
	}
	// A second Lock is a safe no-op.
	v.Lock()
	v.Lock()
}

func TestFilePerms(t *testing.T) {
	dir, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("vault file perms = %o, want 0600", fi.Mode().Perm())
	}
	fi, err = os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("vault dir perms = %o, want 0700", fi.Mode().Perm())
	}
}

// Master plan §8.1 guard: the vault file on disk must not contain any
// plaintext secret (or even session names) from the payload.
func TestRawFileContainsNoPlaintext(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		testPassword,
		"pW-s3cret-mark-1",
		`"name":"S"`,
		"folders",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("vault file contains plaintext %q", secret)
		}
	}
	// The envelope header metadata is visible by design — assert it is
	// well-formed and does not leak KDF-derived material.
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.V != FileVersion || env.KDF.Alg != "argon2id" || env.Enc.Alg != "aes-256-gcm" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

// Errors must never echo password material (§8.3).
func TestErrorsNeverContainPassword(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testPassword, []byte(testPayload1))

	_, err := New().Open(path, "totally-different")
	if err == nil || strings.Contains(err.Error(), testPassword) {
		t.Fatalf("error may not contain password: %v", err)
	}
	for _, e := range []error{ErrWrongPassword, ErrCorruptVault, ErrLocked, ErrVaultExists, ErrVaultNotFound} {
		if strings.Contains(e.Error(), testPassword) {
			t.Fatalf("sentinel %v leaks password", e)
		}
	}
}

func TestLockSweepsTempDir(t *testing.T) {
	dir, path := tmpVaultPath(t)
	v := createVault(t, path, testPassword, []byte(testPayload1))

	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "sftp-edit.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("tmp dir not swept: %v", err)
	}
}

// ------------------------------------------------------------- helpers ---

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func readEnvelope(t *testing.T, path string) Envelope {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}
