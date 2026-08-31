# Phase 3b — SSH auth builders & host-key callback (`sshx`)

**Type:** Backend. **Prereq:** 3a. **Sub-plan 2/5 of old Phase 3. Next: 3c.**

**Master plan:** `plans/1787912690309-master-plan.md` — read **§2 (D5, A1, A2), §4, §5 (event contract row `vault:hostkey-prompt`), §8 (items 5–6), §9 (unit tests)** before starting.

**Scope guard (important):** add two small files + tests under `internal/sshx/`. NO `internal/sshengine`, NO wailsvc changes, NO containers.

## Goal
`model.Auth` → `[]ssh.AuthMethod` conversion, and the TOFU host-key callback with an asynchronous approval hook (master §2 A1/A2), both fully unit-tested. The 3c engine consumes exactly these two entry points.

## Tasks
1. `internal/sshx/auth.go`:
   - `AuthMethods(auth model.Auth, keyPassphrase *string) ([]ssh.AuthMethod, error)`:
     - `AuthPassword` → `ssh.Password(...)`.
     - `AuthKey`: read `KeyPath` (absolute path; friendly errors: not found → `"ssh key file not found: <path>"`, unreadable → `"ssh key file not readable: <path>"`); parse via `ssh.ParseRawPrivateKeyWithOptions(data, nil, passphrase, nil)`; encrypted key with no passphrase provided → typed `ErrKeyPassphraseRequired{KeyPath string}` (the 3c engine shows the UI prompt on this); wrong passphrase → typed `ErrKeyPassphraseWrong`; unparseable → typed `ErrKeyFormat{Path, Detail string}`.
   - Keep `internal/sshx/doc.go` promises consistent.
2. `internal/sshx/hostkey.go`:
   - `type HostKeyApprover interface { PromptHostKey(host string, port int, keyType, keyB64, fingerprint string) (<-chan bool, error) }` — the 3c engine implements it (renders the `vault:hostkey-prompt` UI); the channel delivers true=accept / false=reject; the approver enforces its own timeout.
   - `NewHostKeyCallback(kh *knownhosts.Manager, appr HostKeyApprover) ssh.HostKeyCallback` behavior for (host, port, remoteKey):
     - keyType/b64 form: `keyType = remoteKey.Type()`; `keyB64 = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(remoteKey)))`.
     - `kh.Has(host, port, keyType, keyB64)` → accept (known-good, no events).
     - Entries exist for (host, port) but none matches → typed error `ErrHostKeyChanged{Host, Port, Expected, Actual string}` where Expected/Actual are `knownhosts.Fingerprint` values (never log keys).
     - No entries → `appr.PromptHostKey(...)`; block on the returned channel; on accept → `kh.Add(host, port, keyType, keyB64)` (on `ErrKeyConflict` treat as host-key changed) + `kh.Save()`; on reject → error `"host key rejected by user"`.
   - The callback must be safe for concurrent calls across connections (knownhosts `Manager` is already locked; approver contract: one in-flight prompt per (host,port) is NOT guaranteed — document that the 3c engine serializes prompts per tab).
3. Unit tests:
   - Auth builders: password; ed25519 key without passphrase; RSA key with passphrase (correct + wrong); missing key file; garbage key file. Generate keys in-test (`crypto/ed25519`, `ssh.MarshalPrivateKey`).
   - Host-key callback (temp-file known_hosts): known-good → accept; known-different → `ErrHostKeyChanged` with correct `SHA256:...` fingerprints; unknown + fake approver accepts → accepted AND entry persisted to disk (assert file content); unknown + rejects → rejected error; approver returns error → error propagates unchanged.

## Verification
- `make test` (race) green; `make lint` clean.

## Exit criteria
`sshx.AuthMethods` and `sshx.NewHostKeyCallback` complete with the typed errors and full unit-test coverage above; both ready for the 3c engine integration.
