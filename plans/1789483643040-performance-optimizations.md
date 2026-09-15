# Shelve — Performance Optimization Plan

## Goal

Reduce runtime CPU/RAM, improve SSH/session responsiveness, and shrink the
AppImage from its current **135 MB (325 MB unpacked)** — without regressing
correctness, security, or (unless explicitly flagged) user-visible behavior.

## Measured baseline (current `bin/`)

AppImage `shelve-0.1.0-x86_64.AppImage` = 135 MB; `bin/linux-unpacked` = 325 MB:

| Component | Size | Notes |
|---|---|---|
| `shelve` (Electron/Chromium main) | 220 MB | irreducible |
| `locales/*.pak` (57 langs) | 47 MB | only `en-US` needed |
| `resources/backend/shelve-backend` | 13.4 MB | built **unstripped** |
| `LICENSES.chromium.html` | 20 MB | Electron-shipped |
| `icudtl.dat` | 10.9 MB | irreducible |
| `resources.pak` + `.pak` | 7.7 MB | irreducible |
| `libGLESv2`, `libvk_swiftshader`, `libvulkan`, `libffmpeg` | 16 MB | GPU/codec fallbacks |

Packaging fact: `electron-builder.yml` sets **no `toolsets.appimage`**, so
`AppImageTarget.js:68` takes the legacy FUSE2 path, whose default is
**gzip** (`AppImageTarget.js:88-91`). Root `compression` is unset (normal).

## Decisions (confirmed with user)

1. Include **all** optimizations, but clearly separate behavior-preserving
   from behavior-changing (flagged) items.
2. AppImage may switch to **xz** compression (longer packaging, much smaller
   artifact).

## Constraints

- Follow AGENTS.md layering/security rules; no new dependencies; Linux only.
- Behavior-changing items (Phase 4) require explicit functional-change
  sign-off before merge and a note that the "no functional change rule" is
  intentionally relaxed for them.
- Validate each phase on a clean container: `make lint`, `make test`,
  `make test-race`, `make test-integration` (SSH/SFTP phases).

---

## Phase 1 — AppImage / package size (low risk, largest immediate win)

1. **Prune Chromium locales.** Add top-level `electronLanguages: ["en-US"]`
   to `electron-builder.yml` (supported via `config.electronLanguages`,
   `ElectronFramework.js:67`). Expected: locales 47 MB → ~1 MB.
2. **xz compression.** Add `appImage.compression: "xz"` under the existing
   `appImage:` block in `electron-builder.yml` (legacy FUSE2 forwards
   `-comp xz -Xdict-size 100% -b 1048576`, `appImageUtil.js:62-69`).
   Expected: AppImage drops to roughly the 85–100 MB range; packaging time
   increases substantially.
3. **Strip the Go backend.** In `Makefile`, build the shipped backend with
   `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"` in the `payload`
   target (keep a debug variant only if needed). Expected: 13.4 MB → ~9 MB.
   Also removes absolute build paths.
4. **Build-time only (optional):** `payload` currently always runs
   `npm ci`, wiping/redownloading `node_modules`. Guard it like `node-deps`
   (`[ -d node_modules ] || npm ci ...`) or skip when `package-lock.json`
   is unchanged.
5. **Flagged/optional — legal review before doing:** `LICENSES.chromium.html`
   (20 MB) can be excluded via an `afterPack` hook. Chromium's BSD license
   generally requires shipping the notice; recommended to **keep** unless
   legal approves.

**Validation:** `make appimage && make appimage-check`; record
`ls -la bin/*.AppImage` before/after; confirm `bin/linux-unpacked/locales`
contains only `en-US.pak`; `make run` smoke.

---

## Phase 2 — Frontend CPU/RAM (behavior-preserving)

