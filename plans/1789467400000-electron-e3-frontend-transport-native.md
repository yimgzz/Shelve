# Phase E3 — Frontend transport swap + Electron native integration

**Type:** Frontend + Electron main/preload. **Prereq:** E2. **Next:** E4.
**End of this phase: the app is functionally complete on Electron.**

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read **§5 (RPC/event contract), §6 (UI), §8**; roadmap **§3, §4, §6**.

**Related files:** every file importing
`../../bindings/shelve/internal/wailsvc` (17 files, listed in AGENTS.md §5),
`frontend/src/main.ts`, `frontend/src/store.ts`,
`frontend/src/ui/clipboard.ts`, `frontend/src/ui/theme.ts`,
`frontend/index.html`, `electron/main.ts`, `electron/preload.ts`.

## Goal

Delete the Wails runtime from the renderer and drive the E1 backend through the
new RPC client, with **call sites unchanged apart from the import path**. Add
the small set of native capabilities Electron owns (file dialog, clipboard,
window state) behind a minimal, reviewed `contextBridge` API.

**Scope guard:** no DPI/GPU work (E4); no packaging (E5); no UI redesign. Every
component's behavior, DOM, CSS, event handling and user-visible text is
unchanged.

## Tasks

### 1. `frontend/src/rpc/` — the drop-in transport

- `endpoint.ts`: `resolveEndpoint(): Promise<{ wsURL: string; token: string }>`
  — calls `window.shelve.bridgeEndpoint()`; retries with backoff (the window may
  exist before the backend handshake) and never throws unhandled.
- `client.ts`:
  - opens `ws://<addr>/rpc?token=<token>` (text frames only);
  - request table `Map<number, {resolve, reject}>`, monotonic `id`;
  - `call(svc, method, args): Promise<unknown>` sends
    `{"id":n,"svc":…,"method":…,"args":[…]}`; rejects with
    `new Error(response.error)`; rejects all pending on disconnect with
    `new Error("bridge disconnected")`;
  - `on(event, handler): () => void` and `off`; dispatches
    `{"event":…,"data":…}` to handlers;
  - reconnect with backoff; a stable endpoint (process-lifetime) so no
    re-provisioning;
  - `ready(): Promise<void>` resolves on the first open.
- `types.ts`: TS mirrors of the DTOs (`NodeDTO`, `SearchResultDTO`,
  `SessionDTO`/`SessionInput`, `JumpHostDTO`/`Input`, `CredentialDTO`/`Input`,
  `SavedJumpHostDTO`/`Input`, `SftpEntryDTO`, `VaultStatus`, `Settings`) —
  field names exactly as in `internal/api/dto.go` and `internal/config`.
- `index.ts`:
  ```ts
  export const AppService = service<AppServiceApi>("AppService");
  export const VaultService = service<VaultServiceApi>("VaultService");
  // … all 8 services …
  export const events = { on, ready };
  ```
  `service()` returns a Proxy whose property access yields
  `(...args) => call(name, prop, args)`, so `await SessionService.Tree()`
  type-checks and behaves exactly like the Wails binding did.

### 2. Import swap (mechanical, 17 files)

Replace `from "../../bindings/shelve/internal/wailsvc"` (and the `../bindings/…`
variant in `main.ts` / `store.ts`) with the rpc module. No other change in
`gear.ts`, `jump-host-dialog.ts`, `prompts.ts`, `shortcuts.ts`,
`session-editor.ts`, `shell.ts`, `credential-dialog.ts`, `unlock.ts`,
`monitor-bar.ts`, `tree.ts`, `settings-dialog.ts`, `terminal-view.ts`,
`sftp-panel.ts`, `xterm.ts`, `autolock.ts`.

- `frontend/src/store.ts` header comment: replace "main.ts is the single owner
  of the Wails event bus" with "the rpc event bus" (comment only).

### 3. `frontend/src/main.ts`

- Drop `@wailsio/runtime` / `Events`; import `events` + services from `./rpc`.
- `EV` names stay byte-identical (master plan §5).
- `subscribeEvents()` → `events.on(EV.X, (payload) => handleEvent(EV.X, payload))`
  (payload arrives directly; delete `eventData()` and `Common`).
- `whenReady()`: DOM ready **and** `events.ready()` (the bridge socket opened).
  Delete `WindowRuntimeReady`.
- Theme change: remove the `Common.ThemeChanged` subscription; `ui/theme.ts`
  already has a `matchMedia('(prefers-color-scheme: dark)')` listener (plan
  P001), which fires in Chromium. Keep `onThemeApplied`.
- Terminal WS: unchanged (`terminal/ws.ts` still feeds `TermPool.write`);
  `EV.TerminalData` remains the pre-connect/fallback path (the engine keeps
  emitting it when the sink is inactive).
- Boot ordering: resolve endpoint → `events.ready()` → `AppService.GetSettings`
  → theme → `VaultService.Status()` → gate/shell. Identical semantics to today,
  minus the Wails runtime handshake.

### 4. `frontend/src/ui/clipboard.ts`

- Primary: `window.shelve.clipboard.writeText/readText` (Electron `clipboard`
  through IPC). Fallback: `navigator.clipboard` (works in Chromium under
  `file://` for user-gesture writes), then the existing `execCommand` legacy
  path. Keep the §8.3 rule: never log payloads, generic failure results.

