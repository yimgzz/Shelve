# Plan P006 — Jump Host Manager (Saved One-Hop Jump Hosts)

> Status: Approved (owner, 2026-09-02)
> Scope: new SavedJumpHost entity + vault storage + services + session-editor dropdown + manager UI; inline jump hosts stay unchanged
> Owner: Architect (this document) → implemented in Code mode

## 1. Goal

Let the user define reusable **saved jump hosts** — a single hop with `host + port + login + password OR key path` —
store them inside the encrypted vault, and pick one from a dropdown when creating/editing an SSH session.
Inline jump-host entry stays fully supported for ad-hoc chains.

## 2. Master-plan references (read before implementation)

- [`plans/1787912690309-master-plan.md`](plans/1787912690309-master-plan.md)
  - **§4 Data Model & Storage** — `vault.json` envelope; encrypted `Payload`
    (`{"folders":[...],"sessions":[...],"credentials":[...]}`) gains a `savedJumpHosts` slice (additive — old
    vaults load fine); the `Auth` password-XOR-key rule and per-field validation; atomic debounced writes.
  - **§5 Architecture** — Wails service surface + DTO contract; secrets never leave the vault in read views
    (`HasPassword`/`AuthType` only); new `JumpHostService` fits beside `CredentialService`.
  - **§8 Security Requirements (binding)** — no plaintext creds on disk; only `vault.json` stores secrets;
    `settings.json`/`known_hosts` stay secret-free; no plaintext over IPC.
  - **§2 → A2** — key-file passphrases are never stored; prompted once per connection.
- [`plans/P003-credential-manager.md`](plans/P003-credential-manager.md) — the exact precedent this plan mirrors:
  reference + inline-snapshot semantics, store soft-null on delete, service-layer resolution, secret-free DTOs,
  manager UI + editor dropdown.

## 3. Current state (as-is)

- Sessions carry an ordered inline chain `Session.JumpHosts []JumpHost` ([`internal/model/model.go`](internal/model/model.go:54));
  the engine builds hops from it in [`buildSessionChain()`](internal/sshengine/live.go:279) — structured jumps first,
  Extra Args `ProxyJump` appended, then the target.
- Named credentials (plan P003) exist: `model.Credential`, `Session.CredentialID`, store CRUD + soft-null on delete,
  `CredentialService`, secret-free `CredentialDTO`, frontend manager dialog + session-editor dropdown.
- Connect-time resolution happens in the service layer before the engine sees the session:
  [`TerminalService.Connect`](internal/wailsvc/terminalservice.go:39) and
  [`SessionService.TestConnection`](internal/wailsvc/sessionservice.go:214) call `resolveSessionCredential`.

## 4. Target design (to-be)

### 4.1 Semantics (owner decision, approved)

A session gains an optional **`JumpHostRef`** (one saved jump host). While set:

- the saved jump host is the **single source of truth** at connect/test time — its hop **replaces the entire
  inline chain** (`JumpHosts` is treated as `[savedHop]`);
- the inline rows are kept as a self-contained **snapshot** (rewritten from the saved host on every save) that
  takes over when the reference is cleared (e.g. the saved host is deleted — the store soft-nulls the ref).

Sessions without a ref behave exactly as today (inline chain, possibly multi-hop, plus Extra Args ProxyJump).

### 4.2 Data model (`internal/model`)

```go
// SavedJumpHost is a named, reusable single-hop jump host stored in the
// encrypted vault and referenced by sessions (plan P006).
type SavedJumpHost struct {
    ID   string `json:"id"`             // ULID (model.NewID)
    Name string `json:"name"`           // display name; required
    Host string `json:"host"`           // hostname / IPv4 / IPv6; required
    Port int    `json:"port"`           // 1–65535 (22 default)
    User string `json:"user"`           // login; required
    Auth Auth   `json:"auth"`           // password XOR key path (same rule as sessions)
}
```

- Encrypted `Payload` gains `SavedJumpHosts []SavedJumpHost json:"savedJumpHosts"`.
- `Session` gains `JumpHostRef string json:"jumpHostRef,omitempty"`. When set, connect/test resolve the hop from
  the saved jump host (see §4.5); inline fields remain the fallback snapshot.
- Validation: `Name` non-empty; `Host` hostname/IPv4/IPv6; `Port` 1–65535; `User` non-empty; exactly one auth
  method (password XOR key). Session-level: `JumpHostRef` must reference an existing saved jump host when set
  (checked by the store/service layer, mirroring `CredentialID`).

### 4.3 Store (`internal/store`)

Add `model.SavedJumpHost` CRUD following the credential pattern (`CreateSavedJumpHost`, `UpdateSavedJumpHost`,
`DeleteSavedJumpHost`, `ListSavedJumpHosts`, `SavedJumpHost(id)`, `SavedJumpHostUsage(id)`); `Encode`/`Load`
include the `savedJumpHosts` slice; every mutation triggers the same debounced save. Delete semantics: deleting
a saved jump host **soft-nulls `JumpHostRef`** on every referencing session (returned as the affected count for
the A8-style confirm dialog) so sessions keep connecting with their inline snapshot. `CreateSession`/`UpdateSession`
enforce the ref-exists invariant (`checkJumpHostRefLocked`); `DuplicateSession` copies `JumpHostRef`.

### 4.4 Services & DTOs (`internal/wailsvc`)

