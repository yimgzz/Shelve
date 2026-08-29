package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"dummy-ssh-manager/internal/model"
)

// writeKeyFile writes a PEM block to a 0600 file under t.TempDir and
// returns its path.
func writeKeyFile(t *testing.T, block *pem.Block) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func genEd25519PEM(t *testing.T) (*pem.Block, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return block, pub
}

func genRSAPEM(t *testing.T, passphrase []byte) (*pem.Block, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == nil {
		block, err = ssh.MarshalPrivateKey(priv, "test-key")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "test-key", passphrase)
	}
	if err != nil {
		t.Fatal(err)
	}
	return block, priv
}

func mustSSHPublicKey(t *testing.T, pub any) ssh.PublicKey {
	t.Helper()
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// authorizedKeyLine returns the canonical "type base64" authorized_keys
// line for key (no trailing newline).
func authorizedKeyLine(t *testing.T, key ssh.PublicKey) string {
	t.Helper()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestAuthMethodsPassword(t *testing.T) {
	methods, err := AuthMethods(model.Auth{Type: model.AuthPassword, Password: "pw-123"}, nil)
	if err != nil {
		t.Fatalf("AuthMethods(password) = %v, want success", err)
	}
	if len(methods) != 1 || methods[0] == nil {
		t.Fatalf("want exactly 1 non-nil method, got %d", len(methods))
	}
}

func TestAuthMethodsUnknownType(t *testing.T) {
	if _, err := AuthMethods(model.Auth{Type: model.AuthType(99)}, nil); err == nil {
		t.Fatal("unknown auth type must be an error")
	}
}

func TestAuthMethodsKeyEd25519(t *testing.T) {
	block, pub := genEd25519PEM(t)
	path := writeKeyFile(t, block)
	want := authorizedKeyLine(t, mustSSHPublicKey(t, pub))

	methods, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, nil)
	if err != nil {
		t.Fatalf("unencrypted ed25519 key must parse: %v", err)
	}
	if len(methods) != 1 || methods[0] == nil {
		t.Fatalf("want exactly 1 non-nil method, got %d", len(methods))
	}
	signer, err := parseKeyFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := authorizedKeyLine(t, signer.PublicKey()); got != want {
		t.Fatalf("signer key = %s, want %s", got, want)
	}

	// A supplied passphrase on an unencrypted key is harmless.
	ignored := "some-passphrase"
	if _, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, &ignored); err != nil {
		t.Fatalf("passphrase on unencrypted key must be ignored, got %v", err)
	}
}

func TestAuthMethodsKeyRSAWithPassphrase(t *testing.T) {
	const pass = "correct-horse"
	block, priv := genRSAPEM(t, []byte(pass))
	path := writeKeyFile(t, block)
	want := authorizedKeyLine(t, mustSSHPublicKey(t, &priv.PublicKey))

	got, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, strPtr(pass))
	if err != nil {
		t.Fatalf("RSA key with correct passphrase must parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 method, got %d", len(got))
	}
	signer, err := parseKeyFile(path, strPtr(pass))
	if err != nil {
		t.Fatal(err)
	}
	if line := authorizedKeyLine(t, signer.PublicKey()); line != want {
		t.Fatalf("signer key = %s, want %s", line, want)
	}
}

func TestAuthMethodsKeyWrongPassphrase(t *testing.T) {
	block, _ := genRSAPEM(t, []byte("correct-horse"))
	path := writeKeyFile(t, block)

	_, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, strPtr("wrong-horse"))
	if !errors.Is(err, ErrKeyPassphraseWrong) {
		t.Fatalf("want ErrKeyPassphraseWrong, got %v", err)
	}
}

func TestAuthMethodsKeyEncryptedNoPassphrase(t *testing.T) {
	block, _ := genRSAPEM(t, []byte("correct-horse"))
	path := writeKeyFile(t, block)

	_, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, nil)
	var req *ErrKeyPassphraseRequired
	if !errors.As(err, &req) {
		t.Fatalf("want *ErrKeyPassphraseRequired, got %v", err)
	}
	if req.KeyPath != path {
		t.Fatalf("KeyPath = %s, want %s", req.KeyPath, path)
	}
}

func TestAuthMethodsKeyMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, nil)
	if err == nil {
		t.Fatal("missing key file must be an error")
	}
	want := "ssh key file not found: " + path
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestAuthMethodsKeyGarbageFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage")
	if err := os.WriteFile(path, []byte("this is definitely not a private key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, nil)
	var f *ErrKeyFormat
	if !errors.As(err, &f) {
		t.Fatalf("want *ErrKeyFormat, got %v", err)
	}
	if f.Path != path {
		t.Fatalf("Path = %s, want %s", f.Path, path)
	}
	if f.Detail == "" {
		t.Fatal("Detail must carry the parser message")
	}
}

// Security invariant (master plan §8.3): no error message above may leak
// key material; the passphrase value must never appear in any error.
func TestAuthMethodsErrorsNeverLeakPassphrase(t *testing.T) {
	const secret = "topsecret-passphrase-xyz"
	block, _ := genRSAPEM(t, []byte(secret))
	path := writeKeyFile(t, block)

	_, errWrong := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: path}, strPtr("not-"+secret))
	if errWrong == nil || strings.Contains(errWrong.Error(), secret) {
		t.Fatalf("wrong-passphrase error (nil=%v) must not leak passphrase: %v", errWrong == nil, errWrong)
	}

	garbage := filepath.Join(t.TempDir(), "garbage")
	if err := os.WriteFile(garbage, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errGarbage := AuthMethods(model.Auth{Type: model.AuthKey, KeyPath: garbage}, strPtr(secret))
	if errGarbage == nil || strings.Contains(errGarbage.Error(), secret) {
		t.Fatalf("format error (nil=%v) must not leak file contents: %v", errGarbage == nil, errGarbage)
	}
}

func strPtr(s string) *string { return &s }
