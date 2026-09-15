# Configuration export & import (sessions, credentials, jump hosts, settings)

## Goal

Add **Export configuration…** / **Import configuration…** to Shelve so a user can
move their entire setup (session tree, credentials, saved jump hosts, settings)
between machines or keep a backup, as a single self-contained **encrypted**
`.shelve` file.

## Confirmed decisions

1. **Encrypted export with its own passphrase** — a portable file re-encrypts the
   vault payload using the existing Argon2id (m=64 MiB, t=3, p=4, 16 B salt,
   32 B key) + AES-256-GCM scheme. The export passphrase is independent of the
   master password. Secrets never leave the vault as plaintext.
2. **Contents = vault data + settings, no `known_hosts`.** The decrypted export
   contains `model.Payload` (folders, sessions, credentials, saved jump hosts)
   and `config.Settings`. `known_hosts` is deliberately excluded so host-key
   TOFU trust is never silently transferred.
3. **Import default = Merge, with a Replace option.** Merge assigns fresh ULIDs
   to every imported entity, remaps `CredentialID`/`JumpHostRef`, and places the
   imported top level inside one new folder named `Imported <YYYY-MM-DD>`.
   Replace swaps the whole tree (and settings).
4. **Merge imports data only** (local `settings.json` untouched); **Replace
   imports data + settings**.
5. **All-or-nothing import**: decrypt and fully validate everything, build the
   ID remap in memory, and reject the whole import with a precise error if any
   entry is invalid. Never partially apply.
6. **Path-only transport (master plan A5)**: the Go backend reads/writes the
   file; the renderer only passes a path chosen via a new native dialog. No file
   bytes cross the loopback RPC.
7. **UI**: two gear-menu items opening small modals (native file dialog +
   passphrase + import mode). No new global shortcut.
8. **Replace disconnects all live sessions first** (same engine teardown as
   `VaultService.Lock`), so no tab points at a removed session ID. Merge leaves
   live sessions untouched.
9. **File format**: single JSON envelope, extension `.shelve`, default name
   `shelve-config-YYYY-MM-DD.shelve`.

## Export file format

`<name>.shelve` (JSON, 0600, atomic write):

```jsonc
{
  "format": "shelve-export",
  "v": 1,
  "app": "0.1.0",                       // internal/app.Version at export time
  "createdAt": "2026-09-15T12:00:00Z",
  "kdf": { "alg": "argon2id", "salt": "<b64 16B>", "time": 3, "memory": 65536, "threads": 4 },
  "enc": { "alg": "aes-256-gcm", "nonce": "<b64 12B>" },
  "ct": "<b64 ciphertext>"
}
```

`ct` decrypts to `{"payload": <model.Payload>, "settings": <config.Settings>}`.

- Use a **distinct AAD** (`"dsmexp1"`) so an export ciphertext is
  domain-separated from `vault.json` (`"dsmsv1"`).
- `format`/`v` gate import: unknown `format` or `v` → typed
  `ErrUnsupportedExport`; corrupt envelope/JSON → `ErrExportCorrupt`; GCM auth
  failure / wrong passphrase → `ErrExportWrongPassword`.

## Implementation tasks (ordered)

### 1. `internal/vault/export.go` (new) — reusable encryption

Reuse the package's KDF/cipher helpers (do not duplicate crypto):

- `EncryptWithPassword(plaintext []byte, password string, appVersion string) ([]byte, error)`
  — fresh 16 B salt + 12 B nonce, Argon2id derive, `sealGCM` with AAD `"dsmexp1"`,
  return the JSON envelope above.
- `DecryptWithPassword(data []byte, password string) ([]byte, error)`
  — parse/validate envelope (`format=="shelve-export"`, `v==1`, algs, salt/nonce
  lengths, KDF bounds reusing `maxKDF*`), derive, `openGCM`. Zeroize the derived
  key on every path.
- Typed errors: `ErrExportCorrupt`, `ErrExportWrongPassword`,
  `ErrUnsupportedExport`.
- Envelope struct(s) local to this file; keep `Envelope`/`vault.json` untouched.

### 2. `internal/store/merge.go` (new) — atomic import merge

