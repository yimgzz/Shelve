# Phase 5d — SFTP browser UX polish: default-on, editable path bar, Sessions/SFTP mode toggle

**Type:** Config + frontend (no new Go product code beyond settings defaults; no IPC contract changes).

**Prereq:** Phase 5c (shipped, v1.1).

**Master plan:** `plans/1787912690309-master-plan.md` — read **§4** (settings.json schema, defaults, atomic 0600 writes), **§5** (SftpService contract: `List(tabID, path)` resolves paths server-side; DTOs in `dto.go`), **§6** (SFTP panel spec, left-panel layout, Settings modal, Ctrl+Shift+E shortcut), **§8** (items 1, 4, 7: settings.json must hold no secrets, 0700/0600 perms, atomic writes), **§12** (v1.1 global acceptance, E2E chain) before starting.

## Goal

Four user-visible improvements to the SFTP browser:

1. **SFTP browser enabled by default** — fresh installs (and existing configs that never persisted the flag) get `sftpBrowserEnabled: true`.
2. **Current directory shown in a text field** — the panel header displays the active remote path in an editable input.
3. **Navigate by typing a path** — Enter in the field jumps to that directory (absolute, `~`-relative, or relative to the current dir).
4. **Sessions ↔ SFTP mode toggle** — while a session is active and the SFTP browser is open, a button opens the host/session list so a new session can be started; a button switches back to the SFTP browser.

## Owner-approved decisions

| # | Decision |
|---|----------|
| D5d-1 | Mode toggle UX (owner-confirmed "Recommended" option): a **[Sessions]** button inside the SFTP panel header switches the left panel to the tree; when a session is active and SFTP is enabled, the left **toolbar shows a [SFTP] button** to switch back. Auto-switch to SFTP stays on connect and tab activation. |
| D5d-2 | Path bar **replaces** the clickable breadcrumb: header row = back button (`←`) + editable path input (single row, compact for the 240–480 px panel). The `parentOf`/`joinRemote`/`pathRoot`/`stripRoot` helpers stay; `segmentsOf`/`segmentPath` breadcrumb-only helpers are removed. |
| D5d-3 | Explicit left-panel mode in the store: `leftMode: "tree" | "sftp"`. Visibility rule becomes `settings.sftpBrowserEnabled && leftMode === "sftp" && activeTab.state === "ready"`. Default `"tree"`. |
| D5d-4 | Presence-aware settings load: `sftpBrowserEnabled` absent from `settings.json` → treated as **true** (new default for existing users too); an explicit `false` (user disabled it) is respected. |

## Tasks

### 1. SFTP browser enabled by default (backend)

Files: [`internal/config/settings.go`](internal/config/settings.go), [`internal/config/config_test.go`](internal/config/config_test.go). Master plan refs: **§4** (settings schema/defaults), **§8.7** (atomic writes), **§8.4** (0600 perms — unchanged).

- In `DefaultSettings()` set `SftpBrowserEnabled: true` (currently `false`).
- Make `Load()` presence-aware for this one flag: unmarshal the raw JSON once more into `map[string]json.RawMessage`; if the key `sftpBrowserEnabled` is absent → force `s.SftpBrowserEnabled = true` after `json.Unmarshal`; if present → keep the decoded value (explicit `false` wins). Missing-file path already returns defaults (now `true`).
- `normalize()` is untouched: booleans have no zero-value ambiguity here because presence is detected in `Load()`.
- Tests:
  - fresh config dir (no file) → `SftpBrowserEnabled == true`;
  - file without the key → `true`;
  - file with `"sftpBrowserEnabled": false` → `false`;
  - file with `"sftpBrowserEnabled": true` → `true`.
- No change to `Save()` semantics; an explicit toggle still writes the field.

### 2. SFTP browser enabled by default (frontend)

File: [`frontend/src/store.ts`](frontend/src/store.ts). Master plan refs: **§6** (Settings checkbox mirror; GetSettings at boot).

- `initialState.settings.sftpBrowserEnabled` → `true` (matches the backend default; `AppService.GetSettings()` at boot in [`main.ts`](frontend/src/main.ts) overrides with the persisted value via `toSettings`).
- The settings dialog checkbox ([`settings-dialog.ts`](frontend/src/components/settings-dialog.ts)) and Ctrl+Shift+E ([`shortcuts.ts`](frontend/src/ui/shortcuts.ts)) need no logic changes — they already write `SaveSettings`.

### 3. Editable path bar with navigation

File: [`frontend/src/components/sftp-panel.ts`](frontend/src/components/sftp-panel.ts), plus CSS in [`frontend/src/style/components.css`](frontend/src/style/components.css). Master plan refs: **§6** (SFTP panel: breadcrumb path bar → now editable), **§5** (`List(tabID, path)` already accepts arbitrary paths; `resolve()` in `internal/sftp/manager.go` expands `~`, `~/x`, absolute and relative paths server-side — **no Go change needed**).

- Header layout in `buildStatic()`: keep the `←` back button, replace `breadcrumbEl` (a `nav.sftp-breadcrumb`) with an `<input type="text" class="sftp-path mono">`.
- `curPath` remains the single source of truth; the input mirrors it (`input.value = curPath`) on every render path (rename `renderBreadcrumb()` → `renderPathBar()`).
- Enter handling:
  1. trim input value;
  2. empty → `"~"`;
  3. starts with `~` or `/` → use as-is;
  4. otherwise → treat as a child of `curPath` via `joinRemote(curPath, value)` (keeps navigation anchored to the visible cwd);
  5. assign to `curPath`, call `loadList()`.
