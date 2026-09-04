# Plan: SFTP browser moves to a right-side panel (tree never covered)

**Status:** ready for implementation
**Scope:** frontend layout + store state + one persisted setting field. No SSH/SFTP
protocol changes, no Wails API changes.
**Reference context:** phases 5c/5d built the SFTP panel as a *left-panel mode*
(`leftMode: "tree" | "sftp"`, see `frontend/src/store.ts`, `shell.ts`); master plan
§6 "SFTP panel" bullet and the shortcut table describe that now-replaced behavior.

## Goal

When `settings.sftpBrowserEnabled` is on, the SFTP browser opens in a **new
right-hand column** of the window (right of the terminal pane) instead of
replacing the left-panel session tree. The session tree is always visible.
Visibility semantics stay identical to today (auto-open).

## Decisions (resolved)

- **D1 — Layout.** Body grid grows from 3 to 5 columns:
  `left | split | terminal | sftp-split | sftp`. The left panel never hides its
  tree. The SFTP column spans both grid rows (same as the left panel), so the
  bottom monitor bar stays under the terminal only.
- **D2 — State.** `StoreState.leftMode: "tree" | "sftp"` is **removed** and
  replaced by `sftpPanelOpen: boolean` (default `true`, ephemeral — tabs are
  ephemeral, A3). Single source of truth:
  `sftpPanelVisible = settings.sftpBrowserEnabled && sftpPanelOpen && hasReadyActiveTab`
  (rewritten in `store.ts`; name and signature kept, used by the shell).
- **D3 — Auto-open (confirmed with user).** Today's semantics are preserved:
  `activateTab` and `setTabState` (connecting→ready) set `sftpPanelOpen: true`
  when the setting is on and the (newly) active tab is ready. Closing the panel
  then activating another ready tab reopens it — same as today's
  leftMode-forcing behavior.
- **D4 — Affordances.**
  - SFTP panel header: the `[Sessions]` button becomes an `×` icon button
    ("Close SFTP browser") → `store.set({ sftpPanelOpen: false })`.
  - Left toolbar `[SFTP]` button becomes the *reopen* button →
    `store.set({ sftpPanelOpen: true })`; shown only when setting on + ready
    active tab + panel currently hidden.
  - `Ctrl+Shift+E` is **unchanged**: still toggles the persisted
    `sftpBrowserEnabled` setting (mirrors the Settings checkbox, master plan
    shortcut table, AGENTS.md).
- **D5 — Width.** Default `320 px`, drag range `200–640 px`, with an extra cap so
  the terminal column keeps ≥ `240 px` (`window.innerWidth - leftWidth - 8`,
  window MinWidth is 960). Persisted as `window.sftpWidth` in `settings.json`
  (0/missing → normalized to 320, exactly mirroring `leftWidth`). Persisted via
  the existing debounced `SaveSettings` path on drag end.
- **D6 — No ready tab / setting off.** The column fully collapses (CSS vars →
  0 px, column `display: none`). The existing left-panel hint
  ("Connect to a session to open the SFTP browser") keeps its current place and
  rule unchanged.
- **D7 — Terminal refit.** No code needed: `TermPool` already runs a
  per-container `ResizeObserver` → debounced fit → `TerminalService.Resize`
  (`frontend/src/terminal/xterm.ts`). Verified by QA (see below).

## Tasks

### 1. Backend settings — `internal/config`

1. `settings.go`
   - `WindowSettings`: add `SftpWidth int \`json:"sftpWidth"\`` (comment:
     SFTP right-panel width; 0 = not set → 320).
   - `DefaultSettings()`: `Window.SftpWidth: 320`.
   - `Load()` normalization: `if s.Window.SftpWidth <= 0 { s.Window.SftpWidth = 320 }`
     (next to the `LeftWidth` rule).
