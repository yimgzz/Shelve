# AGENTS.md

Guidance for AI coding agents working in **Shelve**.

A lightweight, fast, fully local SSH session manager. Go backend
(`golang.org/x/crypto/ssh`) as a **child process** + **Electron/Chromium**
frontend (vanilla TypeScript + Vite + xterm.js), no bundled GUI toolkit of our
own. Sessions live in an encrypted vault (Argon2id + AES-256-GCM)
under `$XDG_CONFIG_HOME/shelve`.

**Status: v2.0 — Electron architecture.** Phases E1–E6 of the migration
roadmap are done (legacy toolchain fully removed, docs rewritten); E7 is the
acceptance/QA gate. Linux only: `make build && make run` and `make appimage`.
All user-visible functionality is unchanged from the pre-migration app.

---

## 1. Read these first

The project is driven by a detailed, single-source-of-truth planning system in
`plans/`. Before touching any code, read the relevant plan:

| Reference | File | Contents |
|---|---|---|
| Master plan | [`plans/1789467100000-master-plan.md`](plans/1789467100000-master-plan.md) | Architecture, data model, interfaces, security model, build strategy, roadmap index (§1–§12). **The single source of truth.** |
| Migration roadmap | [`plans/1789467100000-electron-migration-roadmap.md`](plans/1789467100000-electron-migration-roadmap.md) | Migration strategy, transport contract, phase index E1–E7, cross-cutting gates and risks. |
| Migration phases | `plans/1789467200000-electron-e1-*.md` … `plans/1789467800000-electron-e7-*.md` | Per-phase backend bridge, Electron shell/build, frontend transport, HiDPI/GPU, packaging, cleanup, acceptance. |

Every phase plan references the master plan sections (`§`) that must be read
before implementing. **Follow the "read the §s before starting" instructions at
the top of each phase plan.**

- The roadmap is executed **strictly in order**; sub-phases were sized to fit a
  single focused agent session.
- A phase is done only when its exit criteria **plus `make lint` plus `make test`**
  pass on a clean container (`make test-integration` where stated), and it is
  committed after each sub-phase.
- The plan set is now **small on purpose**: the 28 legacy per-feature plans
  were deleted in E6 because they described a substrate that no longer exists
  (a beta GUI toolkit built on an embedded browser engine); their durable rules
  were ported into the master plan before deletion. Do not resurrect them — add
  new work to the master plan and a phase file instead.

## 2. Project layout

```
electron/
  main.ts                    # main process: spawn/supervise backend, window, native
                             # dialogs/clipboard, GPU/DPI flags, single instance, shutdown
  preload.ts                 # the reviewed contextBridge API (window.shelve)
cmd/
  shelve-backend/            # Go entry point: handshake on stdout, signals, service registration
  seed/                      # disposable vault/QA fixture generator (300 sessions)
internal/
  app/                       # composition root: wires config+store+vault+engine; master-password lifecycle
  api/                       # the service layer + DTOs (dto.go) — exposed 1:1 over /rpc
  bridge/                    # loopback RPC + event fan-out + per-run token + stdout handshake
  config/                    # XDG paths, settings.json load/save
  model/                     # types + validation (§4)
  store/                     # in-memory tree CRUD, order ops, move/delete semantics, persistence trigger
  vault/                     # argon2id KDF, AES-256-GCM envelope, create/unlock/lock, zeroization
  sshx/                      # args parser, known_hosts manager, host-key callback factory, auth builders
    args/                    # strict Extra Args parser (used by ValidateExtraArgs)
    knownhosts/              # app-managed known_hosts handling
  sshengine/                 # SessionManager: dial (jump chain), PTY, batched pump, forwards, reconnect
  sftp/                      # sftp ops: list/mkdir/rename/remove/get/put, temp-file editor flow
  monitor/                   # system-monitor metrics over a dedicated SSH connection
  termws/                    # binary terminal I/O framing over the /terminal WebSocket
frontend/
  index.html                 # CSP injected by vite.config.ts; FOUC theme guard inline
  src/
    main.ts                  # bootstrap, rpc event bus, unlock gate
    store.ts                 # tiny pub/sub state (tree, tabs, settings, search)
    rpc/                     # client.ts, endpoint.ts, types.ts, index.ts (service proxies), ipc.ts
    components/              # tree, tabs, session-editor, settings-dialog, sftp-panel,
                             # monitor-bar, unlock, toasts, confirm, context-menu, prompts,
                             # shell, statusbar, search, gear, credential/jump-host dialogs
    terminal/                # xterm.ts (TermPool), ws.ts (binary /terminal client)
    ui/                      # dom helpers, theme, themes, dpi, zoom, shortcuts, keys,
                             # dialog, clipboard, autolock, b64
    style/                   # base.css, themes.css (custom properties), components.css
    vite-env.d.ts
  vite.config.ts             # renderer build (base: "./")
  tsconfig.json
electron-builder.yml         # AppImage + --linux dir packaging metadata
Dockerfile.dev               # shelve-dev toolchain image (golang + Node + Electron/Chromium libs)
Makefile                     # docker-driven targets (§3)
build/icon.png               # the only file kept under build/ (electron-builder buildResources)
scripts/                     # build-electron.mjs, host-runtime-libs.txt, verify-appimage.sh
tsconfig.electron.json       # main/preload typecheck config
package.json                 # the ONLY JS manifest (root); version is the source of truth
docker/sshd/                 # integration-test sshd image (Dockerfile + sshd_config)
```