1. **Cache the WebGL probe.** `useDefaultRenderer()`→`isSoftwareWebGL()`
   (`frontend/src/terminal/xterm.ts:232-261`) creates a canvas and acquires a
   WebGL context **for every tab created**. Compute once per app session
   (module-level memo) — the GPU stack does not change mid-session. Removes a
   per-tab GPU context churn.
2. **Singleton encoders/decoders.** `new TextEncoder()` / `new TextDecoder()`
   are allocated per keystroke and per output frame:
   - `frontend/src/terminal/ws.ts:38,68` (per frame)
   - `frontend/src/terminal/xterm.ts:102,448,454` (per key event)
   - `frontend/src/ui/b64.ts:11-26`
   Hoist to module-level constants.
3. **Fast base64 helpers.** `bytesToB64` builds a binary string with
   `+=` char-by-char (`b64.ts:22-25`) — O(n²)-ish for 64 KB batches. Use a
   chunked `String.fromCharCode` / `TextDecoder("latin1")` path. (The RPC
   fallback path still uses this per batch.)
4. **Tree render cost.** (`frontend/src/components/tree.ts`)
   - Selection-only changes (`selectedID`) currently trigger a full DOM
     rebuild (`renderTreeBody` → `rerender`, lines 105-113, 120-133). Update
     only the `.selected` classes when `tree`/`searchQ` are unchanged.
   - `countChildren` (line 527) walks each folder subtree on **every** render;
     precompute descendant counts once per `NodeDTO` snapshot (O(n) total).
5. **Optional:** selector-based store subscriptions or per-slice listeners to
   avoid running every listener on every unrelated `store.set`.

**Validation:** `make lint` (typecheck); manual — open 10+ tabs and watch
context count/CPU in DevTools; confirm monitor/search/tree still update.

---

## Phase 3 — Go runtime CPU/RAM (behavior-preserving)

1. **Terminal pump buffer reuse.** `internal/sshengine/live.go:508-634`:
   `pending = append(pending, buf[:n]...)` and `pending = nil` on every flush
   reallocate under sustained output. Introduce a pooled/double-buffered
   `pending` (e.g. `sync.Pool` of 64 KB buffers, hand off on flush) to cut GC
   pressure. Behavior/order unchanged.
2. **Cache the data sink.** `flush()` takes `m.mu` on every flush to read
   `m.dataSink` (lines 578-580). Capture the sink once at pump start; the sink
   is set once at boot (`internal/app/app.go:98`).
3. **SFTP copy-buffer pool.** `internal/sftp/transfer.go:139-163` allocates a
   64 KB buffer per transfer; use a package pool.
4. **Monitor ticker timer.** `internal/monitor/manager.go:265-269` uses
   `time.After(interval)` per iteration; use a single `time.NewTicker`.
5. **Optional micro:** `internal/bridge/events.go:22-36` allocates a client
   slice per event; with one client this is minor — special-case len<=1 or
   reuse a snapshot.

**Validation:** `make test -race`; manual flood test (`yes`/`tail -f`) — watch
backend RSS and CPU stay flat at 100 KB/s and survive a 3 MB/s flood.

---

## Phase 4 — Flagged behavior-changing items (require sign-off)

Each item below must be labelled `behavior-changing` in its commit and the
AGENTS.md "no functional change rule" exception noted.

1. **Concurrent `/rpc` dispatch (highest runtime value).**
   `internal/bridge/server.go:259-282` handles requests **sequentially** on the
   read loop, so a blocking call starves every other call:
   `SftpService.Upload/Download/DownloadThenSave` (`internal/api/sftp_service.go:93-116`)
   block for the entire transfer, and `TestConnection` blocks for the dial
   timeout. During a large upload, tree/search/monitor/RPC UI work hangs.
   - **Recommended design:** keep normal calls sequential; dispatch a
     documented blocking-method allow-list on bounded goroutines (semaphore,
     e.g. 4–8 in flight). Serialize all socket writes through the existing
     `writeLoop`/`out` channel (coder/websocket allows one writer), so
     responses and events never race. Client matches responses by id, so
     completion order is irrelevant.
   - Alternative (narrower): make only `Upload`/`Download`/`DownloadThenSave`
     asynchronous in the service layer, returning a transfer id and finishing
     via `sftp:progress` — this changes the RPC contract and is **not**
     recommended.