2. `config_test.go`
   - Extend `TestLoadRoundTrip` (site of `s.Window.LeftWidth = 380`) with
     `s.Window.SftpWidth = 420` + assertion.
   - Extend `TestLoadNormalizesUnknownThemeAndZeros` raw JSON with
     `"sftpWidth":0` and assert the default in the check block.
   - Add `TestLoadDefaultsSftpWidthZeroToDefault` mirroring
     `TestLoadDefaultsLeftWidthZeroToDefault` (omitted key → 320).

No other Go changes: `AppService` returns `config.Settings` as-is, so
`GetSettings`/`SaveSettings` pick up the field automatically (bindings
regenerate at `make build`).

### 2. Store — `frontend/src/store.ts`

- `WindowSettings`: add `sftpWidth: number`; add `DEFAULT_SFTP_WIDTH = 320`;
  `initialState` gets `window.sftpWidth: 320`, `sftpPanelOpen: true`,
  `sftpPanelWidth: DEFAULT_SFTP_WIDTH`.
- Remove `leftMode` (field + initial value + all usages).
- `sftpPanelVisible(state)`:
  `state.settings.sftpBrowserEnabled && state.sftpPanelOpen && hasReadyActiveTab(state)`.
- `activateTab` / `setTabState`: replace `patch.leftMode = "sftp"` with
  `patch.sftpPanelOpen = true` (same conditions). Update the D5d doc comments.

### 3. Shell — `frontend/src/components/shell.ts`

- **Left panel:** drop `sftpHost` and its `renderSftpPanel` mount; `treeHost`
  is no longer display-toggled (always shown); `sftpHint` stays as-is (same
  rule). `btnSftp` click → `store.set({ sftpPanelOpen: true })`; its visibility
  rule: `sftpBrowserEnabled && readyTab && !sftpPanelVisible`.
- **Right side:** after `main.right-pane`, append a second
  `<div class="splitter sftp-splitter">` (role/aria like the first) and an
  `<aside class="sftp-side">` hosting `renderSftpPanel(...)`.
- **Layout subscription** (rename `leftModeUnsub`/`applyLeftMode` →
  `sftpLayoutUnsub`/`applySftpLayout`): on store change compute
  `panel = sftpPanelVisible(st)` and:
  - `document.documentElement.style.setProperty("--sftp-w", panel ? w + "px" : "0px")`
  - `...("--sftp-gap", panel ? "4px" : "0px")` where `w = clamped sftpPanelWidth`
  - `sftpSide.style.display = panel ? "flex" : "none"` (+ `aria-hidden`)
  - toggle `btnSftp` (rule above).
- **Initial width:** `settings.window.sftpWidth || DEFAULT_SFTP_WIDTH`, clamped
  with the same rule as dragging (see below), applied at mount.
- **Drag** (mirror the left-splitter pattern):
  - `width = window.innerWidth - e.clientX` (column is at the right edge).
  - Clamp: `lo = 200`,
    `hi = max(lo, min(640, window.innerWidth - store.leftPanelWidth - 8 - 240))`
    (terminal keeps ≥ 240 px).
  - `pointermove` → update `--sftp-w` + `store.set({ sftpPanelWidth })`;
    `pointerup`/`pointercancel` → write
    `settings.window.sftpWidth` and persist via the debounced
    `SaveSettings` (generalize `persistLeftWidth` / add `persistSftpWidth`
    sharing the 300 ms debounce and error toast).

### 4. SFTP panel — `frontend/src/components/sftp-panel.ts`

- Replace the `[Sessions]` header button with an `×` icon button
  (`.btn small icon-btn`, like the back button): title "Close SFTP browser",
  `aria-label` "Close", click → `store.set({ sftpPanelOpen: false })`.