### 5. `frontend/src/terminal/ws.ts`

- Replace `fetch("/termws-port")` (dead under Electron: there is no app HTTP
  server) with `window.shelve.bridgeEndpoint()` → `ws://<addr>/terminal?token=…`.
  Framing, split, backoff, and the output handler stay **unchanged**.
- Keep the fallback-to-events safety net.

### 6. Electron main/preload native integration

`electron/preload.ts` (`contextBridge.exposeInMainWorld("shelve", …)`):
- `bridgeEndpoint(): Promise<{addr,token}>` → `ipcRenderer.invoke("bridge:endpoint")`.
- `clipboard.readText()/writeText(t)` → `ipcRenderer.invoke("clipboard:…")`.
- `pickFile(): Promise<string>` → `ipcRenderer.invoke("dialog:pickFile")`
  (returns `""` on cancel, matching the old `AppService.PickFile` contract; the
  `session-editor.ts` call site swaps `AppService.PickFile()` →
  `window.shelve.pickFile()`).
- `windowState.onChange(cb): () => void` → `ipcRenderer.on("window:state", …)`.
- `app.quit()` → `ipcRenderer.send("app:quit")` (used by nothing today; added
  only if needed).

`electron/main.ts` handlers:
- `bridge:endpoint` → the E2 handshake endpoint (rejects before ready).
- `clipboard:*` → Electron `clipboard` module (main process).
- `dialog:pickFile` → `dialog.showOpenDialog(win, { title: "Select an SSH
  private key", properties: ["openFile"] })` → `filePaths[0] ?? ""`. Preserve
  the current behavior exactly (single file, full path).
- Window geometry (master plan A7): read `window.width/height` from
  `$XDG_CONFIG_HOME/shelve/settings.json` at startup (read-only, best effort) to
  size the window; on `resize`/`move` (debounced 300 ms) send `window:state`
  `{width,height}` to the renderer; the renderer merges it into the settings
  object and persists through `AppService.SaveSettings` (single writer = Go,
  same atomic path as `leftWidth`/`sftpWidth`). Clamp to `minWidth`/`minHeight`.
- Do **not** add native dialogs for `PickLocalFiles`/`DownloadThenSave`: this
  build's behavior is the documented manual/fallback path, and the no-change
  rule forbids altering it. (A native-dialog enhancement is a separate future
  plan.)

### 7. `frontend/index.html` + CSP

- Add a Content-Security-Policy meta:
  `default-src 'self'; img-src 'self' data:; font-src 'self' data:;
  style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline';
  connect-src ws://127.0.0.1:* ws://localhost:* http://localhost:*`.
  - `connect-src` must cover the loopback `/rpc` + `/terminal` sockets and the
    Vite HMR socket in dev.
  - The inline FOUC theme guard needs `script-src` inline; either keep
    `'unsafe-inline'` or replace the guard with a `sha256-<hash>` source
    computed at build time. **Resolve this in E3 and record which was chosen**
    (prefer the hash; fall back to `'unsafe-inline'` only if Vite dev breaks).
- `viewport` stays as-is (Chromium honors `devicePixelRatio` correctly; E4 owns
  the DPI work).

### 8. Delete the Wails renderer surface

- Confirm no file imports `@wailsio/runtime` or `../../bindings/…`:
  `rg "@wailsio/runtime|bindings/shelve" frontend/src` → no matches.
- `frontend/bindings/` is generated + gitignored: remove the directory.
- `frontend/vite-env.d.ts` / any `wails` type references: clean up.
- `frontend/tsconfig.json` no longer includes `bindings` (E2 already removed it).

## Verification

- `make lint` green (gofmt/vet + renderer & electron `tsc --noEmit`); the rpc
  proxy types must satisfy every call site without `any` escapes at the call
  boundary (internal `any` in `client.ts` is fine).
- `make test` (`-race`) green (Go untouched).
- `make build` + `make run` manual E2E against a real sshd (the old Phase 4/5 +
  P004/P006/P009 checklists, condensed — full parity is E7):
  1. Create vault → unlock → tree/search/editor; wrong password error.
  2. Connect (password, key, jump host, bastion kbdint) → terminal echo,
     resize, Ctrl+C, paste (Ctrl+Shift+V / right-click), select-to-copy.
  3. Prompt modals: host-key (accept/reject), key passphrase, kbdint prefill.
  4. SFTP right panel: browse, path bar, upload/download, edit-as-text, cancel.
  5. Monitor bar metrics; hosts/credentials/saved-jump-host dialogs.
  6. Settings (12 theme variants), shortcuts (US + RU), auto-lock, lock with
     active tabs → unlock.
  7. `[Browse…]` key picker returns a full path (Electron dialog).
  8. Window size survives restart; lock/unlock ×20 → no listener growth, no
     console errors, no leaked timers.
  9. Backend killed externally → main shows the error dialog and quits (no
     orphan); app quit → `pgrep shelve-backend` empty.
- `rg "wails|@wailsio" frontend/src electron` → no matches.

## Gate (this phase)

`make build`, `make test`, `make lint` green from a clean container, plus the
manual E2E above annotated in the commit message. Feature parity is
"complete except DPI/GPU tuning and packaging", which E4/E5 add.

## Exit criteria

The renderer is Wails-free and drives the backend entirely through the local RPC
+ terminal WS; the only native capabilities come from the reviewed preload API;
all features behave as before.
