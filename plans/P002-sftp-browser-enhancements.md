# Plan P002 — SFTP Browser Enhancements

> Status: Draft for review
> Scope: SFTP start-path configuration + open-on-double-click
> Owner: Architect (this document) → implemented in Code mode
>
> Superseded (2026-09-04): the Open-on-double-click part
> (`OpenRemoteFile`, `sftpOpenCommand` setting, 'Open' menu item) was
> removed; file double-click now runs 'Edit as text'. See
> `plans/1788531512181-sftp-remove-open-command.md`. The configurable
> start-path part remains in force.

## 1. Goal

Improve the SFTP browser ([`frontend/src/components/sftp-panel.ts`](frontend/src/components/sftp-panel.ts))
with two behaviors:

1. **Configurable start path** — the directory the SFTP browser opens in for a session
   must be user-configurable instead of always being the hard-coded remote `~`.
2. **Open file on double-click** — today a non-text file double-click only *downloads*
   (`DownloadThenSave` → save dialog/toast). It must instead *open* the file with the
   local system default application (like a normal file manager), while still offering
   explicit "Edit as text" and "Download…" via the context menu.

## 2. Master-plan references (read before implementation)

- [`plans/1787912690309-master-plan.md`](plans/1787912690309-master-plan.md)
  - **§2 Resolved Decisions → D4** — SFTP scope: browse/upload/download/mkdir/rename/delete +
    "edit text file with system editor" (download to temp → external editor → save → re-upload). Open-on-double-click is a natural extension of the temp-file flow.
  - **§2 → A5** — large file bytes never cross IPC: transfers pass *paths*; Go streams bytes.
    Any new "open" operation must download server-side to a local temp path and hand only a path to the OS opener.
  - **§4 Data Model & Storage** — `settings.json` schema and per-session model fields; adding an
    `sftpInitialPath` (per-session) and/or a global SFTP default path; `tmp/` semantics (created 0600, swept on lock/exit).
  - **§5 Architecture** — `SftpService` method contract; DTOs in `dto.go`; `AppService.GetSettings/SaveSettings`.
  - **§6 UI/UX Specification → SFTP panel** — breadcrumb path bar, double-click behavior
    (dir → navigate; file → Edit if text-like else Download), context-menu rows (Open / Edit / Download / …), status line with transfer progress.
  - **§8 Security** — temp files 0600 under `tmp/`, swept on lock/exit/crash; no plaintext creds; no network except user hosts.
- Phase plans to re-check:
  - [`plans/1788003650840-phase-5b-sftp-transfers-editing.md`](plans/1788003650840-phase-5b-sftp-transfers-editing.md) — transfer + `EditRemoteText` state machine, temp cleanup, `DownloadThenSave` fallback.
  - [`plans/1788003650840-phase-5c-sftp-panel.md`](plans/1788003650840-phase-5c-sftp-panel.md) — panel UI, double-click dispatch, context menu.
  - [`plans/1788003650840-phase-4d-settings-lock-shortcuts.md`](plans/1788003650840-phase-4d-settings-lock-shortcuts.md) — settings dialog live-apply/revert pattern.

## 3. Current state (as-is)

### 3.1 Start path
- [`sftp-panel.ts`](frontend/src/components/sftp-panel.ts) hard-codes `curPath = "~"` on mount
  (line 97) and on tab switch (line 123). The breadcrumb always starts at `~`. There is no
  per-session or global start-path setting.

### 3.2 Double-click behavior
- `openEntry(entry)` (lines 345–357): dir → navigate; `textLike` → `startEdit` (`EditRemoteText`);
  **else → `downloadSave`** which calls `SftpService.DownloadThenSave` → save dialog/toast, not "open".
- Context menu (lines 671–700) already lists "Open" (= `openEntry`), "Edit as text", "Download…".
  So "Open" currently means download-for-non-text — the fix must make **Open actually open**.

### 3.3 Backend
- [`internal/wailsvc/sftp_service.go`](internal/wailsvc/sftp_service.go) has `Download`,
  `DownloadThenSave`, `EditRemoteText`, `CancelEdit`. No "download-to-temp-then-launch-system-app" op.
- [`internal/sftp/edit.go`](internal/sftp/edit.go) already implements the temp-download + launch-editor
  pattern (sets up `tmp/edit-<ULID>.<ext>` 0600, process-group lifecycle). This is the closest reusable template for an "Open" op.
- Editor command comes from `Settings.TextEditorCommand` (default `xdg-open`) — a natural opener for the "Open" action.

## 4. Target design (to-be)

