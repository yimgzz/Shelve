# Plan P003 — Credential Manager (Named Login/Password & Login/Key Bundles)

> Status: Draft for review
> Scope: new Credential entity + vault storage + services + session-editor integration + manager UI
> Owner: Architect (this document) → implemented in Code mode

## 1. Goal

Let the user define reusable **named credentials** — either a `login + password` pair or a
`login + SSH key path` pair — store them inside the encrypted vault, and reference a saved
credential when creating/editing an SSH session so the auth is filled in (and kept as a
single source of truth).

## 2. Master-plan references (read before implementation)

- [`.kilo/plans/1787912690309-master-plan.md`](.kilo/plans/1787912690309-master-plan.md)
  - **§4 Data Model & Storage** — `vault.json` envelope; the encrypted `Payload`
    (`{"folders":[...],"sessions":[...]}`) must gain a `credentials` slice; the `Auth`
    password-XOR-key rule and per-field validation; atomic debounced writes; schema versioning note (`"v":1`).
  - **§5 Architecture** — Wails service surface + DTO contract; secrets must never leave the vault
    in read views (`Tree()`, credential lists expose `HasPassword`/`AuthType` only); new
    `CredentialService` fits beside `SessionService`.
  - **§8 Security Requirements (binding)** — no plaintext creds on disk; only `vault.json` stores
    secrets; `settings.json`/`known_hosts` stay secret-free (audited); atomic writes; memory hygiene/zeroization;
    no plaintext over IPC.
  - **§2 → A2** — key-file passphrases are never stored; prompted once per connection. Credentials
    follow the same rule for key-based bundles.
- Phase plans to re-check:
  - [`.kilo/plans/1787912690309-phase-2-vault-session-store.md`](.kilo/plans/1787912690309-phase-2-vault-session-store.md) — store CRUD/persistence trigger, payload encode/load, model validation.
  - [`.kilo/plans/1788003650840-phase-4b-tree-search-editor.md`](.kilo/plans/1788003650840-phase-4b-tree-search-editor.md) — session editor modal + `UpdateSession` password-merge rule (relevant for how a picked credential populates/references auth).
  - [`.kilo/plans/1788003650840-phase-4d-settings-lock-shortcuts.md`](.kilo/plans/1788003650840-phase-4d-settings-lock-shortcuts.md) — dialog/context-menu/confirm primitives reused by the credential manager UI.
  - [`.kilo/plans/1787912690309-master-plan.md#58`](.kilo/plans/1787912690309-master-plan.md) §8 again at implementation time.

## 3. Current state (as-is)

- Auth is inline on sessions/jump hosts only: `model.Auth{Type, Password, KeyPath}` in
  [`internal/model/model.go`](internal/model/model.go); the encrypted `Payload` has just
  `Root / Folders / Sessions`.
- [`internal/store/store.go`](internal/store/store.go) manages folders/sessions with a single
  RWMutex and a debounced 300 ms save through `SaveFunc`; `Encode`/`Load` marshal/unmarshal
  `model.Payload`.
- [`internal/wailsvc/dto.go`](internal/wailsvc/dto.go): read views expose `AuthType`/`HasPassword`
  (no secrets); write views (`SessionInput`) carry passwords only toward the vault.
- Session editor ([`session-editor.ts`](frontend/src/components/session-editor.ts)) builds auth
  inline (password or key path) with no reference to saved bundles.

## 4. Target design (to-be)

### 4.1 Data model (`internal/model`)

```go
// Credential is a named, reusable auth bundle (master plan §4 extension).
type Credential struct {
    ID       string `json:"id"`                // ULID (model.NewID)
    Name     string `json:"name"`              // unique-ish display name; required
    User     string `json:"user,omitempty"`    // login; required
    Auth     Auth   `json:"auth"`              // password XOR key path (same rule as sessions)
}
```

- Encrypted `Payload` gains `Credentials []Credential json:"credentials"`.
- `Session` gains an optional `CredentialID string json:"credentialId,omitempty"`. When set, the
  session's `User` + `Auth` are **resolved from the credential** at connect/test time (single
  source of truth); inline fields may still hold a snapshot/override for backward compatibility.