`dist-electron/` (esbuild main/preload bundles), `frontend/dist/` (Vite output),
`bin/`, `node_modules/` and `tmp/` are gitignored. Never edit generated output.

## 3. Build & development environment

**The host needs Docker ONLY.** All compilation happens inside the `shelve-dev`
container image (golang 1.25-trixie + Node 24 + Electron/Chromium runtime libs +
`electron-builder`). The `run` target is the only one that executes code on the
host; it needs the host distro's Electron/Chromium **runtime** libraries
(listed in `scripts/host-runtime-libs.txt`, documented in the README).

Do **not** install Go, Node, or Chromium packages on the host. Run everything
through the Makefile targets.

| Command | Action |
|---|---|
| `make dev-image` | Build the `shelve-dev` toolchain image (lazy: `dev`/`build` do it automatically) |
| `make dev` | Container hot-reload (Vite + Electron over X11); window opens on the desktop |
| `make build` | Release build in the container → `bin/linux-unpacked/shelve` (+ `bin/shelve-backend`) |
| `make run` | Run the unpacked build on the host (needs the Electron runtime libs) |
| `make test` / `make test-race` | Go unit tests in the container (with `-race`) |
| `make test-integration` | SFTP/SSH integration tests via testcontainers (needs the Docker socket) |
| `make lint` | `gofmt` + `go vet` (container) + renderer & Electron `tsc --noEmit` |
| `make smoke-vault` | Headless vault smoke test against a temp dir |
| `make seed` / `make unseed` | Seed/remove a 300-session QA vault into the host config dir |
| `make appimage` | Self-contained AppImage in the container → `bin/shelve-<version>-x86_64.AppImage` |
| `make appimage-check` | Extract + inspect + zero-dep smoke of the AppImage (`scripts/verify-appimage.sh`) |
| `make package` | Alias of `make appimage` |
| `make clean` | Remove `bin/`, `frontend/dist/`, `dist-electron/`, `node_modules/` |

Notes:
- `make dev` runs the app *inside* the container with X11 forwarding. On some
  desktops the in-container window cannot start (display/DBus restrictions,
  sandbox/namespace limits). The always-supported workflow is
  `make build` + `make run`.
- There is **no legacy GUI-toolkit build path** and no
  DEB/RPM/Flatpak/Windows/macOS target. Version source of truth is root
  `package.json` `version` (keep `internal/app.Version` in sync).

## 4. Tech stack & key rules

