package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/argon2"

	"shelve/internal/config"
)

const (
	// FileVersion is the on-disk envelope format version.
	FileVersion = 1
	// aad is the GCM authenticated data bound to every ciphertext
	// (master plan §8.2).
	aad = "dsmsv1"

	kdfAlg = "argon2id"
	encAlg = "aes-256-gcm"

	// KDF parameters, master plan §8.2.
	kdfTime    = 3
	kdfMemory  = 65536 // KiB = 64 MiB
	kdfThreads = 4

	kdfSaltSize = 16
	keySize     = 32
	nonceSize   = 12
	tempDirName = "tmp"
)

// Typed errors. Error messages never contain passwords or key material
// (master plan §8.3).
var (
	// ErrVaultExists: Create while a vault file is already present.
	ErrVaultExists = errors.New("vault: already exists")
	// ErrVaultNotFound: Open on a path without a vault file.
	ErrVaultNotFound = errors.New("vault: file not found")
	// ErrWrongPassword: the envelope is valid but the GCM auth check
	// failed (wrong master password; ciphertext tampering is
	// indistinguishable to a single-key AEAD and reports the same).
	ErrWrongPassword = errors.New("vault: wrong master password")
	// ErrCorruptVault: the file is not a readable v1 envelope (bad
	// JSON, unknown version/algorithm, malformed salt/nonce/ciphertext
	// or out-of-range KDF parameters).
	ErrCorruptVault = errors.New("vault: file is corrupt or not a v1 vault")
	// ErrLocked: an operation requiring the in-memory key while locked.
	ErrLocked = errors.New("vault: locked")
)

// Bounds sanity-checked against file KDF params before deriving (a
// tampered file must not drive an out-of-range allocation).
const (
	maxKDFTime    = 1000
	maxKDFMemory  = 256 * 1024
	maxKDFThreads = 32
)

// Status is the vault state machine (master plan §4 app startup).
type Status int

// Vault states.
const (
	// StatusCreateNeeded: no vault file yet (first run).
	StatusCreateNeeded Status = iota
	// StatusLocked: file present, key not derived.
	StatusLocked
	// StatusUnlocked: key in memory, Save available.
	StatusUnlocked
)

func (s Status) String() string {
	switch s {
	case StatusUnlocked:
		return "unlocked"
	case StatusLocked:
		return "locked"
	default:
		return "create"
	}
}

// KDFSpec describes the Argon2id derivation of this file.
type KDFSpec struct {
	Alg     string `json:"alg"`
	Salt    string `json:"salt"` // 16 B, base64
	Time    int    `json:"time"`
	Memory  int    `json:"memory"` // KiB
	Threads int    `json:"threads"`
}

// EncSpec describes the AES-256-GCM encryption of this file.
type EncSpec struct {
	Alg   string `json:"alg"`
	Nonce string `json:"nonce"` // 12 B, base64; fresh per write
}

// Envelope is vault.json: header + base64 ciphertext of the JSON payload
// (master plan §4).
type Envelope struct {
	V   int     `json:"v"`
	KDF KDFSpec `json:"kdf"`
	Enc EncSpec `json:"enc"`
	Ct  string  `json:"ct"`
}

// Vault is the encrypted credential store. The derived key lives in
// memory only between Open/.Create and Lock (master plan §2 D3). All
// methods are safe for concurrent use.
type Vault struct {
	mu     sync.Mutex
	path   string
	env    Envelope
	key    []byte
	status Status
}

// New returns an uninitialized Vault. Call Probe for the startup state
// machine, then Create (first run) or Open (subsequent runs).
func New() *Vault {
	return &Vault{}
}

// Probe binds the vault file path and determines the startup state
// (master plan §4): no file → StatusCreateNeeded, file present →
// StatusLocked. An unreadable/malformed file stays StatusLocked and
// surfaces as ErrCorruptVault at Open (the app must never overwrite a
// vault it cannot decrypt).
func (v *Vault) Probe(path string) (Status, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	st := StatusLocked
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			st = StatusCreateNeeded
		} else {
			return StatusLocked, err
		}
	}
	v.path = path
	v.status = st
	return st, nil
}

// Status reports the current state.
func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

// IsUnlocked reports whether the key is currently in memory.
func (v *Vault) IsUnlocked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status == StatusUnlocked
}

// Create writes a fresh first-run vault file and unlocks it in memory:
// random 16 B salt, Argon2id (m=64 MiB, t=3, p=4) → 32 B key, fresh
// 12 B nonce, AES-256-GCM (AAD "dsmsv1"), atomic 0600 write
// (master plan §8.2, §8.7).
func (v *Vault) Create(path, masterPassword string, payload []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, err := os.Stat(path); err == nil {
		return ErrVaultExists
	} else if !os.IsNotExist(err) {
		return err
	}

	salt := make([]byte, kdfSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	key := v.derive(masterPassword, salt, kdfTime, kdfMemory, kdfThreads)

	env := Envelope{
		V: FileVersion,
		KDF: KDFSpec{
			Alg:     kdfAlg,
			Salt:    b64encode(salt),
			Time:    kdfTime,
			Memory:  kdfMemory,
			Threads: kdfThreads,
		},
		Enc: EncSpec{Alg: encAlg},
	}
	if err := v.sealInto(&env, key, payload); err != nil {
		zero(key)
		return err
	}

	data, err := json.Marshal(env)
	if err != nil {
		zero(key)
		return err
	}
	data = append(data, '\n')
	if err := config.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), data); err != nil {
		zero(key)
		return fmt.Errorf("vault: %w", err)
	}

	v.path = path
	v.env = env
	v.key = key // now owned by the Vault until Lock
	v.status = StatusUnlocked
	return nil
}

