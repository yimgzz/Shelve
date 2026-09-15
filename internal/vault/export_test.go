package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	testExportPW        = "export-passphrase-1"
	testExportPlaintext = `{"payload":{"root":["f1"],"folders":[{"id":"f1","name":"F"}]},"settings":{"theme":"dark"}}`
)

// encryptTestExport is a round-trip helper returning the raw envelope bytes.
func encryptTestExport(t *testing.T) []byte {
	t.Helper()
	data, err := EncryptWithPassword([]byte(testExportPlaintext), testExportPW, "0.1.0")
	if err != nil {
		t.Fatalf("EncryptWithPassword: %v", err)
	}
	return data
}

func TestExportRoundTrip(t *testing.T) {
	data := encryptTestExport(t)

	got, err := DecryptWithPassword(data, testExportPW)
	if err != nil {
		t.Fatalf("DecryptWithPassword: %v", err)
	}
	if !bytes.Equal(got, []byte(testExportPlaintext)) {
		t.Fatalf("round-trip mismatch:\n got %s\nwant %s", got, testExportPlaintext)
	}

	// The envelope header is readable and carries the documented shape.
	var env ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("envelope JSON: %v", err)
	}
	if env.Format != "shelve-export" || env.V != ExportVersion {
		t.Fatalf("format/version = %q/%d", env.Format, env.V)
	}
	if env.App != "0.1.0" || env.CreatedAt == "" {
		t.Fatalf("app/createdAt = %q/%q", env.App, env.CreatedAt)
	}
	if env.KDF.Alg != kdfAlg || env.KDF.Time != kdfTime || env.KDF.Memory != kdfMemory || env.KDF.Threads != kdfThreads {
		t.Fatalf("kdf spec = %+v", env.KDF)
	}
	if env.Enc.Alg != encAlg {
		t.Fatalf("enc spec = %+v", env.Enc)
	}

	// The file must never contain the passphrase or the plaintext.
	if bytes.Contains(data, []byte(testExportPW)) {
		t.Fatal("export envelope contains the passphrase")
	}
	if bytes.Contains(data, []byte(testExportPlaintext)) {
		t.Fatal("export envelope contains the plaintext")
	}
}

func TestExportWrongPassphrase(t *testing.T) {
	data := encryptTestExport(t)
	if _, err := DecryptWithPassword(data, "not-the-passphrase"); !errors.Is(err, ErrExportWrongPassword) {
		t.Fatalf("wrong passphrase: %v, want ErrExportWrongPassword", err)
	}
}

func TestExportTamperedCiphertext(t *testing.T) {
	data := encryptTestExport(t)
	var env ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	ct, err := base64.StdEncoding.DecodeString(env.Ct)
	if err != nil {
		t.Fatal(err)
	}
	ct[len(ct)-1] ^= 0xff
	env.Ct = base64.StdEncoding.EncodeToString(ct)
	tampered, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptWithPassword(tampered, testExportPW); err == nil {
		t.Fatal("tampered ciphertext decrypted successfully")
	}
}

func TestExportTamperedNonce(t *testing.T) {
	data := encryptTestExport(t)
	var env ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}

	// A structurally valid but different nonce fails the GCM auth check.
	other := make([]byte, nonceSize)
	if _, err := rand.Read(other); err != nil {
		t.Fatal(err)
	}
	env.Enc.Nonce = b64encode(other)
	valid, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptWithPassword(valid, testExportPW); !errors.Is(err, ErrExportWrongPassword) {
		t.Fatalf("wrong nonce: %v, want ErrExportWrongPassword", err)
	}

	// A malformed (wrong-length) nonce is structural corruption.
	env.Enc.Nonce = b64encode([]byte("short"))
	malformed, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptWithPassword(malformed, testExportPW); !errors.Is(err, ErrExportCorrupt) {
		t.Fatalf("bad nonce length: %v, want ErrExportCorrupt", err)
	}
}

func TestExportUnsupportedFormatVersion(t *testing.T) {
	data := encryptTestExport(t)
	var env ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}

	env.Format = "some-other-format"
	badFormat, _ := json.Marshal(env)
	if _, err := DecryptWithPassword(badFormat, testExportPW); !errors.Is(err, ErrUnsupportedExport) {
		t.Fatalf("unknown format: %v, want ErrUnsupportedExport", err)
	}

	env.Format = exportFormat
	env.V = ExportVersion + 1
	badVersion, _ := json.Marshal(env)
	if _, err := DecryptWithPassword(badVersion, testExportPW); !errors.Is(err, ErrUnsupportedExport) {
		t.Fatalf("unknown version: %v, want ErrUnsupportedExport", err)
	}
}

func TestExportCorruptEnvelope(t *testing.T) {
	if _, err := DecryptWithPassword([]byte("not json at all"), testExportPW); !errors.Is(err, ErrExportCorrupt) {
		t.Fatalf("bad JSON: %v, want ErrExportCorrupt", err)
	}

	data := encryptTestExport(t)
	var env ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}

	// Out-of-range KDF parameters must be rejected before deriving.
	for _, mutate := range []func(*ExportEnvelope){
		func(e *ExportEnvelope) { e.KDF.Memory = 1 << 30 },
		func(e *ExportEnvelope) { e.KDF.Time = 0 },
		func(e *ExportEnvelope) { e.KDF.Threads = 0 },
		func(e *ExportEnvelope) { e.KDF.Alg = "pbkdf2" },
		func(e *ExportEnvelope) { e.Enc.Alg = "chacha20" },
		func(e *ExportEnvelope) { e.KDF.Salt = b64encode([]byte("short")) },
		func(e *ExportEnvelope) { e.Ct = "!!!" },
	} {
		e := env
		mutate(&e)
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecryptWithPassword(raw, testExportPW); !errors.Is(err, ErrExportCorrupt) {
			t.Fatalf("corrupt envelope %+v: %v, want ErrExportCorrupt", e, err)
		}
	}
}

// TestExportIsNotAVault proves the domain separation: an export written with
// the SAME passphrase as a vault still cannot be opened as vault.json, because
// the AAD ("dsmexp1" vs "dsmsv1") differs.
func TestExportIsNotAVault(t *testing.T) {
	dir, _ := tmpVaultPath(t)
	createVault(t, filepath.Join(dir, "vault.json"), testExportPW, []byte(testExportPlaintext))

	data := encryptTestExport(t)
	exportPath := filepath.Join(dir, "config.shelve")
	if err := os.WriteFile(exportPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	v := New()
	if _, err := v.Open(exportPath, testExportPW); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("export opened as a vault: %v, want ErrWrongPassword", err)
	}
}

// TestVaultIsNotAnExport is the mirror: vault.json (no "format" field) is not a
// readable export envelope.
func TestVaultIsNotAnExport(t *testing.T) {
	_, path := tmpVaultPath(t)
	createVault(t, path, testExportPW, []byte(testExportPlaintext))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptWithPassword(data, testExportPW); !errors.Is(err, ErrUnsupportedExport) {
		t.Fatalf("vault opened as an export: %v, want ErrUnsupportedExport", err)
	}
}