### 4.1 Configurable start path

Two complementary knobs (proposal — confirm during review):

- **Global default** in `settings.json`: `"sftpInitialPath": "~"` (applies when a session has no
  per-session override). Added to [`internal/config/settings.go`](internal/config/settings.go)
  with normalize fallback to `~`.
- **Per-session override** `SftpInitialPath string` on `model.Session` / `SessionDTO` /
  `SessionInput` in [`internal/model/model.go`](internal/model/model.go) and
  [`internal/wailsvc/dto.go`](internal/wailsvc/dto.go). Empty = use global default.

Frontend resolution order in `sftp-panel.ts`:
`per-session value (if set) → settings.sftpInitialPath → "~"`. The session editor gains an
optional "SFTP start path" field (blank = default).

Validation: path must be absolute or `~`-prefixed (reuse the existing `resolve()` semantics);
server-side `List` errors surface via the existing `errorMsg` path — no crash, fall back to `~` on error.

### 4.2 Open file on double-click

Add a backend operation **`OpenRemoteFile(tabID, remotePath)`**:

- **Security (A5)**: download the remote file to a local temp path under `tmp/` (0600), then
  launch the local system default handler on that **path** (no file bytes over IPC).
- Reuse the `internal/sftp` temp-download plumbing. For the opener command, use
  `Settings.TextEditorCommand`-style mechanism — introduce an **"Open command"** defaulting to
  `xdg-open` (reuse the same `strings.Fields` verbatim-execution pattern already documented in
  `edit.go` lines 9–12). Distinguish from "Edit as text": Open launches the default handler and
  does **not** run the save-detection watcher / does not re-upload.
- Cleanup: opened temp files are swept by the existing `tmp/` lifecycle (lock/exit/startup, §8).
- Errors: no default handler / launch failure → typed error surfaced as a toast (same pattern as
  other SFTP ops).

Wire-up:

- `internal/sftp`: `Manager.OpenRemoteFile(tabID, remotePath, openCmd string) error` (or `(string, error)`
  returning the temp path for the toast).
- `internal/wailsvc/sftp_service.go`: expose `OpenRemoteFile(tabID, remotePath)` reading the open
  command from settings.
- [`frontend/src/components/sftp-panel.ts`](frontend/src/components/sftp-panel.ts):
  - `openEntry`: dir → navigate (unchanged); otherwise → `OpenRemoteFile` (file opens locally).
  - Text files: default double-click = **Open** too (consistent with a file manager); "Edit as text"
    stays a context-menu action.
  - Context menu: keep **Open**, **Edit as text** (text-like only), **Download…** distinct.

### 4.3 Session editor / settings UI

- Session editor: optional "SFTP start path" input.
- Settings dialog (General): optional "SFTP default path" input (live-apply not needed — used on
  next browse, so apply-on-Save like other settings).

## 5. Implementation steps (todo)

1. `internal/config/settings.go`: add `SftpInitialPath` (global default, fallback `~`) + normalize;
   extend `config_test.go` (round-trip, secret-free).
2. `internal/model/model.go` + `validate.go`: add `Session.SftpInitialPath`; validation rule
   (absolute or `~`-prefixed, or empty).
3. `internal/wailsvc/dto.go`: carry `sftpInitialPath` on `SessionDTO` / `SessionInput` (+ `toModel`).
4. `internal/sftp`: add `OpenRemoteFile` (temp download 0600 + launch open command, no save-back,
   no watcher); unit tests for temp lifecycle + command building.
5. `internal/wailsvc/sftp_service.go`: bind `OpenRemoteFile(tabID, remotePath)` reading the open command from settings; service test.
6. `frontend/src/components/sftp-panel.ts`: resolve start path (per-session → global → `~`);
   change `openEntry` double-click to `OpenRemoteFile`; adjust context menu labels.
7. `frontend/src/components/session-editor.ts`: add optional "SFTP start path" field.
8. `frontend/src/components/settings-dialog.ts`: add global "SFTP default path".
9. Integration/QA: `make test-integration` (SFTP ops against `docker/sshd`), `make test`, `make lint`;
   manual pass: configure a start path, double-click a non-text file → it opens in the default app.

## 6. Exit criteria

- SFTP browser opens in the configured path (per-session wins over global; blank/global → `~`).
- Double-click on any non-directory file opens it with the local default app; no file bytes over IPC.
- "Edit as text" and "Download…" remain available and correct from the context menu.
- Temp opened files are 0600 and swept on lock/exit/startup (§8).
- `make test`, `make lint`, `make test-integration` green.