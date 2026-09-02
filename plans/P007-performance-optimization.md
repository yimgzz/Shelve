# P007 — Performance & Stability Optimization (pre-release pass)

> Goal: identify bottlenecks affecting performance / UI responsiveness and eliminate
> defects WITHOUT changing any user-visible functionality. Every change below is
> behavior-preserving (guards, throttles, leak fixes, micro-optimizations).
> Scope: frontend TS + a small Go micro-opt. No new features.

## Context reviewed

Backend: `internal/store` (debounced persistence, CRUD, Tree/Search), `internal/vault`
(Argon2id + AES-GCM), `internal/sshengine` (dial/pump/batching/backpressure),
`internal/termws` (P005 WS transport), `internal/monitor` (2 s tick), `internal/sftp`
(streaming transfers), `internal/app` + `main.go` (composition root, shutdown order).
Frontend: `store.ts` (pub/sub fan-out), `tree.ts`, `tabs.ts`, `terminal-view.ts`,
`monitor-bar.ts`, `search.ts`, `main.ts` (event bus), `terminal/xterm.ts`, `ui/*`.

## What is already good (no action)

- Terminal output path (P005 WS): macrotask delivery, no promise-chain starvation,
  50 ms / 64 KB batching + interactive fast path + 256 KB backpressure cap.
- SFTP progress throttled ≥256 KB / ≥250 ms; transfers stream in Go (A5).
- Monitor uses a dedicated SSH connection (never contends with PTY output).
- Store persistence debounced 300 ms; `Flush()` on Lock and Shutdown; atomic writes.
- Shutdown order correct: termws close → engine → sftp/monitor close → store flush.
- Tree/search budgets (300 nodes <10 ms search, <50 ms rebuild) are met today.

## Findings

### A. Defects (leaks — must fix, zero behavior change)

| # | Location | Problem |
|---|----------|---------|
| L1 | [`search.ts`](frontend/src/components/search.ts:91) | `renderSearch` stores its `store.subscribe` return on `wrap.__unsub` but nothing ever invokes it. Each lock/unlock cycle adds one permanent subscriber to `store.listeners` (the old DOM node is discarded). Violates the listener-audit rule; unbounded growth. Fix: module-level `searchUnsub`, release at the start of the next `renderSearch` (the same pattern tree/tabs/monitor-bar use). |
| L2 | [`shell.ts`](frontend/src/components/shell.ts:140) | `leftModeUnsub = store.subscribe(applyLeftMode)` is intentionally "kept alive for the shell's lifetime", but `renderShell` is re-invoked after every lock/unlock → one orphaned subscriber per cycle. Fix: module-level `leftModeUnsub`, release at the start of `renderShell` before re-subscribing. |

### B. UI responsiveness (fan-out churn — fix with guards, identical behavior)

| # | Location | Problem | Fix |
|---|----------|---------|-----|
| F1 | [`monitor-bar.ts`](frontend/src/components/monitor-bar.ts:283) | `render()` is subscribed to the whole store: **every** `set()` wipes `.status-band` (`textContent = ""`) and rebuilds ~7 DOM subtrees — including on sftp:progress (up to 4×/s per transfer), tab status, search keystrokes, tree refreshes. Continuous layout churn + fights the Disk tooltip hover logic. | Early-return guard: recompute only when (a) visibility inputs changed (active tab id/state, `settings.monitoringEnabled`), or (b) the `store.monitor[tabID]` object identity changed (each metric event is a fresh object). Lifecycle Start/Stop block stays untouched inside the guarded body. |
| F2 | [`terminal-view.ts`](frontend/src/components/terminal-view.ts:131) | `reconcile()` runs on every store mutation: loops all panes/tabs, touches `style.display`, re-renders overlays even when nothing about tabs changed (monitor events, sftp progress, searchQ…). | Early-return guard comparing `state.tabs`, `state.activeTabID`, `state.settings.terminal` references with the previous call; skip everything when all three are identical. |
| F3 | [`xterm.ts`](frontend/src/terminal/xterm.ts:89) | `restoreTerminalFocus` (via the capture-phase keydown handler in [`main.ts`](frontend/src/main.ts:346)) calls `TermPool.recoverAll()` on **every** stray keydown while focus sits on `<body>`. Under repeated keypresses this can force repeated full renderer recovery. | Throttle `recoverAll()` to at most once per 300 ms (module-level timestamp). `TermPool.activate` still runs every time — the actual input-fix path is unchanged. |