| Concern | Choice |
|---|---|
| Language | Go ≥ 1.25, module `shelve`, built as the `shelve-backend` child process |
| GUI | Electron **42.x** (exact pin in `package.json`, same major as VSCode); Chromium renderer; no GUI toolkit imported by Go |
| Main/preload build | TypeScript bundled by **esbuild** → `dist-electron/*.cjs` (no bundler framework) |
| Renderer | TS 5 + Vite 8 (vanilla), `@xterm/xterm` + addons `fit`, `webgl` (graceful fallback), `web-links`, `search`. No framework, no CSS framework. |
| SSH | `golang.org/x/crypto/ssh` |
| KDF/cipher | Argon2id (m=64 MiB, t=3, p=4, 16 B salt, 32 B key) + AES-256-GCM (random 12 B nonce, AAD `"dsmsv1"`) |
| SFTP | `github.com/pkg/sftp` (pinned) |
| IDs | `github.com/oklog/ulid/v2` |
| RPC WS | `github.com/coder/websocket` (pinned) |
| Transport | `internal/bridge`: one token-gated `127.0.0.1` listener serving `/rpc` (JSON) + `/terminal` (binary) |

### Layering rules (critical)

- **No package imports a GUI toolkit.** There is no GUI toolkit in Go any more;
  `internal/{model,store,vault,sshx,sshengine,sftp,monitor,config,termws}` are
  pure Go and must stay free of any Electron/RPC dependency. `sshengine`,
  `sftp` and `monitor` expose structural `Emitter`/`Dialer`/`TabProvider`
  interfaces; the composition root (`internal/app`) wires them.
- **All frontend↔backend traffic goes through `internal/bridge`** (RPC calls and
  events) or the terminal WebSocket. `internal/api` is the service layer;
  `internal/bridge` is the dispatcher/transport; do not add a second path.
- **`internal/bridge` and `internal/api` are the only packages that know about
  the transport.** `internal/bridge` may import `internal/api` and
  `internal/termws`; `internal/api` may import the domain packages; never the
  reverse.
- **The renderer↔main surface is the reviewed preload API only**
  (`electron/preload.ts` → `window.shelve`). Never enable `nodeIntegration`,
  never disable `contextIsolation`/`sandbox`, never add arbitrary `ipcRenderer`
  passthrough.
- DTOs live in `internal/api/dto.go`; never expose internal structs. The RPC
  surface is reflection-based over the registered services and pinned by a
  **surface golden test** — keep the registry explicit.
- **No plaintext credentials over the transport.** `Tree()`/DTOs expose only
  `AuthType`/`HasPassword`/`KeyPath`; the single documented exception is the
  bastion kbdint prefill (§6).
- **No large file bytes over the transport (A5).** Uploads/downloads pass
  *paths*; Go streams the bytes. Only terminal I/O and small events cross the
  loopback socket. Configuration export/import is also path-only: the renderer
  picks a file via `window.shelve.pickSaveFile()`/`pickOpenFile()` and calls
  `TransferService.Export`/`Import` with the chosen path.

## 5. Architecture

### Process model

- Electron **main** spawns `shelve-backend`, reads the one-line JSON handshake
  `{"event":"ready","addr":"127.0.0.1:PORT","token":"…"}` from **stdout**
  (stderr is logs), and provisions `{addr,token}` to the renderer through the
  preload `bridgeEndpoint()` bridge. On quit main sends `SIGTERM` (bounded, then
  `SIGKILL`); the backend handles `SIGINT`/`SIGTERM` and runs `App.Shutdown()`.
  If the backend exits unexpectedly, main shows an error and quits (no orphan).
- The backend's loopback listener is the **only** network surface besides the
  user's SSH hosts. `/rpc` is a JSON WebSocket (requests/responses + events);
  `/terminal` reuses the `internal/termws` binary framing. Both require the
  per-run token (32 random bytes, base64url) on the upgrade query string.
- The renderer is sandboxed (`contextIsolation: true`, `nodeIntegration: false`,
  `sandbox: true`) and reaches native capabilities only through the reviewed
  preload API (bridge endpoint, file dialog, clipboard, window state, display
  changes, zoom).
- No application data crosses stdio.

### RPC envelope

```jsonc
{"id":1,"svc":"SessionService","method":"Tree","args":[]}   // request
{"id":1,"result":[…]}{"id":1,"error":"…"}                    // response
{"event":"terminal:status","data":{…}}                       // server→client
```

Errors are the Go error string; the TS client rejects with `new Error(msg)`.

### Service surface (`internal/api`, exposed 1:1 over `/rpc`)