New **`JumpHostService`** (registered in `main.go` alongside the others):
- `List() []SavedJumpHostDTO` — secret-free read view (`ID, Name, Host, Port, User, AuthType, HasPassword, KeyPath?`).
- `Create(input SavedJumpHostInput) string` / `Update(input)` / `Delete(id)` / `Get(id)` / `Usage(id) int`.
- Update password-merge rule (mirror of `CredentialService.Update`): empty incoming password on a password-auth
  saved host means "keep the current password"; switching to key auth never resurrects a stale password.

`SessionDTO`/`SessionInput` gain `jumpHostRef`. The read view exposes the reference only (never the underlying
secret). All secret-bearing inputs go straight into the encrypted vault — never echoed back (§8).

### 4.5 Resolution

- `resolveSessionJumpHost(st, sess)` (service layer, mirrors `resolveSessionCredential`): when
  `sess.JumpHostRef != ""`, look up the saved host and set `sess.JumpHosts = []JumpHost{savedHop}`; a dangling
  reference (externally-edited payload) falls back to the inline snapshot untouched.
- Applied in `TerminalService.Connect` and `SessionService.TestConnection` after `resolveSessionCredential`.
- `applyJumpHostSnapshot(sess)` on `CreateSession`/`UpdateSession` (mirrors `applyCredentialSnapshot`): when the
  ref is set, `sess.JumpHosts` is replaced with `[savedHop]` so the stored snapshot is valid and self-contained
  even though the frontend could not send the password (passwords never cross IPC read views).

### 4.6 Session editor integration

- In the existing "Jump hosts" section: a **"Saved jump host"** dropdown (from `JumpHostService.List()`) with
  options `None (inline jump hosts)` / each saved host, plus a "Manage…" button opening the manager in pick mode.
- Selecting a saved host replaces the inline row list with the saved hop (prefilled, marked "from saved jump
  host") and sets `jumpHostRef` on the draft. Selecting "None" clears the ref and restores the editable inline rows.
- Edit mode prefills the dropdown from `initial.jumpHostRef`; saving persists both the reference and the inline
  snapshot (backend keeps the saved host authoritative while referenced).

### 4.7 Manager UI

- A management modal (mirroring [`credential-dialog.ts`](frontend/src/components/credential-dialog.ts)) opened from
  the gear menu ("Jump hosts…") and from the session editor: list saved jump hosts (name, host, user, type,
  masked), with New / Edit / Delete (confirm via the existing `confirm` primitive, affected-session count).
- New `frontend/src/components/jump-host-dialog.ts` for create/edit, reusing `ui/dialog` + field helpers.

## 5. Implementation steps (todo)

1. `internal/model`: add `SavedJumpHost` type + `Payload.SavedJumpHosts` + `Session.JumpHostRef`; extend
   `Validate` (name, host, port, user, auth XOR; prefix `savedJumpHost`); unit tests (`model_test.go`).
2. `internal/store`: savedJumpHosts map + `New()` init, `Encode`/`Load` integration, CRUD + usage,
   soft-null delete returning affected count, `ErrSavedJumpHostNotFound`, `checkJumpHostRefLocked` in
   Create/UpdateSession, `DuplicateSession` copies `JumpHostRef`; unit tests (`store_test.go`).
3. `internal/wailsvc`: `SavedJumpHostDTO`/`SavedJumpHostInput` + conversions; `SessionDTO`/`SessionInput`
   gain `jumpHostRef` (`dto.go`); new `jumphostservice.go` (`JumpHostService` CRUD, password-merge,
   `resolveSessionJumpHost`, `applyJumpHostSnapshot`); service tests.
4. Wire resolution: `TerminalService.Connect` + `SessionService.TestConnection` call `resolveSessionJumpHost`;
   `CreateSession`/`UpdateSession` call `applyJumpHostSnapshot`.
5. `internal/app/app.go` field + getter; `main.go` Services list; regenerate bindings (`make build`/`make dev`).
6. `cmd/seed/main.go`: a few fixture saved jump hosts (password + key variants); some sessions reference them.
7. Frontend store/types: `SavedJumpHostDTO`/`SavedJumpHostInput`, `savedJumpHosts` state, `refreshSavedJumpHosts()`.
8. New `frontend/src/components/jump-host-dialog.ts` (list/create/edit/delete + pick mode).
9. `frontend/src/components/session-editor.ts`: "Saved jump host" dropdown + Manage button + `jumpHostRef` on the draft.
10. `frontend/src/components/gear.ts`: "Jump hosts…" menu item.
11. Security tests: saved-jump-host passwords never appear in `settings.json`/`known_hosts` or any read DTO
    (extend existing secret-free assertions). QA: `make test`, `make lint`; manual pass per §6.

## 6. Exit criteria

- Create/edit/delete named saved jump hosts (password and key variants) persisted **only** in the encrypted vault.
- A session can reference a saved jump host; connect and Test Connection resolve the hop from it; clearing the
  reference or deleting the saved host leaves the session working via its inline snapshot.
- Inline jump-host chains (multi-hop + Extra Args ProxyJump) behave exactly as before when no ref is set.
- No secret ever appears in settings, known_hosts, or any read DTO (§8 assertions green).
- Manager UI reachable from the gear menu and the session editor; destructive delete confirmed with the
  affected-session count.
- `make test`, `make lint` green (plus `make test-integration` for the connect-with-saved-jump-host path).

## 7. Non-goals (scope guard)

- Mixing a saved host with extra inline hops in one chain (owner chose full-chain replacement; revisit only on request).
- Saved multi-hop "profiles" (a saved entity is always exactly one hop).
- Credential reuse inside a saved jump host (auth is inline password XOR key path only — same rule as sessions).