- Validation: `Name` non-empty; exactly one auth method (password XOR key); `User` non-empty for
  password bundles; `CredentialID` must reference an existing credential when set.

### 4.2 Store (`internal/store`)

Add `model.Credential` CRUD following the existing session pattern (`CreateCredential`,
`UpdateCredential`, `DeleteCredential`, `ListCredentials`) — each mutation triggers the same
debounced save; `Encode`/`Load` include the `credentials` slice. Delete semantics: deleting a
credential clears `CredentialID` references on sessions (soft-null) so existing sessions still
connect with their inline snapshot — decide during review whether to (a) keep inline snapshot,
or (b) hard-require the reference.

### 4.3 Services & DTOs (`internal/wailsvc`)

New **`CredentialService`** (registered in `main.go` alongside the others):
- `List() []CredentialDTO` — secret-free read view (`ID, Name, User, AuthType, HasPassword, KeyPath?`).
- `Create(input CredentialInput) string` / `Update(input CredentialInput)` / `Delete(id)`.
- `Get(id) CredentialDTO`.

`SessionDTO`/`SessionInput` gain `credentialId`. The read view exposes the reference only (never the
underlying secret). All secret-bearing inputs go straight into the encrypted vault — never echoed
back (§8).

### 4.4 Session editor integration

- Add a "Saved credential" dropdown (from `CredentialService.List()`), plus an inline
  "New…/Manage…" affordance opening the credential manager.
- Selecting a credential **prefills** `User` + `Auth` (password masked / key path) and sets
  `credentialId` on the saved input. User may still override inline; saving persists both the
  reference and the current inline values.
- Key bundles follow A2: passphrase prompted once at connect, never stored in the credential.

### 4.5 Credential manager UI

- A management surface (modal dialog opened from the gear menu and/or from the session editor):
  list existing credentials (name, type, masked), with Create / Edit / Delete (confirm via the
  existing `confirm` primitive). Delete shows affected session count (mirrors A8 destructive-op UX).
- New `frontend/src/components/credential-dialog.ts` for create/edit, reusing `ui/dialog` +
  `session-editor` field helpers.

## 5. Implementation steps (todo)

1. `internal/model`: add `Credential` type + `Payload.Credentials` + `Session.CredentialID`;
   extend `Validate` (name, auth XOR, user, reference-exists where known).
2. `internal/store`: credential CRUD + encode/load integration + reference cleanup on delete;
   unit tests (`store_test.go`).
3. `internal/wailsvc`: `CredentialService` + `CredentialDTO`/`CredentialInput`; thread `credentialId`
   through `SessionDTO`/`SessionInput`/`toModel`; secret-free read views; service tests.
4. `main.go`: register `CredentialService`.
5. Resolution: in `sshengine`/`sshx` connect & `TestConnection`, when `session.CredentialID` is set
   and inline auth is empty, look up the credential (via store) for `User`/`Auth`. Key passphrase
   still flows through the existing prompt path (A2).
6. Frontend store/types: add credentials state + credential types.
7. `frontend/src/components/session-editor.ts`: "Saved credential" dropdown + prefill + `credentialId`.
8. New `frontend/src/components/credential-dialog.ts` + gear/session-editor entry points (list/create/edit/delete).
9. `cmd/seed/main.go`: extend fixture generator with a few named credentials for QA.
10. Security tests: assert credentials never appear in `settings.json`/`known_hosts`; DTO list omits
    passwords (extend existing secret-free assertions); zeroization covered by existing vault hygiene.
11. QA: `make test`, `make lint`; manual pass — create bundle, attach to session, connect works;
    delete bundle → sessions still connect via inline snapshot.

## 6. Exit criteria

- Create/edit/delete named credentials (password and key variants) persisted **only** in the
  encrypted vault.
- A session can reference a saved credential; connect and Test Connection resolve `User`+`Auth`
  from it (or its inline snapshot after deletion).
- No secret ever appears in settings, known_hosts, or any read DTO (§8 assertions green).
- Credential manager UI reachable from gear menu and session editor; destructive delete confirmed
  with affected-session count.
- `make test`, `make lint` green (plus `make test-integration` for the connect-with-credential path).