- Update the file-header comment (no longer "left-panel … replaces the session
  tree"). No other logic changes — the panel internals are layout-agnostic.

### 5. CSS

- `base.css`
  - `body` grid:
    ```
    grid-template-columns: var(--left-w, 320px) 4px minmax(0, 1fr)
                           var(--sftp-gap, 0px) var(--sftp-w, 0px);
    grid-template-areas:
        "left split right ssplit sftp"
        "left split status ssplit sftp";
    ```
  - `.sftp-splitter { grid-area: ssplit; }` (inherits `.splitter` styles).
  - `.sftp-side { grid-area: sftp; min-width: 0; display: flex;
    flex-direction: column; background: var(--bg-panel);
    border-left: 1px solid var(--border); overflow: hidden; }`
  - Update the file header comment (layout description).
- `components.css`: update the `.sftp-host` comment ("shares the left panel's
  flex column" → right side column). No other style changes needed — all
  `.sftp-*` rules are position-independent.

### 6. Misc frontend

- `main.ts` `toSettings`: `sftpWidth: Number(win.sftpWidth ?? 320) || 320`.
- `settings-dialog.ts`: checkbox hint →
  "Shown in a right-side panel when a session is active."

### 7. Docs (master plan only)

`plans/1787912690309-master-plan.md`:
- §2 feature bullet: "replaces the tree when a session is active" →
  "shown in a right-hand panel beside the terminal; the session tree stays visible".
- §4 settings schema: `window` gains `"sftpWidth": 320`.
- §6 Layout ASCII + bullets: add the right SFTP column line; add bullet
  "Right SFTP panel: 320 px default, drag-resizable 200–640 px (splitter),
  width persisted in `window.sftpWidth`; terminal column keeps ≥ 240 px."
- §6 "SFTP panel" bullet: rewrite placement (right column, `×` closes,
  left-toolbar `[SFTP]` reopens) — keep the content/ops description.
- Shortcut table: `Ctrl+Shift+E` row wording → "toggle SFTP browser setting
  (mirrors the checkbox)".

## Risks & edge cases

- **Narrow window (min 960 px):** drag and initial-width clamping keep the
  terminal ≥ 240 px; `minmax(0, 1fr)` on the center column prevents grid
  blowout.
- **Collapsing the column:** both `--sftp-gap` and `--sftp-w` go to 0 px *and*
  the column is `display: none` — no stray 4 px gap.
- **xterm at degenerate size:** covered by clamping; `doFit` already
  `try/catch`es `fit()`.
- **Lock/unlock re-render:** the per-mount store subscription is released in
  `renderShell` (existing pattern) — keep the renamed unsubscribe variable
  working.
- **Backward-compatible settings.json:** missing `sftpWidth` → 0 → 320
  (normalized in `Load`, covered by new test).
- **Settings dialog Save:** writes the full settings object; the spread
  `{ ...current.window }` already carries `sftpWidth` — no change needed.

## Validation

1. `make test` — new/extended `internal/config` tests green (`-race` via make).
2. `make lint` — gofmt/vet + `tsc --noEmit` clean.
3. `make build` + `make run` manual QA (host runtime):
   - Unlock with SFTP on → connect a session → panel **opens on the right**
     at 320 px; **session tree fully visible** on the left.
   - `×` closes it (column collapses, no gap); left toolbar `[SFTP]` reopens it;
     switching to another ready tab reopens it (auto-open preserved).
   - Active tab in error/closed state or zero tabs → column hidden, left-panel
     hint shown (unchanged).
   - Drag second splitter: clamps at 200 / 640 px; on a 960 px window the
     terminal never shrinks below 240 px; width survives app restart
     (`window.sftpWidth` in `~/.config/shelve/settings.json`).
   - Terminal refits correctly on open/close/drag (cols change, no artifacts,
     no pty resize errors).
   - `Ctrl+Shift+E` toggles the setting: off → panel hidden even with a ready
     tab; on → reopens.
   - Lock → unlock: no duplicate listeners (DevTools), no stale panel state
     (tabs are gone, so column hidden).
   - Upload/download progress footer and inline edit still work inside the
     right column (regression).

## Out of scope

- Changing SFTP panel internals (path bar, rows, context menu, editing flow).
- Multi-session SFTP views (panel stays bound to the active ready tab).
- Persisting panel open/closed state (width is persisted; open state is not).
- Rebinding Ctrl+Shift+E (stays a settings toggle).