// Open reads the vault file, derives the key from the master password
// and returns the decrypted payload. On success the key is kept in
// memory for Save. Failure leaves the vault locked.
func (v *Vault) Open(path, masterPassword string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.openLocked(path, masterPassword)
}

func (v *Vault) openLocked(path, masterPassword string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrVaultNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}

	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, ErrCorruptVault
	}
	salt, nonce, ct, err := env.validated()
	if err != nil {
		return nil, ErrCorruptVault
	}

	key := v.derive(masterPassword, salt, env.KDF.Time, env.KDF.Memory, env.KDF.Threads)

	payload, err := openGCM(key, nonce, ct)
	if err != nil {
		// No ownership transfer on failure: zeroize the material that
		// never leaves this scope. (A `defer zero(key)` would not work
		// here — defer evaluates its argument at registration time, so
		// it would zeroize the slice after ownership has passed to the
		// Vault.)
		zero(key)
		return nil, ErrWrongPassword
	}

	// On success the key is owned by the Vault until Lock.
	v.path = path
	v.env = env
	v.key = key
	v.status = StatusUnlocked
	return payload, nil
}

// Save re-encrypts the payload with the in-memory key — fresh random
// nonce per write (master plan §8.2) — and rewrites the file atomically.
func (v *Vault) Save(payload []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.status != StatusUnlocked || v.key == nil {
		return ErrLocked
	}
	env := v.env
	if err := v.sealInto(&env, v.key, payload); err != nil {
		return err
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := config.WriteFileAtomic(filepath.Dir(v.path), filepath.Base(v.path), data); err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	v.env = env
	return nil
}

// Lock zeroizes the in-memory key, sweeps leftover SFTP temp files
// (master plan §8.8, best effort) and marks the vault locked. It is a
// safe no-op when already locked.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	zero(v.key)
	v.key = nil
	if v.status == StatusUnlocked {
		v.status = StatusLocked
	}
	sweepTempDir(v.path)
}

// sealInto encrypts payload with key and a fresh random nonce into env.
func (v *Vault) sealInto(env *Envelope, key, payload []byte) error {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	ct, err := sealGCM(key, nonce, payload)
	if err != nil {
		return err
	}
	env.Enc.Nonce = b64encode(nonce)
	env.Ct = b64encode(ct)
	return nil
}

func (v *Vault) derive(password string, salt []byte, time, memory, threads int) []byte {
	pw := make([]byte, len(password))
	copy(pw, password)
	defer zero(pw)
	return argon2.IDKey(pw, salt, uint32(time), uint32(memory), uint8(threads), keySize)
}

// validated decodes and sanity-checks the envelope fields, returning
// error only for structural corruption (bad version, algorithm, salt or
// nonce, or out-of-range KDF params).
func (e Envelope) validated() (salt, nonce, ct []byte, err error) {
	if e.V != FileVersion || e.KDF.Alg != kdfAlg || e.Enc.Alg != encAlg {
		return nil, nil, nil, errors.New("bad version or algorithm")
	}
	if e.KDF.Time < 1 || e.KDF.Time > maxKDFTime ||
		e.KDF.Memory < 1024 || e.KDF.Memory > maxKDFMemory ||
		e.KDF.Threads < 1 || e.KDF.Threads > maxKDFThreads {
		return nil, nil, nil, errors.New("out-of-range kdf parameters")
	}
	if salt, err = b64decode(e.KDF.Salt); err != nil || len(salt) != kdfSaltSize {
		return nil, nil, nil, errors.New("bad salt")
	}
	if nonce, err = b64decode(e.Enc.Nonce); err != nil || len(nonce) != nonceSize {
		return nil, nil, nil, errors.New("bad nonce")
	}
	if ct, err = b64decode(e.Ct); err != nil {
		return nil, nil, nil, errors.New("bad ciphertext")
	}
	return salt, nonce, ct, nil
}

// sweepTempDir removes <vaultdir>/tmp/* best effort (master plan §8.8).
func sweepTempDir(vaultPath string) {
	if vaultPath == "" {
		return
	}
	dir := filepath.Join(filepath.Dir(vaultPath), tempDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	_ = os.Remove(dir)
}

func sealGCM(key, nonce, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, []byte(aad)), nil
}

func openGCM(key, nonce, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return gcm.Open(nil, nonce, ct, []byte(aad))
}

func b64encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func b64decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// zero blanks a buffer (master plan §8.3 memory hygiene).
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