- **AppService**: `GetVersion`, `GetSettings`, `SaveSettings`.
- **VaultService**: `Status`, `CreateVault`, `Unlock`, `Lock`, `ApproveHostKey`,
  `RejectHostKey`, `SubmitKeyPassphrase`, `SubmitKbdintResponse`, `CancelKbdint`.
- **SessionService**: `Tree`, `Search`, `CreateFolder`, `RenameFolder`,
  `MoveNode`, `DeleteNode`, `CreateSession`, `Session`, `UpdateSession`,
  `DuplicateSession`, `ValidateExtraArgs`, `TestConnection`.
- **CredentialService**: `List`, `Create`, `Update`, `Delete`, `Get`, `Usage`.
- **JumpHostService**: `List`, `Create`, `Update`, `Delete`, `Get`, `Usage`.
- **TerminalService**: `Connect`, `Disconnect`, `Write`, `Resize`, `Reconnect`.
- **SftpService**: `IsActive`, `List`, `Mkdir`, `Rename`, `Remove`,
  `PickLocalFiles`, `Upload`, `Download`, `DownloadThenSave`, `EditRemoteText`,
  `CancelEdit`.
- **MonitorService**: `Start`, `Stop`.
- **TransferService**: `Export`, `Import` (encrypted `.shelve` configuration
  export/import; Merge/Replace modes; paths only).

Native file picking is `window.shelve.pickFile()` in the main process, not a
service method; configuration export/import use `pickSaveFile()`/`pickOpenFile()`
and pass the resulting path to `TransferService`.

### Event contract (backend → renderer, over `/rpc`)

| Event | Payload | Producer |
|---|---|---|
| `vault:state-changed` | `{unlocked: bool}` | vault/app lifecycle |
| `vault:hostkey-prompt` | `{connID, host, port, keyType, keyB64, fingerprint}` | sshx host-key cb |
| `vault:key-prompt` | `{connID, keyPath}` | encrypted private key |
| `vault:kbdint-prompt` | `{connID, name?, instruction?, questions:[string], echo:[bool], prefill?}` | bastion hop; `prefill` = the single §6 IPC exception (masked inputs only) |
| `terminal:status` | `{tabID, state:"connecting"\|"ready"\|"error"\|"closed", message?}` | sshengine |
| `terminal:data` | `{tabID, data b64}` | sshengine (fallback only; the `/terminal` WS is authoritative) |
| `terminal:exit` | `{tabID, exitStatus?}` | remote shell exit |
| `ssh:forward` | `{tabID, spec, state, localAddr?, error?}` | sshengine |
| `sftp:progress` | `{transferID, direction, doneBytes, totalBytes, fileName?, error?}` | sftp |
| `monitor:metrics` | `{tabID, hostname, cpuPercent, memUsedBytes, memTotalBytes, netUpBps, netDownBps, uptimeSeconds, diskUsedPct, diskRoot, dfText}` | monitor |
| `app:toast` | `{level, message}` | services |

Events emitted before any `/rpc` client connects are dropped; a full per-client
queue blocks the emitter rather than dropping a lifecycle event.

### Terminal transport (plan P005, retained)

- Binary frames `[u8 tabIDLen][tabID][u32 payloadLen][payload]`, tabID ≤ 64 B,
  payload ≤ 256 KB (bigger batches split).
- Go batches output ≤ 50 ms / ≤ 64 KB with an interactive fast path; the reader
  pauses when a tab's pending buffer exceeds 256 KB (flow control). Input rides
  the same socket JS→Go (independent reader), so Ctrl+C works even while output
  writes are blocked.
- The `terminal:data` event remains as a pre-connect/never-connected fallback
  (3 s grace) for headless tests.

### Threading / failure modes

- One goroutine per SSH connection (read pump, forward accepts); store access
  via mutex; per-tab emitter buffers.
- `Lock()` disconnects all sessions, zeroizes the key, emits
  `vault:state-changed`.
- Disconnect while active → `error` state + message, tab kept (A4); Reconnect
  re-dials the full chain. Port-forward bind failure is non-fatal
  (`ssh:forward failed` + toast).
- Backend crash → main shows an error and quits (no orphan). GPU-process crash
  → Chromium restarts it. Display change → debounced re-measure, never a reload.

