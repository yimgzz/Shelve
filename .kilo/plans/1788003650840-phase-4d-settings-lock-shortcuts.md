# Phase 4d — Settings dialog, lock & auto-lock, shortcuts, polish

**Type:** Frontend. **Prereq:** 4c. **Sub-plan 4/4 of old Phase 4 — FINAL GATE of the core UI.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (D3), §4 (settings.json schema), §6 (Settings, Shortcuts, gear placement), §8 (item 3)** before starting.

**Scope guard (important):** settings, lock, auto-lock, shortcuts, cleanup, and the full Phase 4 regression checklist. NO SFTP (5c), NO packaging.

## Goal
Settings dialog (3 groups, live apply), vault-lock UX (gear menu + confirm + auto-lock), the complete shortcut table, dev-hook cleanup, and the final manual QA pass that closes the old Phase 4.

## Tasks
1. `components/settings-dialog.ts` (4a dialog):
   - General: theme radio (System/Light/Dark — applies LIVE via `theme.setTheme`, reverts on Cancel), auto-lock minutes (number, 0 = off), SFTP browser checkbox (persisted only; the panel lands in 5c).
   - Terminal: fontFamily (text), fontSize (8–24), scrollback (500–100000) — applied live to existing xterm instances on Save (`term.options.*`; `options.scrollback` works live).
   - Files: textEditorCommand (free text, default `xdg-open`).
   - Buttons: [Cancel] (discard draft) / [Save] → single `AppService.SaveSettings(fullObject)` + "Settings saved" toast.
2. Gear menu (left-panel header; replaces the hidden 4a gear): [Settings…], [Lock vault…], [About] (small dialog: name + `AppService.GetVersion()`).
3. Lock: if any tab is in state `ready` → `confirmDialog` ("N active connection(s) will be closed. Lock the vault?") → `VaultService.Lock()` → the 4a switch runs (terminals destroyed, store reset, unlock gate). No confirm when nothing active.
4. Auto-lock (D3): only when `autoLockMinutes > 0` and vault unlocked — 60 s interval checks `now - lastActivity > N min`; `lastActivity` refreshed on `pointerdown`/`keydown` (throttled to 5 s); fires → `VaultService.Lock()` WITHOUT confirm.
5. `ui/shortcuts.ts` — global keydown router implementing the FULL master §6 table:
   - Ctrl+K / Ctrl+L → `focusSearch()`
   - Ctrl+T → connect the selected session; if none selected → open the editor with an empty root draft
   - Ctrl+W → close active tab (no confirm — A3)
   - Ctrl+Tab / Ctrl+Shift+Tab → cycle tabs
   - Ctrl+1…9 → activate nth tab
   - Ctrl+, → settings dialog
   - F2 / Delete → rename / delete selected tree node (delegate to 4b handlers)
   - Esc → close topmost modal / clear search (existing component behavior; router must not double-handle)
   - Ctrl+Shift+E → toggle `sftpBrowserEnabled` via `AppService.SaveSettings` (two-way with the dialog checkbox; the panel itself is 5c)
   - Typing rule (documented): while a form field has focus, all shortcuts except Esc are suppressed.
6. Cleanup:
   - Remove the 4a/4b TEMPORARY dev hook (Lock button etc.) — KEEP the `window.__dsmDev` flag (search timer, dev notes are useful).
   - Listener audit: verify the pattern "main.ts subscribes once; component-level listeners added on mount are removed on unmount"; add a header comment in `store.ts` documenting it. DevTools check: no listener growth across 50 modal cycles + 50 renders.
   - README: Shortcuts table; Development notes — `cmd/seed` tool, `make dev` in-container window limitation vs `make run` (master §7 wording).
7. FULL Phase 4 regression checklist (manual; annotate results in the commit message):
   1. fresh config dir → create → unlock → empty states.
   2. 3 folders + 12 sessions CRUD (4b list) + search on 300-session seed (record the < 10 ms timing).
   3. connect cycle (4c list) incl. error/retry/exit overlays.
   4. prompt flows: host-key accept + reject, key-passphrase.
   5. tabs: 3 open, cycle, Ctrl+3, middle-click, Ctrl+W, scrollback preserved on switch.
   6. themes: system + manual override survive restart; terminal palette follows.
   7. settings: font size live-applies; SFTP checkbox persists; auto-lock 1 min idles → locks.
   8. Lock with active connections → confirm → unlock → reconnect works.
   9. every row of the shortcuts table.
   10. no console errors; heap sane after 50 renders.

## Verification
- `make lint` clean; `make test` green; `make build` + `make run` (or `make dev` where the container window works) pass.
- Checklist items 1–10 all green, annotated in the commit message.

## Exit criteria
Master §6 fully implemented and QA'd; the entire old-Phase-4 scope is closed → proceed to 5a.
