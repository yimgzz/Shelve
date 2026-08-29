# Phase 4b — Session tree, search, session editor, tabs

**Type:** Frontend + small backend merge rule. **Prereq:** 4a. **Sub-plan 2/4 of old Phase 4. Next: 4c.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (A8, A9, A10), §5 (SessionService, events), §6 (Tree, Search, Tabs, Session editor, Empty state)** before starting.

**Scope guard (important):** left panel + tabs + editor. The terminal pane is a placeholder card (4c provides xterm). Settings dialog, gear menu, auto-lock, shortcut table = 4d.

## Goal
A fully functional left panel (tree CRUD + live search results) and the session editor modal, plus the tab strip driving REAL `TerminalService.Connect` calls (terminal rendering lands in 4c in the same pane container).

## Tasks
1. `components/tree.ts`:
   - Render `store.tree` recursively: folder rows (chevron toggle — expand state is component-local, NOT persisted in v1; folder icon; name; descendant count badge), session rows (session icon + name).
   - Selection: single, highlight current; hover highlight.
   - Double-click session (and "Connect" menu item) → `store.connectSession(session)` (task 4).
   - Context menus (4a primitive), contextual:
     - empty tree background / folder: New Session, New Folder
     - session: Connect, Edit, Duplicate, Move to…, Delete
     - F2 = rename (inline input replaces label; Enter/blur commits, Esc cancels): folder → `RenameFolder`; session → `UpdateSession` with name only.
     - New Folder → inline input row under the target → `CreateFolder`.
     - New Session → editor modal (draft, `parentID` = folder or `""` for root). Edit → editor prefilled from the session DTO.
     - Duplicate → `DuplicateSession`. Move to… → submenu: root + all folders EXCLUDING the node itself and its descendants → `MoveNode(id, newParentID, index=sibling-end)`.
     - Delete → `confirmDialog` with the count from `DeleteNode` (A8: show affected sessions+folders; confirm AFTER fetching the count).
   - Toolbar: [+ Session] (root-draft editor), [+ Folder] (root inline) now active (replace the 4a no-op intents).
   - Empty state (no sessions): "No sessions yet — press [+ Session]" centered in tree area.
   - After every successful mutation: re-fetch `Tree()` into store; errors → `app:toast`-style toast via local `toast()`.
2. `components/search.ts` (left-panel header):
   - Input, 100 ms debounce, `×` clear button, Esc clears (component exposes `focusSearch()` for the 4d shortcut).
   - Query state: tree body replaced by flat Results list: sessions whose Name/Host/User contains `q` (case-insensitive substring; host/user exist only on sessions); row = `name  host — path/to/folder` with `<mark>` highlights (master §2 A9).
   - Single-click selects the session (tree syncs selection); double-click connects; no matches → `No results for "<q>"`; empty query → back to tree.
   - Perf: flat scan over ≤300 nodes; `console.time` note behind `window.__dsmDev` (budget < 10 ms, master §6; recorded in 4d QA).
3. `components/session-editor.ts` (4a dialog):
   - Fields per master §6: Name*, Host*, Port (default 22), User*, auth radio [Password | SSH key]:
     - Password: input; when EDITING an existing password session, placeholder "leave blank to keep the current password" (backend merge rule, task 5).
     - Key: path input + [Browse…] — verify native file-picker support in the pinned Wails v3 version: if its dialog API exists, add a tiny `AppService.PickFile()` binding; otherwise plain text input (document in README via 4d). Helper under the field: "Key passphrase is asked on connect, never stored".
     - Jump Hosts repeater: rows (host, port, user, password-or-key + Browse), [Add jump host], per-row remove `×`.
     - Extra Args: monospace input; helper text from master §6 ("Supported: -L, -D, -o ServerAliveInterval|ServerAliveCountMax|ConnectTimeout|StrictHostKeyChecking, ProxyJump=user@host[:port]"); inline validation on blur via `SessionService.ValidateExtraArgs` (3a real parser); red error text under the field.
   - Footer: [Test connection] — works on the UNsaved draft → `SessionService.TestConnection(draftDTO)`; host-key prompt modals (4a) may appear mid-test = correct; result as inline status / toast. [Cancel], [Save] → `CreateSession`/`UpdateSession` → `Tree()` refresh + toast.
   - Server/model validation errors → inline field errors.
4. `components/tabs.ts` + right-pane placeholder:
   - Strip above the right pane from `store.tabs`: label = session name; status dot (amber pulsing connecting / green ready / red error / gray closed); `×` on hover — closes in ANY state, no confirm (A3); middle-click closes; click activates; overflow = horizontal scroll; active tab kept visible.
   - `store.connectSession(session)`: `TerminalService.Connect(sessionID)` (async) → optimistic tab creation (state `connecting`, session snapshot), set active; on returned Connect error → tab state `error` + `errorMessage` (the 3c engine keeps failed tab records so Retry works).
   - Tab close → if state != `closed`: `TerminalService.Disconnect(tabID)`; remove; activate neighbor. `terminal:status` events already update tab state via the 4a bus.
   - Right pane (4b version): `#terminal-pane` wrapper (STABLE id — 4c mounts xterm inside) containing a placeholder card: session label, live state, error message when `error`, [Close] button.
5. Backend merge rule (small, keep `wailsvc_test.go` green + new tests): `SessionService.UpdateSession` — when updating an existing session whose stored auth is a password and the incoming DTO's password is `""` → preserve the stored password (same for each jump host); result must still pass `model.Validate`. Document on the method.
6. QA seed tool: `cmd/seed/main.go` — writes a 300-session TEST vault (Phase 2 `internal/model` fixtures) into the config dir (`--dir` override flag); Makefile targets `make seed` / `make unseed` (unseed removes `vault.json` + `known_hosts`, keeps settings, prints what it did). Keep as a permanent dev tool.

## Verification
- `make lint` clean; `make test` green (new wailsvc merge tests).
- Manual (`make run`):
  1. `make unseed` → `make seed` → unlock → 300-node tree renders; search "db" → results with highlights, no-results case, Esc restores tree.
  2. create 3 folders + 12 sessions (one password, one key, one jump-host) — all CRUD, F2 rename, duplicate, move (into descendant hidden from menu), delete with count confirm.
  3. editor: invalid Extra Args → inline error; valid `-L …` + `ProxyJump=…` save OK; Test connection against a reachable sshd (dev machine or 3e container via published port) → toast OK + host-key prompt modal accept path; wrong password test → inline error.
  4. double-click session → tab appears, dot amber → green/red per reachability; close via ×, middle-click, [Close].
  5. dev-hook lock → tabs cleared; unlock → empty tab strip.

## Exit criteria
Left panel + tabs + editor fully usable; every tree/search/editor operation works through the real bindings with no console errors; 300-node tree rebuild < 50 ms (measured, recorded in commit message); `make lint` + `make test` green.
