# Phase 4c — xterm.js terminal, overlays, status bar

**Type:** Frontend. **Prereq:** 4b. **Sub-plan 3/4 of old Phase 4. Next: 4d.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (A3, A4, A6), §5 (terminal events, backpressure note), §6 (Terminal, Tabs, status bar)** before starting.

**Scope guard (important):** terminal inside tabs only. NO new backend methods (3c bindings suffice), NO settings-dialog changes (4d).

## Goal
Real per-tab terminals (xterm.js): data pump, resize, input, error/exit overlays with Retry, correct status bar, and leak-free teardown of all instances on vault lock.

## Tasks
1. Deps: `npm i @xterm/xterm @xterm/addon-fit @xterm/addon-webgl @xterm/addon-web-links @xterm/addon-search` (install `addon-search` per master §3 but wire no UI for v1 — document). New `ui/b64.ts`: `b64ToBytes(s) → Uint8Array`, `bytesToB64(u8) → string` (atob/btoa single helpers used by pump + input).
2. `terminal/xterm.ts` — `TermPool`:
   - Per-tabID instance, created when the tab opens (so `terminal:data` is never lost during `connecting`): theme = current terminal palette (4a tokens via `theme.currentTheme()`), options from settings (`fontFamily`, `fontSize`, `scrollback`, `cursorBlink: true`, padding 8 px).
   - Renderer: try WebGL → on init error, catch + fallback to the default renderer (one `console.warn`, no retry loop).
   - Addons: Fit — `ResizeObserver` on the pane → `fit.fit()` → debounced 150 ms → `TerminalService.Resize(tabID, term.cols, term.rows)`; WebLinks enabled.
   - `term.onData` → buffer chunks, flush on `requestAnimationFrame` → `TerminalService.Write(tabID, bytesToB64(data))`.
   - `terminal:data` (via 4a bus → store effect → this module) → `term.write(b64ToBytes(payload))` for that tab (write even when the pane is hidden — xterm retains scrollback).
   - `destroyAll()` → dispose every instance (`term.dispose()`, addons disposed with it), clear the pool — wire this to the 4a shell hook (replaces the no-op): called on `vault:state-changed` lock; also `destroy(tabID)` on tab close.
3. `components/terminal-view.ts`:
   - Replaces the 4b placeholder card INSIDE `#terminal-pane`: one pane element per tab (`display:none` unless active); active switch → show + `fit()` + `term.focus()`.
   - State overlays (absolute over the pane, centered cards, real buttons):
     - `connecting`: "Connecting to host…" (subtle spinner, click-through to the pane disabled).
     - `terminal:status error` → card: `message` + [Retry] → `TerminalService.Reconnect(tabID)`, `term.clear()`, state → `connecting`; + [Close tab].
     - `terminal:exit {exitStatus?}` → card "Connection closed" (+ "exit code N" when present) + [Retry]/[Close tab]; tab state → `closed`.
   - `ready` removes any overlay.
4. `components/statusbar.ts` (fills the 4a status placeholder): for the active tab render `user@host:port` + `, via <jump1>[, <jump2>]` (from the tab's session snapshot) + forward summary built from `ssh:forward` events cached per tab in the store (e.g. `L 8080→db:5432 · D 1080`; a `failed` forward renders dim-red with the spec). No active/ready tab → empty.
5. Perf guard (master §6 budget): sustained 100 KB/s output must keep UI responsive (WebGL renderer + Go-side batching, §2 A6). If jank appears, tune the write-coalescing granularity and record the decision in commit message — NO new events/IPC.
6. Teardown verification: multiple lock/unlock cycles → no "GPU context lost" spam, no leak (DevTools heap + console check).

## Verification
- `make lint` clean; `make test` still green (backend untouched).
- Manual (`make run` against a real sshd — local sshd or the 3e container with a published port; X11 host run per master §7 note):
  1. connect: dot amber → green; echo works; window resize + splitter drag → remote pty resizes (verify `stty size` before/after).
  2. 3 tabs: switching keeps per-tab scrollback; input only on the active tab; close/middle-click works (4d adds the shortcut-table QA).
  3. remote `exit` → exit overlay with code; [Retry] re-dials fresh (buffer cleared, no prompt if host key cached).
  4. error state: connect to a dead port → red dot + error card with message; [Retry] after fixing; [Close tab].
  5. host-key prompt (accept + reject) and key-passphrase prompt — real flows through 4a modals.
  6. flood: `yes | head -c 1000000` → responsive UI, scrollback present, no console errors; manual FPS note recorded.
  7. status bar correct for a jump-host session with forwards (incl. a failed bind case).
  8. dev-hook lock → all instances disposed cleanly; unlock + reconnect works.
  9. no console errors throughout; heap stable across 50 tab open/close cycles.

## Exit criteria
Master §6 terminal spec works end-to-end against a REAL sshd in both themes; 100 KB/s feed smooth; error/exit/recovery paths demonstrated; teardown leak-free.