- Escape → revert `input.value = curPath`. Blur without Enter → revert (guard: reuse the existing `isTypingInPanel()` check in `refresh()` so store churn never clobbers an in-progress edit, and blur-revert must not fire right after a successful Enter submission).
- Unchanged behavior: failed `List` (bad path) → existing error state in the list area; the input keeps the attempted path until the next render; `curPath` reverts to the last good value on next successful render (make this explicit in `loadList()`'s catch: reset `curPath` to the previous good path so breadcrumb/back logic stays consistent).
- Back button (`parentOf`) and double-click directory navigation continue to work and re-render the input.
- CSS: `.sftp-path` mono font, flex: 1 inside `.sftp-header`, right padding, `min-width: 0`; remove `.sftp-breadcrumb` styles; keep `.sftp-header` layout (back button + path bar).

### 4. Sessions ↔ SFTP mode toggle

Files: [`frontend/src/store.ts`](frontend/src/store.ts), [`frontend/src/components/shell.ts`](frontend/src/components/shell.ts), [`frontend/src/components/sftp-panel.ts`](frontend/src/components/sftp-panel.ts). Master plan refs: **§6** (left-panel layout, SFTP panel replaces tree), **§6 Shortcuts** (Ctrl+Shift+E unchanged), **§6 Components** (Settings checkbox semantics unchanged).

Store (`store.ts`):
- Add `leftMode: "tree" | "sftp"` to `StoreState`, `initialState.leftMode = "tree"`.
- `sftpPanelVisible(state)` → `state.settings.sftpBrowserEnabled && state.leftMode === "sftp"` and the active tab exists with `state === "ready"`.
- Add helper `hasReadyActiveTab(state)` (active tab exists && ready) reused by shell and store.
- Auto-switch (preserves current auto-show behavior per D5d-1):
  - in `activateTab(tabID)`: after switching, if the activated tab is ready and `sftpBrowserEnabled` → `leftMode = "sftp"`;
  - in `setTabState(tabID, state, msg)`: when `state === "ready"`, `tabID === activeTabID` and `sftpBrowserEnabled` → `leftMode = "sftp"`.
  - No change in `main.ts` — both hooks live in the store.

Shell (`shell.ts`):
- `applyLeftMode()`:
  - panel visible → tree hidden, `sftpHost` shown (as today);
  - panel hidden + setting on + `hasReadyActiveTab` → tree shown, **no hint**, toolbar **[SFTP]** button shown;
  - panel hidden + setting on + no ready active tab → tree shown + existing hint "Connect to a session to open the SFTP browser" (`sftpHint` display condition changes from `setting && !panel` to `setting && !hasReadyActiveTab`);
  - setting off → tree always, no button, no hint.
- Add a **[SFTP]** button in the left toolbar (before the gear spacer, after `+ Folder`) — visible only when `settings.sftpBrowserEnabled && hasReadyActiveTab && !sftpPanelVisible(state)`; click → `store.set({ leftMode: "sftp" })`.

SFTP panel (`sftp-panel.ts`):
- Header gets a **[Sessions]** button as the first element (before `←`): `title` "Show the session list", click → `store.set({ leftMode: "tree" })`.
- The panel stays mounted when hidden (display toggled by the shell, as today), so `curPath` is preserved across the Sessions ↔ SFTP round-trip for the active tab — intended behavior.

No changes to `shortcuts.ts` (Ctrl+Shift+E still toggles the setting, mirroring the checkbox per §6) and no changes to the settings dialog.

### 5. Docs, QA and gates

- README: update any statement implying SFTP browser is opt-in (check `README.md` for `sftpBrowserEnabled` / "SFTP browser" mentions); document the new path-bar navigation and the Sessions/SFTP toggle.
- Manual QA checklist (annotate in commit message):
  - Fresh config dir → create vault → connect to a session → **SFTP panel appears without enabling anything** (default-on).
  - Path field shows the start path (`~` by default; per-session/global override respected via `initialPath()`).
  - Type `/etc` + Enter → browsed; type a non-existent dir → error state, path/back consistency intact; type `..`, `~/sub`, absolute paths → correct navigation; Esc reverts; blur reverts.
  - Double-click a directory → field updates; back button works.
  - With a ready tab: [Sessions] → tree visible; open a **second session** from the tree; auto-switch to SFTP for the new tab; [SFTP] button returns to the browser; tab switching between ready tabs auto-shows SFTP.
  - Ctrl+Shift+E and the Settings checkbox still toggle the feature two-way; disabling while the panel is open returns to tree; re-enabling + ready tab auto-shows the panel.
  - Lock mid-browse → unlock screen; hint line correct in all modes.
- Gates: `make test` (config tests), `make lint` (tsc `--noEmit`), `make build`; `make test-integration` only if the SFTP manager touched (it is not, but run it if any sftp-panel-facing behavior changes land in Go).

## Out of scope

- No backend IPC/service changes (`SftpService` is untouched; `List` already accepts arbitrary paths).
- No drag & drop (D4, §2), no tab restoration (A3), no auto-reconnect (A4).
- No changes to `Cd`/cwd-persistence semantics: each `List` call resolves its own path (§5 contract).
- No secret/key material handling changes (§8.1, §8.3 untouched).

## Exit criteria

1. All four features work per the QA checklist above against a real sshd (host-run `make build` + `make run`).
2. `make test`, `make lint`, `make build` green.
3. Existing `settings.json` without the flag loads as SFTP-enabled; an explicit `false` stays disabled (unit-tested).
4. No IPC contract changes; no secrets in settings; perms assertions still pass.