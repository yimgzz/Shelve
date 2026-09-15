package vault

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Export/import envelope (master plan §4, plan config-export-import).
//
// A `.shelve` file re-encrypts the vault payload plus settings under its own
// passphrase, using the SAME Argon2id + AES-256-GCM scheme as vault.json but a
// distinct AAD ("dsmexp1") so the two ciphertext domains can never be confused.
// The envelope carries enough metadata (format, version, KDF/enc specs, salt,
// nonce, ciphertext) to be self-contained and portable between machines.
const (
	// exportFormat is the envelope discriminator (must match exactly).
	exportFormat = "shelve-export"
	// ExportVersion is the on-disk export format version.
	ExportVersion = 1
	// exportAAD domain-separates export ciphertexts from vault.json's aad.
	exportAAD = "dsmexp1"
)

// Typed errors for the export envelope. Messages never contain the passphrase
// or any key/plaintext material (master plan §8.3).
var (
	// ErrExportCorrupt: the file is not a readable export envelope (bad
	// JSON, unknown algorithm, malformed salt/nonce/ciphertext or
	// out-of-range KDF parameters).
	ErrExportCorrupt = errors.New("export: file is corrupt or not a shelve-export envelope")
	// ErrExportWrongPassword: the envelope is structurally valid but the
	// GCM auth check failed (wrong passphrase; tampering reports the same).
	ErrExportWrongPassword = errors.New("export: wrong passphrase")
	// ErrUnsupportedExport: the format or format version is not supported
	// by this build.
	ErrUnsupportedExport = errors.New("export: unsupported format or version")
)

// ExportEnvelope is the `.shelve` file shape: a readable header plus the
// base64 ciphertext of the JSON contents.
type ExportEnvelope struct {
	Format    string  `json:"format"`    // "shelve-export"
	V         int     `json:"v"`         // ExportVersion
	App       string  `json:"app"`       // internal/app.Version at export time
	CreatedAt string  `json:"createdAt"` // RFC3339 UTC
	KDF       KDFSpec `json:"kdf"`
	Enc       EncSpec `json:"enc"`
	Ct        string  `json:"ct"`
}

// EncryptWithPassword encrypts plaintext under password with a fresh 16 B
// salt and 12 B nonce, returning the JSON export envelope (no trailing
// newline). It reuses the vault KDF parameters and GCM primitive with the
// export AAD. appVersion is recorded in the header only.
func EncryptWithPassword(plaintext []byte, password string, appVersion string) ([]byte, error) {
	salt := make([]byte, kdfSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	key := derive(password, salt, kdfTime, kdfMemory, kdfThreads)
	defer zero(key)

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	ct, err := sealGCMWithAAD(key, nonce, plaintext, []byte(exportAAD))
	if err != nil {
		return nil, err
	}

	return json.Marshal(ExportEnvelope{
		Format:    exportFormat,
		V:         ExportVersion,
		App:       appVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		KDF: KDFSpec{
			Alg:     kdfAlg,
			Salt:    b64encode(salt),
			Time:    kdfTime,
			Memory:  kdfMemory,
			Threads: kdfThreads,
		},
		Enc: EncSpec{Alg: encAlg, Nonce: b64encode(nonce)},
		Ct:  b64encode(ct),
	})
}

// DecryptWithPassword parses and validates an export envelope, derives the key
// from password and returns the decrypted contents. The derived key is
// zeroized on every path (master plan §8.3).
func DecryptWithPassword(data []byte, password string) ([]byte, error) {
	var env ExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, ErrExportCorrupt
	}
	if env.Format != exportFormat || env.V != ExportVersion {
		return nil, ErrUnsupportedExport
	}
	if env.KDF.Alg != kdfAlg || env.Enc.Alg != encAlg {
		return nil, ErrExportCorrupt
	}
	if env.KDF.Time < 1 || env.KDF.Time > maxKDFTime ||
		env.KDF.Memory < 1024 || env.KDF.Memory > maxKDFMemory ||
		env.KDF.Threads < 1 || env.KDF.Threads > maxKDFThreads {
		return nil, ErrExportCorrupt
	}
	salt, err := b64decode(env.KDF.Salt)
	if err != nil || len(salt) != kdfSaltSize {
		return nil, ErrExportCorrupt
	}
	nonce, err := b64decode(env.Enc.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return nil, ErrExportCorrupt
	}
	ct, err := b64decode(env.Ct)
	if err != nil {
		return nil, ErrExportCorrupt
	}

	key := derive(password, salt, env.KDF.Time, env.KDF.Memory, env.KDF.Threads)
	plaintext, err := openGCMWithAAD(key, nonce, ct, []byte(exportAAD))
	zero(key)
	if err != nil {
		return nil, ErrExportWrongPassword
	}
	return plaintext, nil
}
