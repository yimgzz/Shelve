# Phase E1 — Standalone Go backend + loopback RPC bridge

**Type:** Backend (main work). **Prereq:** none. **Next:** E2.
**Fully verifiable headlessly: no Electron, no display.**

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read **§3 (stack), §5 (process model, RPC/event contract, threading), §8
(security items 1, 9, 10)** and the roadmap
[`1789467100000-electron-migration-roadmap.md`](1789467100000-electron-migration-roadmap.md)
**§2–§4, §6**.

**Related code to read first:** `internal/app/app.go` (composition root),
`internal/wailsvc/emitter.go`, `internal/wailsvc/appservice.go` (the only Wails
imports), `internal/termws/server.go` (the existing terminal WS), `main.go`.

## Goal

Turn the Go process into a **standalone backend** that any frontend can drive,
with the Wails dependency gone:

1. `internal/wailsvc` becomes `internal/api` — the plain service + DTO layer,
   importing **no** GUI toolkit.
2. A new `internal/bridge` package serves the services over one loopback
   `http.Server`: `/rpc` (JSON request/response + server→client events) and
   `/terminal` (the existing `internal/termws` binary protocol).
3. `cmd/shelve-backend` is the new entry point; on ready it prints a one-line
   JSON handshake (`{addr, token}`) to stdout so the Electron shell can spawn it
   and provision the endpoint. Logs go to stderr.
4. `go.mod` no longer requires `github.com/wailsapp/wails/v3`; the root
   `main.go` is deleted.

**Scope guard:** do NOT touch `internal/{config,model,store,vault,sshx,
sshengine,sftp,monitor,termws}` behavior, DTO fields, event names or event
payloads. The only edits there are none. No frontend work (E3). No Electron
(E2).

## Why this is safe

All 8 services are already plain structs with `(T, error)`-shaped methods and
JSON DTOs (master plan §5). The bridge's reflection dispatcher binds exactly the
same shapes the Wails code generator bound, so the call surface cannot drift.
The terminal byte path is already a loopback WebSocket; the bridge only
co-locates it with `/rpc` on one listener.

## Tasks

### 1. `internal/wailsvc` → `internal/api` (mechanical)

- `git mv internal/wailsvc internal/api`; `git mv` the `_test.go` files too.
- `package wailsvc` → `package api` in every file.
- Update imports in `internal/app/app.go` (`wailsvc.` → `api.`) and in every
  `internal/api/*_test.go`.
- `internal/api/appservice.go`: delete the Wails import, `ErrPickerUnavailable`
  and `PickFile`. `GetVersion`, `GetSettings`, `SaveSettings` stay verbatim.
  (Native file picking moves to the Electron main process in E3; the frontend
  call site for `PickFile` is `session-editor.ts` and is rewired in E3.)
- Keep `Emitter`, `FuncEmitter`, `LateEmitter` in `internal/api` (they are
  transport-neutral). The bridge implements `api.Emitter`.
- Delete the package doc paragraph claiming "the only package that may import
  Wails" and replace it with the new layering rule (see AGENTS.md, E6).

### 2. `internal/bridge` — one loopback listener, token-gated

`server.go`:
- `type Server struct` owning: a `net.Listener` on `127.0.0.1:0`, an
  `*http.Server`, a `token string`, the registration table (below), the
  connected `/rpc` clients, and a `*termws.Server`.
- `New(termwsSrv *termws.Server) *Server` — generates a 32-byte token with
  `crypto/rand` (base64url, no padding).
- `Start() (addr string, err error)` — binds the listener and serves
  `ServeHTTP`; idempotent (second call returns the stored address).
- `Handshake() string` — `{"event":"ready","addr":"127.0.0.1:PORT","token":"…"}`
  (one line, `json.Marshal`).
- `ServeHTTP`:
  - `/rpc` → `handleRPC` (upgrade, then request loop + event writer).
  - `/terminal` → delegate to `termwsSrv.ServeHTTP` (existing upgrade/origin
    logic). `termws.Start` is **not** called; `termws` runs listener-less.
  - anything else → 404.
- Auth: `authenticate(r)` reads `?token=` and compares with
  `subtle.ConstantTimeCompare`; missing/wrong → `401` before upgrade. The token
  is the only gate; no cookies, no ambient credentials.
- `Close()` — closes `/rpc` connections, the listener, and delegates to
  `termwsSrv.Close()`.

`dispatch.go` (the reflection RPC):
- `Register(name string, svc any)` — stores the service and enumerates its
  exported methods once, building an allow-list `map[string]reflect.Method`.
  Only registered `(service, method)` pairs are callable.
- `Invoke(svc, method string, rawArgs []json.RawMessage) (any, error)`:
  - unknown service/method → `"unknown method %s.%s"` (never leak the registry).
  - arg-count mismatch → `"invalid argument count for %s.%s"`.
  - binds each input parameter with `json.Unmarshal`; type mismatch → the
    unmarshal error (so the frontend sees a usable message).
  - calls; `recover()` → `"panic in %s.%s: %v"` (logged, never panics the
    server).
  - result binding: 0 returns → `nil`; 1 → result; 2 → last must be `error`
    (nil → result, non-nil → error). Any other shape → registration-time panic
    so a mis-shaped method fails the `Register` call, not runtime.
- Request loop per `/rpc` connection:
  `{"id":N,"svc":"…","method":"…","args":[…]}` → one response frame
  `{"id":N,"result":…}` or `{"id":N,"error":"…"}`. Requests are handled
  sequentially per connection (services are already mutex-guarded); the event
  writer is a separate goroutine so events never block responses.

