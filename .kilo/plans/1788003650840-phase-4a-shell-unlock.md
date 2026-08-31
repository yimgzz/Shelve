# Phase 4a — UI foundation: theme, layout, store, bootstrap, unlock gate

**Type:** Frontend (main) + backend (3-line change). **Prereq:** Phase 3 done (3e). **Sub-plan 1/4 of old Phase 4. Next: 4b.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (D3, A3–A9), §5 (full event contract + services), §6 (Layout, Theming, Unlock screen), §11 (Wails beta isolation)** before starting.

**Scope guard (important):** shell + unlock only. NO tree, NO tabs, NO terminal, NO session/settings dialogs (4b–4d fill those in). The ONLY backend change is the `window.leftWidth` setting.

## Goal
The application skeleton: theme engine + CSS tokens + layout (`left panel | splitter | right pane`, 22 px status-band row), pub/sub store, `main.ts` bootstrap that subscribes to ALL master §5 events in exactly one place, the unlock gate (create + unlock), and the shared UI primitives (dialog, toasts, confirm, context menu, host-key / key-passphrase prompt modals) that 4b–4d reuse verbatim.

## Tasks
1. Backend (keep `make test` green):
   - `internal/config/settings.go`: add `LeftWidth int \`json:"leftWidth"\`` to `WindowSettings` (0 = not set → treated as 320); extend config tests for the new default.
   - `internal/wailsvc` settings DTO mirrors it.
2. `style/themes.css`: token sets under `:root[data-theme="light"]` and `:root[data-theme="dark"]` — exact token list from master §6 Theming: `--bg, --bg-panel, --bg-hover, --bg-active, --border, --text, --text-dim, --accent, --error, --ok, --warn, --terminal-bg, --terminal-fg`; terminal palettes: light = white-ish bg, dark = `#1e1e2e`-ish (two small palettes bound to theme, master §6).
   `style/base.css`: reset + layout grid:
   ```css
   body { display: grid;
     grid-template-columns: var(--left-w, 320px) 4px 1fr;
     grid-template-rows: 1fr 22px;
     grid-template-areas: "left split right"
                          ".    .     status"; }
   ```
   `style/components.css`: buttons, inputs, modal (backdrop + card), toast stack, context menu, status dots (amber/green/red/gray, amber pulsing), empty-state blocks.
3. `ui/theme.ts`: `initTheme(settings)` → set `<html data-theme>`; system mode: `matchMedia('(prefers-color-scheme: dark)')` change listener + Wails `ThemeChanged` event (subscribe after runtime ready — verify the exact common-event name against the pinned Wails v3 version, master §5 names it `events.Common.ThemeChanged`); manual mode: settings override wins. `setTheme(mode)` + `currentTheme()` export (4c terminal palette will consume it). FOUC guard: tiny inline script in `frontend/index.html` reads `localStorage["dsm-theme"]` cache → sets `data-theme` before first paint; `theme.ts` keeps the cache in sync.
4. `store.ts`: typed pub/sub store. State: `{ settings, vaultState: "create"|"locked"|"unlocked", tree: NodeDTO[], selectedID: string|null, searchQ: string, tabs: Tab[], activeTabID: string|null, leftPanelWidth: number }`; `Tab = { id: string, session: <session DTO snapshot incl. jumps>, state: "connecting"|"ready"|"error"|"closed", errorMessage?: string }`. API: `getState() / set(partial) / subscribe(fn)`. RULE (enforce in review): components subscribe to the store, never raw `Events.On` — `main.ts` is the single event-owner.
5. `main.ts` bootstrap (verify event names against the pinned Wails version):
   - Wait `WindowRuntimeReady`; `AppService.GetSettings()` → theme init → store; `VaultService.Status()` → `vaultState` → render unlock gate or app shell.
   - Subscribe ONCE to every master §5 event and route: `vault:state-changed` → store + shell effect (on lock: call `shell.destroyTerminals()` — a NO-OP function wired to the real impl in 4c; reset tree/tabs; show unlock gate); `app:toast` → toasts; `terminal:status|data|exit`, `ssh:forward`, `sftp:progress`, `vault:hostkey-prompt`, `vault:key-prompt` → store updates / prompt modals (task 8).
6. `components/unlock.ts`: first-run mode ("Create master password" + confirm; min 8 chars; simple length/charset heuristic hint — NO external libs) and unlock mode; Enter submits; wrong password → inline error + CSS shake; success → `SessionService.Tree()` → store → app shell.
7. Shell:
   - Left panel: search-input placeholder (4b), toolbar with [+ Session] [+ Folder] buttons wired to store intents `requestNewSession(parentID)` / `requestNewFolder(parentID)` whose handlers are deliberate no-ops with `console.debug` until 4b; empty-state text "No sessions yet".
   - Splitter: pointer-event drag, clamp 240–480 px, persist `window.leftWidth` via `AppService.SaveSettings` (debounce 300 ms, partial update), apply via `--left-w`.
   - Right pane: empty state "No open sessions — create one from the left panel"; status-band placeholder (4c fills it).
   - TEMPORARY dev hook (delete in 4d): `window.__dsmDev` truthy → shows a small "Lock" button calling `VaultService.Lock()`, plus dev-only QA helpers.
8. Primitives:
   - `ui/dialog.ts`: Promise-based modal — focus trap (Tab cycling), Esc closes, backdrop-click policy param, `aria-modal`, open/close animations.
   - `components/toasts.ts`: top-right stack; `toast(level, message)`; auto-dismiss 4 s info / 6 s error.
   - `components/confirm.ts`: `confirmDialog({title, message, confirmLabel, danger}) => Promise<boolean>` (the A8 "affected count" is part of message).
   - `components/context-menu.ts`: `openContextMenu(x, y, items[{label, action, danger?, disabled?}])` — fixed positioning, viewport-clamped, closes on Esc / outside click / scroll.
   - Prompt modals: `vault:hostkey-prompt` → modal with host/port/keyType/fingerprint + [Accept and connect] / [Reject] → `VaultService.ApproveHostKey/RejectHostKey(connID)`; `vault:key-prompt` → single password input → `VaultService.SubmitKeyPassphrase(connID, pw)` (master §5 contract, modal = blocking UX).

## Verification
- `make lint` (tsc noEmit + eslint) clean; `make test` green; `make build` succeeds.
- Manual (`make run`, fresh config dir = remove `~/.config/shelve`):
  1. create-vault flow (weak-password hint, confirm-mismatch error) → unlock → shell renders in both themes.
  2. wrong master password → inline error + shake.
  3. theme: follows OS toggle live; manual light/dark persists across restart; no FOUC flash on reload.
  4. splitter drag clamps 240–480 and persists across restart.
  5. lock via dev hook → unlock gate; wrong password again → error.
  6. prompt modals visually QA'd via a dev hook rendering fake payloads (functional flow is QA'd in 4b/4c against real sshd).
  7. no console errors; 50 lock/unlock cycles → no listener growth (DevTools check).

## Exit criteria
Unlock gate + shell work in both themes from a clean config dir; store/event-bus is the single source of truth (review-verified: no `Events.On` outside `main.ts`); all primitives reusable by 4b–4d without changes.