2. **Monitor `df -h` cadence.** `internal/monitor/collect.go:21-31` runs the
   full `df -h` listing every 2 s and ships it in `dfText` on every
   `monitor:metrics` event (JSON over loopback). Fetch the full listing every
   N ticks / on change, cache it in the ticker, and keep the payload shape
   (tooltip may lag by up to N ticks). Reduces remote CPU, SSH bytes, and
   per-tick JSON/serialization churn.
3. **Monitor connection reuse (risky, optional).** `DialMonitorClient` opens a
   *second full SSH chain* (TCP + KEX + auth) per monitored tab
   (`internal/monitor/manager.go:63-73`, `internal/sshengine` dial path), kept
   alive alongside the PTY connection. Running monitor execs as an extra SSH
   *session channel* on the tab's existing final-hop client would halve
   handshakes/keys for monitored tabs. The original dedicated-connection fix
   was about not sharing the PTY *session*; a separate `NewSession` on the
   same `*ssh.Client` multiplexes over an independent channel window. Must be
   proven not to stall PTY output via `make test-integration` before adoption;
   otherwise keep the dedicated connection.

**Validation:** `make test`, `make test-race`, `make test-integration`,
`make lint`. Manual: start a multi-GB SFTP upload and concurrently browse /
search / switch tabs (RPC concurrency); watch remote `df` load on a
many-mount host (cadence); flood `tail -f` while monitoring (connection reuse).

---

## Suggested execution order

1. Phase 1 (size) — independent, immediate, easy to verify.
2. Phase 2 + Phase 3 (behavior-preserving runtime) — batch per package.
3. Phase 4.1 (RPC concurrency) — highest responsiveness win, needs review.
4. Phase 4.2 (monitor cadence), then 4.3 (connection reuse) only if
   integration tests prove it safe.

## Validation summary

- Commands: `make lint`, `make test`, `make test-race`,
  `make test-integration`, `make appimage`, `make appimage-check`.
- Metrics to capture before/after: AppImage bytes; `bin/linux-unpacked` size;
  locale file count; backend binary size; backend + renderer RSS at idle, with
  10 tabs, and under a 3 MB/s terminal flood; wall-clock of a 1 GB SFTP
  upload with concurrent RPC.

## Risks

- xz packaging is slow (acceptable per decision) and needs `xz-utils`/
  `libarchive-tools` in `Dockerfile.dev` (already installed).
- Locale pruning changes the language set: the app's UI is English-only
  (AGENTS.md §7), but Chromium's own UI/`--lang` is set from the OS locale
  (`electron/main.ts:105-116`); `en-US`-only may affect non-English Chromium
  strings and `--lang` fallback. Verify the `--lang` path or keep the
  matching locale(s).
- Concurrent RPC dispatch must preserve the single-writer WebSocket rule and
  bound goroutine growth; a non-concurrency-safe service would surface as a
  race under `-race`.
- Buffer pooling can mask ownership bugs if a flushed buffer is reused while
  the sink still reads it; the sink contract copies/serializes before return
  (verify in `termws`).

## Out of scope

- Reducing the 220 MB Electron/Chromium main binary.
- Removing GPU/SwiftShader/codec libraries (`libvk_swiftshader.so`,
  `libvulkan.so.1`, `libGLESv2.so`, `libffmpeg.so`) — needed for GPU parity and
  `--disable-gpu` fallback.
- Any change to vault cryptography/KDF parameters or the security model.
- macOS/Windows/DEB/RPM/Flatpak targets (Linux-only per AGENTS.md).
