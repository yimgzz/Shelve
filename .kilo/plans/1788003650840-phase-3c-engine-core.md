# Phase 3c — SSH engine core: dial chains, PTY, prompts, events

**Type:** Backend (main work). **Prereq:** 3b. **Sub-plan 3/5 of old Phase 3. Next: 3d.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (A1, A2, A4, A6), §5 (package layout, services, full event contract, threading/failure modes), §9, §11 (Wails isolation)** before starting.

**Scope guard (important):** engine core + prompt flow + service registration. Local port forwards and `TestConnection` belong to 3d — ignore `Parsed.Forwards` for now (but DO honor a parsed `ProxyJump` in the chain, task 1); leave `SessionService.TestConnection` on its `ErrEngineNotWired` placeholder. No container tests (3e owns those).

## Goal
`internal/sshengine.Manager`: connect any stored session (password or key, 0..N jump hosts + ProxyJump), PTY + batched output, host-key / key-passphrase prompt flows, status/exit events, Write/Resize/Disconnect/Reconnect/Shutdown — plus `TerminalService` + three `VaultService` prompt methods registered in Wails, and app/lifecycle wiring (Lock closes all sessions).

## Tasks
1. `internal/sshengine/manager.go` + `live.go`:
   - `type Emitter interface { Emit(event string, payload any) }` — define inside the engine package (structurally satisfied by `wailsvc.LateEmitter`; the engine MUST NOT import `internal/wailsvc` — wailsvc imports the engine).
   - `type Manager struct { mu sync.Mutex; conns map[string]*liveConn; emit Emitter; kh *knownhosts.Manager; PromptTimeout time.Duration /* default 120s, tests override */ }` — `New(emit, kh) *Manager`.
   - Event name constants + payload structs (exact JSON field names per master §5; single source of truth in this package).
   - `Connect(session *model.Session) (tabID string, err error)`:
     - `tabID` = ULID; the `*model.Session` is stored on the liveConn (needed by Reconnect).
     - Hop chain = structured `session.JumpHosts` (in order), then the parsed `ProxyJump` (if present — appended after structured jumps per master §2 D5), then the target as `{Host, Port, User, Auth}`.
     - Per hop: `args.Parse(session.ExtraArgs)` gives dial timeout (`Options.ConnectTimeout` seconds, default 10); `net.DialTimeout("tcp", ...)`; `ssh.NewClientConn` with `sshx.NewHostKeyCallback(kh, engineApprover)`; auth via `sshx.AuthMethods(hopAuth, nil)`.
     - Key-passphrase flow: on `ErrKeyPassphraseRequired` → emit `vault:key-prompt {connID: tabID, keyPath}` and suspend; `SubmitKeyPassphrase(tabID, pw)` resolves; retry auth once; `ErrKeyPassphraseWrong` → fail the tab. No resolution within `PromptTimeout` → fail tab (`"key passphrase prompt timed out"`).
     - Host-key flow: unknown key → emit `vault:hostkey-prompt {connID: tabID, host, port, keyType, keyB64, fingerprint}` and suspend on a pending-prompt slot (one pending prompt per connID; prompt slots are keyed by connID, not tab — 3d's TestConnection reuses the same machinery with an ephemeral connID). `ApproveHostKey(tabID)` / `RejectHostKey(tabID)` resolve it; a second call with no pending prompt → typed `ErrNoPendingPrompt`.
     - Hop error attribution: wrap every hop failure: `"jump host 1/2 (user@host:port): <err>"` (target hop: `"target host:port: <err>"`).
     - Intermediate hops keep their clients alive; final hop: `RequestPty("xterm-256color", 80, 24, ssh.TerminalModes{...})` + `Shell()`; emit `terminal:status {tabID, "ready"}`.
     - Tab record states: `connecting → ready | error | closed`. On dial failure: tear down partial chain, KEEP the record (state `error` + message) so the UI can render Retry/Close and Reconnect can re-dial with the same tabID (master §2 A4).
     - Read pump (goroutine per ready conn): batch per master §2 A6 — flush on 50 ms tick OR ≥16 KB OR close → `terminal:data {tabID, data(base64)}`. Backpressure: pending buffer > 1 MiB → pause reading until drained (master §5).
     - Shell exit: `terminal:exit {tabID, exitStatus?}` then state `closed` + teardown (pty session, all chain clients). Record stays (UI removes the tab / Retry).
   - `Write(tabID, dataB64 string) error` — decode → pty `Stdin.Write`; typed `ErrUnknownTab` / `ErrTabNotReady` (connecting/closed).
   - `Resize(tabID, cols, rows int) error` — `Session.WindowChange`; no-op when not ready.
   - `Disconnect(tabID) error` — close pty session then clients last→first, emit `terminal:status {tabID, "closed"}`, remove record; unknown tab → typed `ErrUnknownTab`.
   - `Reconnect(tabID) error` — allowed from `error`/`closed` records (and `ready`: disconnect+redial); reuses the stored session, re-emits `connecting`; prompts may re-appear (host keys may be cached and skip).
   - `Shutdown(ctx)` — `Disconnect` all, bounded (per-conn ≤ 3 s total), idempotent. Used by app exit and Lock.
   - `Tabs() []TabInfo` (state introspection for tests).
2. `internal/app` wiring:
   - Create `knownhosts.New(config.File("known_hosts"))` (filename constant per master §4) and `sshengine.New(a.emitter, kh)`.
   - Lock flow: `wailsvc.VaultService.Lock()` must call `engine.Shutdown(3s ctx)` BEFORE `vault.Lock()` (master §5: disconnect all, zeroize key, emit `vault:state-changed`) — add the engine as a `NewVaultService` constructor parameter.
   - `App.Shutdown()` calls engine Shutdown before the store flush.
3. `internal/wailsvc`:
   - New `TerminalService` (`terminalservice.go`, `NewTerminalService(st *store.Store, mgr *sshengine.Manager)`): `Connect(sessionID string) (string, error)` (resolve session from store; unknown ID → typed error; vault must be unlocked), `Disconnect`, `Write`, `Resize`, `Reconnect` per master §5 signatures.
   - `VaultService` gains `ApproveHostKey(connID string) error`, `RejectHostKey(connID string) error`, `SubmitKeyPassphrase(connID string, pw string) error` — delegate to the manager; locked vault → `vault.ErrLocked` (password never logged — master §8.3).
   - Register `TerminalService` in `main.go` following the existing service pattern.
4. Unit tests (fake emitter capturing events; NO real network): state machine on failed dial (unresolvable host → `connecting` then `error`, record retained, Reconnect legal); typed errors for unknown-tab Write/Resize/Disconnect (double-Disconnect → `ErrUnknownTab`); pending-prompt slots: Approve/Reject/NoPending; `SubmitKeyPassphrase` with no pending prompt → `ErrNoPendingPrompt`; `Shutdown` idempotent + goroutine delta stable (test helper: settle loop over `runtime.NumGoroutine`, `-race` clean).

## Verification
- `make test` (race) green (existing + new); `make lint` clean.
- `wails3 build` succeeds and `frontend/bindings/` contains `TerminalService` + the three new `VaultService` methods (content check only).
- `make build && make run` — window still opens (frontend unchanged).

## Exit criteria
Every master §5 event this phase owns is emitted in the correct order (asserted with the fake emitter); prompts suspend and resolve (unit-tested through the public Approve/Reject/Submit methods); Leak-free: after `Shutdown` no goroutines/sockets remain (test), `-race` clean.