## 6. Security model (binding — §8 of the master plan)

1. No plaintext credentials on disk: `vault.json` is the only secret store;
   `settings.json` and `known_hosts` contain none (gated by tests). **No
   plaintext credentials over the transport** — with ONE scoped exception: a
   bastion hop's stored password may cross it inside
   `vault:kbdint-prompt.payload.prefill` only, solely to prefill masked
   keyboard-interactive inputs; never persisted, logged, or cached. Kbdint
   answers typed by the user are never stored or cached. User-entered secrets
   also necessarily reach the backend over the same token-gated loopback
   listener — the master password (`VaultService.CreateVault`/`Unlock`) and the
   configuration export/import passphrase (`TransferService`) — and are never
   persisted, logged, echoed in errors, or returned.
2. Argon2id params fixed (m=64 MiB, t=3, p=4, 16 B salt, 32 B key);
   AES-256-GCM, random 12 B nonce per write, AAD `"dsmsv1"`.
3. Memory hygiene: master password and derived key **zeroized** after use;
   keys never logged; errors never include password/key material.
4. Config dir `0700`, files `0600` (asserted by test).
5. Host keys: TOFU via app-managed `known_hosts`; mismatch = hard fail, never
   auto-accepted.
6. SSH key files: read-only use; app never writes/rewrites them.
7. Vault writes always atomic (temp file + rename), never truncated on failure.
8. SFTP edit temp files `0600` under `tmp/`, swept on lock/exit/startup.
9. No network calls except to user SSH hosts and the token-gated loopback
   listener on `127.0.0.1` (backend RPC/terminal) plus local port-forward
   sockets. No telemetry, no update checks, no crash reporting.
10. Corrupt/undecryptable vault → refuse to unlock, never auto-overwrite.
11. Renderer hardening: `contextIsolation`, `nodeIntegration: false`,
    `sandbox: true`, the minimal reviewed preload API, and a CSP (injected by
    `vite.config.ts`). The AppImage sandbox fallback (`--no-sandbox` +
    `--disable-gpu-sandbox` when user namespaces are unavailable; override with
    `SHELVE_SANDBOX=0|1`) does not change any of the above.
12. The backend binds `127.0.0.1` only, inherits `XDG_CONFIG_HOME`, and exits on
    `SIGTERM`.

## 7. Conventions

- **Store concurrency:** single `Store` with `sync.RWMutex`; every mutation
  re-encrypts & atomically writes `vault.json`, debounced 300 ms; flush on
  `Lock` and app exit.
- **Tabs are ephemeral (A3):** closing the app closes all sessions; no tab
  restoration on relaunch. No auto-reconnect (A4) — manual [Retry]/[Close].
- **Session card:** exactly one auth method (password XOR key path); Jump Hosts
  are structured fields; Extra Args uses a strict parser (`internal/sshx/args`).
  Key passphrases are prompted once per connection, cached only in memory.
- **Bastion jump mode (P009):** a jump host flagged `Bastion` embeds the target
  in the SSH username (`user@target`) and is authed keyboard-interactively
  (multi-round, pre-filled from the stored password); after auth the bastion
  relays the session channel. Target login == bastion login; target port 22 and
  hostname/IPv4 only, no ProxyJump.
- **Search:** matches Name, Host, User (case-insensitive substring); folder path
  shown in results (A9). Session ID is a ULID (A10).
- **Destructive tree ops** (delete folder with children, delete session) require
  a confirm dialog showing the affected count (A8).
- **UI language is English only.** Shortcuts: Ctrl+K/L search, Ctrl+T connect,
  Ctrl+W close tab, Ctrl+Tab cycle, Ctrl+, settings, F2/Delete rename/delete,
  Esc close modal, Ctrl+Shift+E toggle SFTP browser. All shortcuts and terminal
  control keys are keyed to the physical key position (US layout) via
  `KeyboardEvent.code`, so they fire identically under any keyboard layout;
  plain text input remains layout-aware. Terminal keys: Ctrl+C always sends
  the interrupt (ETX, even with a selection); Shift+Backspace deletes the
  previous word (^W); Ctrl+Shift+V pastes the system clipboard into the active
  terminal; Ctrl+Shift+C copies the terminal selection (no-op when empty).
