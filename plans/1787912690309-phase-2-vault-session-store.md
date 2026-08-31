# Phase 2 — Encrypted Vault, Session Store & Service Bindings

**Type:** Backend. **Prereq: Phase 1.**

**Master plan:** `plans/1787912690309-master-plan.md` — read **§1, §2 (D3, A1, A8, A10), §4 (all), §5 (services/DTOs), §8 (all), §9 (unit)** before starting.

## Goal
The complete data layer: model + validation, settings persistence, Argon2id/AES-256-GCM vault with first-run/unlock/lock lifecycle, in-memory tree store with CRUD/move/order, OpenSSH `known_hosts` manager, all Wails service bindings for tree/settings (frontend comes later — services must be callable/testable headlessly).

## Tasks
1. `internal/model`:
   - types per master §4 (`Folder`, `Session`, `Auth`, `JumpHost`, `AuthType` constants).
   - `Validate()` for each node with precise errors (field name + rule); host regex (hostname / IPv4 / IPv6), port range, auth XOR rule, jump-host field rules.
   - ULID ID generation helper.
2. `internal/store`:
   - `Store` (RWMutex) with `folders`/`sessions` maps + per-parent ordered ID lists.
   - Ops: `CreateFolder`, `RenameFolder`, `CreateSession`, `UpdateSession`, `DuplicateSession` (copies into same folder, name suffix “ (copy)”), `DeleteNode` (recursive delete; returns count of removed sessions+folders), `MoveNode` (re-index; forbid moving a folder into its own descendant), `Tree() -> [NodeDTO]`.
   - Persistence trigger: callback to `vault.Save()` debounced 300 ms; `Flush()` synchronous.
   - Load: reconstruct from decrypted payload.
   - 300-session fixture generator (test-only, exported in `internal/model/fixtures.go` or a test helper package).
3. `internal/vault`:
   - Envelope struct per master §4; `Create(path, masterPw, payload)`, `Open(path, masterPw) -> payload` (Argon2id m=64MiB t=3 p=4; AES-256-GCM; AAD `"dsmsv1"`), `Save(payload)` using the in-memory key, `Lock()` (zeroize key), `IsUnlocked()`.
   - Wrong password / tampered ciphertext → distinct typed errors (never log the password).
   - File perms 0700 dir / 0600 vault file; atomic writes.
   - `New()` is called by `internal/app` bootstrap; app startup state machine: no file → “create” pending; file present → “locked” pending. (UI not yet — state is exposed via `VaultService`.)
4. `internal/sshx/knownhosts`:
   - OpenSSH subset parser (plain + `[host]:port`, ssh-ed25519/ssh-rsa/rsa lines; skip comments/whitespace; hashed/`@cert-authority`/wildcard lines recognized-but-inert) + `Lookup(host, port)`, `Add(host, port, keyType, key)`, `Save()` (rewrite clean lines only, 0600), `Fingerprint(key) -> "SHA256:..."` display form.
5. `internal/wailsvc` — add services (constructor injection of store/vault/settings; NO wails import beyond what the template already uses for registration; emit events via the app handle passed in):
   - `VaultService`: `Status`, `CreateVault`, `Unlock`, `Lock` (Lock must be a no-op safe when already locked).
   - `SessionService`: `Tree`, `CreateFolder`, `RenameFolder`, `MoveNode`, `DeleteNode`, `CreateSession`, `UpdateSession`, `DuplicateSession`, `ValidateExtraArgs` (delegate to Phase 3 parser — for now a placeholder that accepts `""` and rejects anything else with “parser not wired yet”), `TestConnection` (placeholder returning a stable “engine not wired yet” error).
   - `AppService`: `GetSettings`, `SaveSettings`, `GetVersion`.
   - DTOs in `dto.go`: tree nodes `{kind, id, name, children}`; session DTOs strip secrets (expose `AuthType`, `HasPassword bool`, `KeyPath`, jump hosts with `HasPassword`); settings DTO mirrors §4.
   - Register all services in `main.go` (Phase 1 pattern).
6. Unit tests (§8 + §9 of master):
   - vault: create→unlock round-trip; wrong pw fails; flipped ciphertext byte fails; lock zeroes (assert no re-unlock without pw); save is atomic (simulate mid-write crash: pre-existing file intact); perms 0600/0700.
   - store: create/rename/move (incl. into-descendant rejection), duplicate, recursive delete counts, ordering, Tree shape with 300-node fixture (search-free ops timing < 50 ms wall).
   - known_hosts: parse matrix, add/save idempotence, fingerprint format.
   - model: each validation rule (table tests).
   - **secret-leak guard**: assert `settings.json` sample + a serialized `Tree()` output never contain a password substring.
   - Run with `-race` in container.

## Verification
- `make test` (race) green in a clean container.
- Headless smoke (add `make smoke-vault` target): a small Go test-binary or `go run` tool `internal/vault/smol/main.go` (gated, not shipped) that against `TMPDIR` does create→unlock→modify→lock and prints PASS. Run inside container.
- `wails3 build` still succeeds; bindings regenerated for new services (verify `frontend/bindings/` contains the new service modules — content check only, no frontend work yet).

## Exit criteria
All §8 invariants unit-tested and passing; services callable from generated JS bindings with correct DTO shapes; zero plaintext secrets on disk (verified by tests + manual `rg` over the config dir after the smoke run).