`events.go`:
- `Emit(event string, payload any)` (implements `api.Emitter`) — marshals
  `{"event":event,"data":payload}` and fans out to every connected `/rpc`
  client through a per-client buffered channel (cap 4096) drained by the
  connection's writer goroutine. Pre-connection events are dropped (same as
  `LateEmitter` today). If a client's buffer fills, block the emitter rather
  than drop lifecycle events; terminal bytes never use this path (the `/terminal`
  sink is authoritative), so this channel stays low-rate.
- Set the default `log` output to stderr in `cmd/shelve-backend` so stdout is
  reserved for the handshake line.

### 3. `cmd/shelve-backend/main.go`

```
log.SetOutput(os.Stderr)
a, _ := app.New()                     // unchanged composition root
srv := bridge.New(a.TerminalWS())
srv.Register("AppService", a.AppService())
… all 8 services …
a.SetEmitter(srv)                     // replaces the Wails FuncEmitter
addr, _ := srv.Start()
fmt.Println(srv.Handshake())          // the one stdout line
install SIGINT/SIGTERM → a.Shutdown(); srv.Close()
select {}                             // run until signalled
```

- Delete the root `main.go` (the Wails bootstrap, `//go:embed`, webkit env,
  `application.*`).
- The backend keeps its own `internal/app` shutdown ordering: signal →
  `srv.Close()` (drops the terminal sink first) → `a.Shutdown()` (engine, sftp,
  monitor, store flush) — identical to today's `OnShutdown`.
- `app.New()` must not start `termws.Start` anymore: remove that call from
  `internal/app/app.go` (the bridge owns the listener now); keep the fallback
  emitter wiring so a never-connecting client still routes `terminal:data`
  events through the bridge.

### 4. `go.mod`

- `go mod tidy` inside the dev container; confirm `github.com/wailsapp/wails/v3`
  is gone and `go.sum` shrank. No other dependency changes.

### 5. Tests (all in-container, `-race`)

`internal/bridge/dispatch_test.go`:
- a local `fakeService` with methods `Echo(S) (S, error)`, `Bare() string`,
  `OnlyErr() error`, `NoArgs()`; invoke through `Invoke` for each shape.
- unknown service, unknown method, wrong arg count, wrong arg type, panic
  recovery.

`internal/bridge/server_test.go`:
- a real `coder/websocket` client: missing token → 401; wrong token → 401;
  correct token → upgrade; a full request/response round-trip through
  `ServeHTTP`.
- handshake line: `Handshake()` parses as JSON with a non-empty `addr`/`token`.
- `/terminal` is served by the mounted `termws` server (upgrade succeeds with
  the token; a frame reaches the configured input handler).
- two RPC clients: events fan out to both.

`internal/bridge/events_test.go`:
- `Emit` produces the exact `{event,data}` JSON for each master-plan §5 event
  name used by the backend, with the DTO field names unchanged (guards the
  contract).
- pre-connection emits are dropped without error; `Close` releases waiters.

**Golden surface test** `internal/bridge/surface_test.go` (the anti-drift
gate): register the real `api` services built by a test composition root and
assert every method that Wails used to bind is present in the registry — i.e.
the method-name list equals the documented list: `AppService.{GetVersion,
GetSettings,SaveSettings}`, `VaultService.{Status,CreateVault,Unlock,Lock,
ApproveHostKey,RejectHostKey,SubmitKeyPassphrase,SubmitKbdintResponse,
CancelKbdint}`, `SessionService.{Tree,Search,CreateFolder,RenameFolder,MoveNode,
DeleteNode,CreateSession,Session,UpdateSession,DuplicateSession,
ValidateExtraArgs,TestConnection}`, `CredentialService.{List,Create,Update,
Delete,Get,Usage}`, `JumpHostService.{List,Create,Update,Delete,Get,Usage}`,
`TerminalService.{Connect,Disconnect,Write,Resize,Reconnect}`,
`SftpService.{IsActive,List,Mkdir,Rename,Remove,PickLocalFiles,Upload,Download,
DownloadThenSave,EditRemoteText,CancelEdit}`, `MonitorService.{Start,Stop}`.
The list is the contract; adding/removing a method requires updating this test.

`internal/termws` tests stay green unchanged.

## Verification

- From a clean container: `go build ./...`, `go test -race ./...`, `go vet
  ./...` — all green; `gofmt -l` clean (keep excluding `build/`, which still
  holds the wails-templated Go files until E6 deletes them).
- `go build -o bin/shelve-backend ./cmd/shelve-backend`; run it with a temp
  `XDG_CONFIG_HOME`; assert the first stdout line is the JSON handshake and the
  process exits cleanly on `SIGTERM`.
- A small manual WS probe (a `go test` in `bridge`) proves: create vault →
  `VaultService.CreateVault` → `SessionService.Tree` → events received — all
  without any GUI.
- `rg "wailsapp/wails" go.mod go.sum internal/ cmd/` → no matches.
- `rg "PickFile" internal/` → no matches (moved to Electron in E3).

## Gate (this phase)

`go build ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l` clean.
Frontend `tsc` is **not** a gate here: `frontend/bindings/` is about to be
deleted and the frontend is switched to the local `rpc` module in E3. Note this
explicitly in the commit message so the transitional red state is intentional.

## Exit criteria

The Go process is a toolkit-free backend that exposes the complete service
surface (golden test) over a token-gated loopback RPC + the unchanged terminal
WS; the Wails dependency and the Wails bootstrap are gone; all Go tests pass
with `-race`.