### C. Micro-optimizations (optional, low value but safe)

| # | Location | Proposal |
|---|----------|----------|
| M1 | [`tabs.ts`](frontend/src/components/tabs.ts:110) | `el.scrollIntoView(...)` on every strip rebuild can scroll ancestor containers (the whole right pane). Replace with strip-local `scrollLeft` math (`strip.scrollLeft = clamp(el.offsetLeft - ...)`) so only the horizontal tab strip scrolls. Behavior intent preserved. |
| M2 | [`store.go`](internal/store/store.go:830) | `Search` calls `strings.ToLower` per session per field per query. Precompute lowercase `Name/Host/User` once at `Load` and on mutations (parallel lowercase copies); `containsFold` then does plain `strings.Contains`. Budget already met (<10 ms) — pure nicety. |

### D. Store pub/sub fan-out root cause (optional phase-2, discussed, not committed)

`store.set()` notifies every subscriber on every mutation. tree.ts/tabs.ts already filter
cheaply; F1/F2 guards remove the expensive cases. A generic `subscribe(selector)` API
(projection + change detection) would be the systemic fix but touches all 8 subscribers —
riskier than the targeted guards; defer unless benchmarks demand it.

### E. Observations — intentionally NOT changed (by design / security)

- Argon2id m=64 MiB, t=3, p=4: ~0.5–1.5 s unlock by design (§8.2). 
- Full vault re-encode + re-encrypt on each debounced autosave: negligible at 300 nodes.
- Monitor exec cadence (2 s, one script on the active tab only): acceptable.
- `xterm.ts` pokes `_renderService` private API for recovery: maintenance risk only
  (version pinned), do not touch for this release.
- Wails v3 beta pin: pinned version, isolated in `main.go` + `internal/wailsvc`.

## Implementation plan (behavior-preserving only)

1. Fix L1 — release search subscription on remount (`search.ts`).
2. Fix L2 — release shell left-mode subscription on remount (`shell.ts`).
3. Guard F1 — monitor-bar change detection (`monitor-bar.ts`).
4. Guard F2 — terminal-view reconcile early return (`terminal-view.ts`).
5. Throttle F3 — `TermPool.recoverAll` ≤1/300 ms (`xterm.ts`).
6. Optional M1 — strip-local scroll instead of `scrollIntoView` (`tabs.ts`).
7. Optional M2 — precomputed lowercase search keys (`internal/store/store.go` + tests).
8. Validation: `make lint`, `make test`, `make test-race`; manual QA (lock/unlock ×20
   with DevTools listener count stable; monitor bar updates; SFTP progress; tab switching;
   multi-monitor focus recovery; search while transferring).

## QA checklist (performed by implementer)

- Lock/unlock cycle 20×: no listener growth (DevTools `getEventListeners` / store audit),
  no console errors, no duplicate toasts.
- Active tab + monitoring ON: monitor bar refreshes every 2 s; during an SFTP download it
  must not rebuild more often than the progress cadence, tooltip behavior unchanged.
- Terminal: paste large text into `cat`, verify input/output still snappy (WS active).
- Focus recovery: switch workspaces / move window between monitors, type immediately.
- Tree/search/rename/create/move/delete: identical behavior to pre-change build.
- `make lint` (incl. `tsc --noEmit`), `make test`, `make test-race` green.