- `type MergeResult struct { Folders, Sessions, Credentials, SavedJumpHosts, RemappedReferences int }`
- `func (s *Store) Merge(payload []byte, rootFolderName string) (MergeResult, error)`:
  1. `json.Unmarshal` into `model.Payload` (bad JSON → error).
  2. Validate every folder/session/credential/saved jump host with the existing
     `Validate()` methods. Any failure aborts (wrap with entity index/name).
  3. Reject dangling references: a non-empty `CredentialID`/`JumpHostRef` that
     does not resolve inside the import → error (app-generated exports never
     contain them; store deletes soft-null).
  4. Rebuild the order graph from `Root` + `Folder.Children` exactly like
     `Load` (dedupe, drop unknown IDs, recover unreferenced nodes into root).
  5. Build the ID remap (`old → model.NewID()`) for credentials, saved jump
     hosts, folders, sessions. Rewrite parent/children/root, `FolderID`/
     `ParentID`, `CredentialID`, `JumpHostRef`.
  6. If the import has ≥1 node, create a new folder `rootFolderName` under root
     and reparent the imported top-level nodes into it, preserving order.
  7. Acquire `s.mu`, apply the maps/order/`searchIdx` in one shot, `scheduleSave()`.
     All validation/remap work happens **before** the lock; a failure leaves the
     store byte-for-byte unchanged.
  8. Empty payload (no nodes) → no-op, zero counts.
- Session/folder/credential names are not deduplicated (duplicates are allowed
  everywhere else). Count `RemappedReferences` = sessions whose refs were
  rewritten.

### 3. `internal/api/transferservice.go` (new) + DTO

- `type ImportResultDTO struct { Mode string; Folders, Sessions, Credentials, SavedJumpHosts, RemappedReferences int }`
  (json tags; secret-free) in `dto.go`.
- `NewTransferService(store *store.Store, v *vault.Vault, engine *sshengine.Manager, emit Emitter) *TransferService`.
- `Export(path, password string) error`:
  - require `v.IsUnlocked()` → `vault.ErrLocked`; require non-empty `path`,
    non-empty `password`.
  - `payload := store.Encode()`; `settings, _ := config.Load()`.
  - marshal `{payload, settings}`; `vault.EncryptWithPassword(...)`; write with
    `config.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), data)`
    (0600, atomic).
- `Import(path, password, mode string) (ImportResultDTO, error)`:
  - require unlocked; read file (cap size, e.g. 64 MiB → error beyond it);
    `vault.DecryptWithPassword`; unmarshal `{payload, settings}`.
  - `mode == "merge"`: `store.Merge(payload, "Imported "+time.Now().Format("2006-01-02"))`.
  - `mode == "replace"`: `engine.Shutdown(ctx)` (bounded 3 s, same as Lock) →
    `store.Load(payload)` → `store.Flush()` → if settings present,
    `settings.Save()`. Counts = entity totals from the payload.
  - unknown `mode` → error.
- Never log the passphrase or decrypted bytes.

### 4. Wire-up

- `internal/app/app.go`: construct `transferService` and expose
  `TransferService()`; import `path/filepath`.
- `internal/bridge/register.go`: `s.Register("TransferService", a.TransferService())`.
- `internal/bridge/surface_test.go`: add
  `"TransferService": {"Export", "Import"}` to the golden map.

### 5. Electron native surface

- `electron/main.ts`: add IPC handlers
  - `dialog:pickSaveFile` → `dialog.showSaveDialog(win, { title: "Export configuration", defaultPath: <name>, filters: [{ name: "Shelve configuration", extensions: ["shelve"] }] })` → path or `""`.
  - `dialog:pickOpenFile` → `dialog.showOpenDialog(win, { title: "Import configuration", properties: ["openFile"], filters: [...] })` → path or `""`.
  - Both `trusted(event)` gated like the existing handlers.
- `electron/preload.ts`: expose
  `pickSaveFile(defaultName: string): Promise<string>` and
  `pickOpenFile(): Promise<string>` under `window.shelve`.
- `frontend/src/rpc/types.ts`: extend `ShelveNative`; add `ImportResultDTO`
  interface mirroring the Go DTO.

### 6. Frontend

- `frontend/src/rpc/index.ts`: add `TransferServiceApi`
  (`Export(path, password): Promise<void>`,
  `Import(path, password, mode): Promise<ImportResultDTO>`) and export the proxy.
- `frontend/src/store.ts`: move the settings-mapping helper here as exported
  `normalizeSettings(raw)` (currently the private `toSettings` in `main.ts`);
  update `main.ts` to import it. Add `async afterImport(mode)`:
  - `merge`: `refreshTree()`, `refreshCredentials()`, `refreshSavedJumpHosts()`,
    keep tabs.
  - `replace`: clear `tabs`/`activeTabID`/`selectedID`/`forwards`/
    `sftpTransfers`/`monitor`, re-read `AppService.GetSettings()`, apply
    `normalizeSettings`, `store.set({ settings })`, `initTheme(...)`,
    `applyZoomLevel(...)`, then refresh tree/creds/jumps.
- `frontend/src/components/transfer-dialog.ts` (new), modeled on
  `credential-dialog.ts` (`openDialog` + `field` pattern):
  - `openExportDialog()`: passphrase + confirm (min length 8, mismatch inline
    error), `window.shelve.pickSaveFile(...)`, `TransferService.Export`, success
    toast. Cancel/empty path → no-op.
  - `openImportDialog()`: passphrase, `window.shelve.pickOpenFile()`, mode radio
    (Merge default / Replace). Replace shows the A8-style `confirmDialog` with
    the current tree/credential/jump-host counts ("N sessions will be replaced").
    On success `TransferService.Import`, then `store.afterImport(mode)` and a
    toast summarizing imported counts.