- **HiDPI/zoom model:** OS scale comes from Chromium per-monitor; the renderer
  watches `matchMedia('(resolution: Ndppx)')` plus the main-process debounced
  display event and re-measures/refits (no reload). User zoom is separate
  (`1.2 ** level` via `webFrame`, persisted in `settings.ui.zoomLevel`).
- **Linux display backend:** Wayland defaults to native
  (`--ozone-platform-hint=auto`) for fractional scaling; X11 uses Chromium's
  global scale. Overrides: `--ozone-platform=…` > `ELECTRON_OZONE_PLATFORM_HINT`
  > `SHELVE_DISPLAY_BACKEND=x11|wayland|auto`; `--force-device-scale-factor`
  forces one global scale.
- **GPU:** hardware acceleration on by default (VSCode parity); `--disable-gpu`
  / `--disable-hardware-acceleration` is the only off-switch; `--gpu-info`
  prints the GPU feature status and exits.
- **No functional change rule:** a change must not alter a user-visible
  behavior, DTO field, event name, or event payload. `internal/{vault,model,
  store,sshx,sshengine,sftp,monitor,config}` are preserved from the pre-Electron
  tree except for mechanical renames.
- **Integration tests** use build tag `integration` and spin up `docker/sshd`
  via testcontainers. Run with `make test-integration`.

## 8. Testing & QA

- **Go unit** (`make test`, run with `-race`): vault round-trip / wrong-password
  / tamper / lock; args parser matrix; known_hosts parse/write; store
  CRUD/move/delete/order; model validation incl. bastion and reference rules;
  settings load/save/defaults; perms & no-secret-leak assertions; bridge RPC
  (dispatch shapes, unknown method, panic recovery, token gate, event
  fan-out, surface golden list); termws framing.
- **Go integration** (`make test-integration`, tag `integration`): password &
  key auth, PTY echo, jump chains, local forwards, host-key mismatch rejection,
  disconnect event correctness, SFTP ops + edit, monitor metrics, 300-session
  perf smoke.
- **Frontend:** `tsc --noEmit` in `make lint`. No e2e framework; manual QA
  checklists per phase.
- **Electron manual matrix** (E4/E7): scale factors 100–200 %, X11 + Wayland,
  monitor moves, GPU on/off, AppImage launch.
- **Perf budgets:** search over 300 nodes < 10 ms; tree DOM rebuild < 50 ms;
  terminal responsive at 100 KB/s sustained output (3 MB/s flood survives).

## 9. Do's and don'ts

**Do:**
- Read the master-plan sections referenced by the phase plan before coding.
- Keep all frontend↔backend traffic inside `internal/bridge` + the `/terminal`
  WS; keep the preload API minimal and reviewed.
- Use atomic writes for `vault.json` and `settings.json` (tmp + rename).
- Zeroize password/key material after use; never log secrets.
- Return gofmt-clean code and keep the whole tree free of legacy GUI-toolkit
  references.
- Add unit tests alongside new backend logic; use `make test` (and
  `test-integration` where relevant) and `make lint` before finishing.

**Don't:**
- Install toolchain packages on the host — use the Docker targets.
- Add Node/Electron imports to the renderer or widen the preload surface beyond
  the reviewed API.
- Edit `dist-electron/` or commit `frontend/dist/`.
- Send credentials or large file bytes across the transport.
- Auto-accept unknown/mismatched host keys.
- Open any new non-loopback listener.
- Persist tab state or restore tabs on relaunch.
- Reintroduce cross-platform build targets (Windows/macOS, DEB/RPM/Flatpak) —
  Linux only.
- Introduce new make/npm/Go dependencies on the host toolchain.

## 10. Getting started for new work

1. Read `plans/` — pick the relevant master-plan sections + phase plan.
2. Make changes within the affected package(s); keep the Go/Electron layering
   (no GUI toolkit in Go; all traffic through the bridge).
3. Run `make test`, `make lint` (and `make test-integration` if the change
   touches SSH/SFTP networking) on a clean container.
4. Follow the phase plan's exit criteria and QA checklist; commit after each
   logical unit.
