// Command smol is the headless Phase 2 vault smoke tool: against a
// throwaway temp dir it runs create → unlock → modify → lock and prints
// PASS. Gated development tool (make smoke-vault); not part of the
// shipped app (the app binary is the repo-root main package).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"dummy-ssh-manager/internal/vault"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "smol: FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	dir, err := os.MkdirTemp("", "dsm-smoke-*")
	if err != nil {
		fail("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "vault.json")
	pw := "smoke-master-password"

	payload1 := []byte(`{"root":[],"folders":[],"sessions":[]}`)
	payload2 := []byte(`{"root":["smoke-node"],"folders":[],"sessions":[]}`)

	// Create (first run) + verify perms and unlock state.
	v1 := vault.New()
	if err := v1.Create(path, pw, payload1); err != nil {
		fail("create: %v", err)
	}
	if !v1.IsUnlocked() {
		fail("not unlocked after create")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		fail("vault file perms: %v %v", fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		fail("vault dir perms: %v %v", fi, err)
	}

	// Probe: file present → locked pending.
	v2 := vault.New()
	if st, err := v2.Probe(path); err != nil || st != vault.StatusLocked {
		fail("probe: %v %v", st, err)
	}

	// Unlock + payload round-trip.
	out, err := v2.Open(path, pw)
	if err != nil {
		fail("open: %v", err)
	}
	if string(out) != string(payload1) {
		fail("payload round-trip mismatch")
	}

	// Modify + save, re-open in a fresh instance.
	if err := v2.Save(payload2); err != nil {
		fail("save: %v", err)
	}
	v3 := vault.New()
	out2, err := v3.Open(path, pw)
	if err != nil {
		fail("re-open: %v", err)
	}
	if string(out2) != string(payload2) {
		fail("payload after save mismatch")
	}

	// Lock: no more saves, no re-unlock without the password.
	v3.Lock()
	if v3.IsUnlocked() {
		fail("still unlocked after lock")
	}
	if err := v3.Save(payload1); err != vault.ErrLocked {
		fail("save after lock: got %v, want ErrLocked", err)
	}
	if _, err := vault.New().Open(path, "wrong-password"); err == nil {
		fail("wrong password accepted")
	}

	fmt.Println("PASS")
}