- `frontend/src/components/gear.ts`: add
  `{ label: "Export configuration…", action: () => openExportDialog() }` and
  `{ label: "Import configuration…", action: () => openImportDialog() }`
  (after "Jump hosts…", before "Lock vault…").

### 7. Tests

- `internal/vault/export_test.go`: round-trip; wrong passphrase →
  `ErrExportWrongPassword`; tampered `ct`/`nonce` → error; bad `format`/`v` →
  `ErrUnsupportedExport`; out-of-range KDF → `ErrExportCorrupt`; export blob
  cannot be opened as a vault.
- `internal/store/merge_test.go`: remap of every ID; refs rewritten; nested
  structure + sibling order preserved; imported roots under the new folder;
  existing nodes untouched; invalid entity → error with no mutation; dangling
  ref → error; empty payload no-op; duplicate names allowed.
- `internal/api/transfer_test.go`: `Export` writes 0600 file with the expected
  envelope and **contains no plaintext password bytes**; round-trip
  export→`Import` merge reproduces counts; wrong passphrase error; replace
  applies payload + settings; unknown mode error; locked vault → `ErrLocked`.
- `internal/bridge/surface_test.go`: updated golden.
- No integration test (no SSH path touched).

### 8. Documentation (same change)

- `plans/1789467100000-master-plan.md`:
  - §1 Goals: export/import of the full configuration.
  - §2 Resolved Decisions: add a `D13` row (encrypted `.shelve`, merge/replace,
    settings included, `known_hosts` excluded).
  - §4 Data Model: document the `.shelve` export envelope + AAD `"dsmexp1"`.
  - §5 Service surface: add `TransferService` (`Export`, `Import`).
  - §6 UI: gear menu items + modal behavior; note import mode.
  - §8 Security: export protects secrets with its own passphrase; no plaintext
    at rest or over the transport; paths only.
  - §12 Acceptance: add an export→import (merge and replace) round-trip.
- `AGENTS.md`: add `TransferService` to the service surface list and note
  config export/import is path-only.
- `README.md`: user-facing "Backup & migrate" section.

## Risks / edge cases to verify

- **Crypto reuse:** export must use the exact vault KDF params and a distinct
  AAD; reuse `sealGCM`/`openGCM`/`derive` rather than copying them.
- **Merge atomicity:** all validation/remap before `s.mu`; a rejected import must
  leave `vault.json` untouched (assert by encoding before/after).
- **Replace persistence:** `store.Load` does not save; `Flush` must be called
  (vault stays unlocked) and settings saved only in Replace mode.
- **UI reconciliation after Replace:** tabs must be cleared locally; imported
  settings must re-apply theme/zoom without a window reload.
- **Dangling refs:** abort (all-or-nothing), since the app never exports them.
- **Large/corrupt files:** cap read size and return typed errors, never panic.
- **Locked vault:** both actions return `vault.ErrLocked`; dialogs should be
  disabled behind the unlock gate.
- **File permissions:** exported file 0600 even outside the config dir.
- **Import file with passwords/key paths:** ensure no secret appears in logs or
  toasts (toast counts only).

## Validation

- `make test` **and** `make test-race` (new unit tests, existing suites green).
- `make lint` (Go `gofmt`/`vet` + renderer/electron `tsc --noEmit`).
- Manual QA (`make build && make run`):
  1. Create folders/sessions (password + key auth), a credential, a saved jump
     host, and a bastion session; Export with a passphrase → `.shelve` file
     created 0600.
  2. Inspect the file in an editor → readable header, no plaintext secrets.
  3. Lock/unlock, Import the file with Merge → new `Imported <date>` folder with
     all entities, references intact (session editor shows credential/jump host),
     existing local data unchanged.
  4. Import with Replace while a terminal tab is connected → tab closes/session
     disconnects, tree + settings match the export, theme/zoom update live.
  5. Wrong passphrase → inline error, nothing changed.
  6. `make seed`, export the 300-session QA vault, merge-import into an empty
     vault → counts match.

## Open questions / out of scope

- Transferring `known_hosts` (explicitly excluded; could be a future opt-in
  checkbox).
- Selective export (a single folder/subtree) or importing into a chosen folder.
- Scheduled/automatic backups, cloud sync, import of the pre-Electron vault
  format (the export format is new in v1).
- Exporting while locked (requires the decrypted payload).

## Documentation/plan location note

The user asked for plans under `./plans`; master-plan/AGENTS/README updates are
part of the same change and must stay consistent with this plan.
