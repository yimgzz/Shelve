# Phase E2 — Electron shell + rebuilt build system

**Type:** Build system + Electron main/preload. **Prereq:** E1. **Next:** E3.

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read **§3, §5, §7, §8**; roadmap **§3–§6**.

**Related files:** `Dockerfile.dev`, `Makefile`, `frontend/vite.config.ts`,
`frontend/package.json`, `frontend/tsconfig.json`, `.gitignore`,
`.dockerignore`, `cmd/shelve-backend` (E1), `build/config.yml` (version only).

## Goal

A packaged Electron application that:

1. spawns the E1 Go backend (`bin/shelve-backend`), reads its stdout handshake,
   and owns its lifecycle (single instance, quit, crash);
2. opens one `BrowserWindow` (sandboxed, context-isolated) that in this phase
   shows a **placeholder page** (the real frontend is rewired in E3);
3. is built entirely in Docker through the Makefile:
   `make build` → `bin/linux-unpacked/shelve`, `make run` → launch it;
4. removes the Wails build path entirely (no wails3 CLI in the toolchain, no
   `Taskfile.yml` in the build).

**Scope guard:** no renderer/UI migration (E3); no DPI/GPU tuning (E4); no
AppImage (E5); no file deletions beyond the build path (E6). The service layer
and terminal WS from E1 are used as-is.

## Decisions locked here

| # | Decision |
|---|---|
| E2-D1 | **One root `package.json`** owns the whole JS build (Electron main/preload, renderer deps, Vite, TypeScript). `frontend/package.json` and `frontend/package-lock.json` are removed; the lockfile moves to the repo root. |
| E2-D2 | `electron/main.ts` + `electron/preload.ts` are bundled by **esbuild** to CommonJS (`dist-electron/main.cjs`, `dist-electron/preload.cjs`) — no bundler framework, one small dep. |
| E2-D3 | Renderer = Vite with `base: "./"` so `loadFile` resolves relative assets (`file://`). Dev loads `SHELVE_DEV_URL`; production loads `frontend/dist/index.html`. |
| E2-D4 | The Go backend is a **plain child process**, not a native addon. Production path `process.resourcesPath/backend/shelve-backend`; dev path `bin/shelve-backend`. |
| E2-D5 | Stdio is lifecycle only: stdout = the one-line handshake, stderr = logs. No application data over stdio. |
| E2-D6 | Build-time version source of truth moves to root `package.json` `version` (keep `internal/app.Version` in sync); `build/config.yml` stops being read. |
| E2-D7 | Build only Linux. Delete the Wails CLI from the image and the cross/alt/format targets from the Makefile. |

## Tasks

### 1. Root `package.json` (new)

```jsonc
{
  "name": "shelve",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "main": "dist-electron/main.cjs",
  "scripts": {
    "build:renderer": "vite build --mode production",
    "build:electron": "node scripts/build-electron.mjs",
    "typecheck": "tsc -p frontend/tsconfig.json --noEmit && tsc -p tsconfig.electron.json --noEmit",
    "dev:renderer": "vite",
    "dev:electron": "node scripts/build-electron.mjs && electron ."
  },
  "dependencies": {
    "@xterm/addon-fit": "^0.10.0",
    "@xterm/addon-search": "^0.15.0",
    "@xterm/addon-web-links": "^0.11.0",
    "@xterm/addon-webgl": "^0.18.0",
    "@xterm/xterm": "^5.5.0"
  },
  "devDependencies": {
    "electron": "<pin exact>",
    "electron-builder": "^<pin>",
    "esbuild": "^<pin>",
    "typescript": "^5.5.0",
    "vite": "^8.0.5"
  }
}
```

- **Pin `electron` exactly** (no `^`) and record the Chromium/Node versions in
  AGENTS.md (E6). **Target the same Electron major VSCode pins on `main`
  (verified 2026-09-15: `42.10.0`, `.npmrc` `target="42.10.0"`)** so the
  Chromium rendering/DPI behavior discussed in E4 matches the reference
  implementation; its Chromium major sets the Vite `build.target`.
- Delete `@wailsio/runtime`.
- `scripts/build-electron.mjs`: `esbuild` entry points `electron/main.ts` and
  `electron/preload.ts`, `bundle: true`, `platform: "node"`, `format: "cjs"`,
  `target: "node<ElectronNode>"`, `external: ["electron"]`, `outdir:
  "dist-electron"`. Preload must stay a single self-contained file.

### 2. `electron/main.ts`

```ts
// single instance (A7)
if (!app.requestSingleInstanceLock()) app.quit();
app.on("second-instance", () => win?.focus());

// E2 baseline: hardware acceleration ON (VSCode parity: VSCode never appends
// --disable-gpu; its ONLY off-switch is app.disableHardwareAcceleration()).
// Accept both spellings; E4 adds the DPI/GPU feature switches.
if (process.argv.includes("--disable-gpu") ||
    process.argv.includes("--disable-hardware-acceleration")) {
  app.disableHardwareAcceleration();
}

const backend = spawn(backendPath(), [], { stdio: ["ignore", "pipe", "inherit"] });
// read stdout line-by-line until the handshake parses:
//   {"event":"ready","addr":"127.0.0.1:PORT","token":"…"}
let endpoint: { addr: string; token: string } | null = null;

const win = new BrowserWindow({
  width: startWidth, height: startHeight, minWidth: 960, minHeight: 540,
  backgroundColor: "#06070f",
  webPreferences: {
    preload: path.join(__dirname, "preload.cjs"),
    // VSCode parity (windows.ts defaultBrowserWindowOptions + windowImpl.ts):
    // contextIsolation/nodeIntegration are Electron defaults; VSCode sets
    // sandbox:true, spellcheck:false, enableWebSQL:false, autoplayPolicy, and
    // backgroundThrottling:false only where output must stay live.
    contextIsolation: true, nodeIntegration: false, sandbox: true,
    spellcheck: false, enableWebSQL: false,
    autoplayPolicy: "user-gesture-required",
    backgroundThrottling: false, // terminal output stays live when unfocused
  },
});
```

- `ipcMain.handle("bridge:endpoint", () => endpoint)` — gates the renderer until
  the handshake exists; returns `503`-style rejection before ready.
- `app.on("window-all-closed", () => app.quit())` — single-window app (A7);
  Linux only, so no macOS re-activate branch.
- `before-quit`: `backend.kill("SIGTERM")`, wait for exit with a bounded
  timeout (e.g. 3 s, matching `app.exitShutdownTimeout`), then `SIGKILL`.
- `backend.on("exit")` when not quitting → `dialog.showErrorBox("Shelve backend
  stopped")` and quit.
- Window geometry: read the persisted `window.width/height` from
  `$XDG_CONFIG_HOME/shelve/settings.json` (read-only, best-effort; fall back to
  1280×800) to create the window at the last size. Persisting *changes* is
  wired in E3 via the renderer's `SaveSettings`.
- No menu bar (vanilla app); `autoHideMenuBar: true` or `Menu.setApplicationMenu(
  null)`.

### 3. `electron/preload.ts`

- Expose one namespaced API via `contextBridge.exposeInMainWorld("shelve", …)`.
  Phase E2 exposes only `bridgeEndpoint()`; E3 adds `clipboard`, `pickFile`,
  `windowState`, `onThemeChange`, `quit`. Keep the surface minimal — every
  addition is reviewed against §8.

### 4. `electron/placeholder.html`

- A tiny static page (title + "electron scaffold ok" + the resolved bridge
  address once `bridgeEndpoint()` resolves) used as the window content in E2 so
  the phase is verifiable before the renderer migration. Deleted in E3.

### 5. `frontend/vite.config.ts` + `frontend/tsconfig.json`

- Remove the `@wailsio/runtime/plugins/vite` import/plugin and the
  `WAILS_VITE_PORT` logic. Set `base: "./"`, `server.host: "127.0.0.1"`,
  `server.port: 5173`, `server.strictPort: true`,
  `build.outDir: "dist"` (unchanged path), `build.target` aligned with the
  pinned Electron Chromium.
- `tsconfig.json`: `include: ["src"]` (drop `"bindings"`).

### 6. `Dockerfile.dev` (rewrite)

- `FROM golang:1.25-trixie`; Node + npm from Debian trixie (verify the version
  satisfies Vite/Electron builders; use NodeSource only if needed — record the
  decision).
- apt: `build-essential pkg-config ca-certificates git file`
  + Electron/Chromium **runtime** libs (so the container can also run the app
  under `xvfb` for smoke checks):
  `libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0
  libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libpango-1.0-0
  libcairo2 libasound2 libxss1 libxtst6 libsecret-1-0`
  + packaging: `fakeroot libarchive-tools zstd xdg-utils`
  + headless smoke: `xvfb xauth x11-utils`.
- Remove: `libgtk-4-dev`, `libwebkitgtk-6.0-dev`, `libglib2.0-dev-bin`,
  `libdbus-1-dev`, `wails3` install, `fuse3` (electron-builder AppImage needs
  none), `libglib2.0-0t64`, `x11-apps`.
- `ENV ELECTRON_CACHE=/root/.cache/electron
  ELECTRON_BUILDER_CACHE=/root/.cache/electron-builder` (mounted as named
  volumes for cache persistence).
- Keep `git config --system --add safe.directory /app`; `WORKDIR /app`.

### 7. `Makefile` (rewrite)

Variables: `APP := shelve`, `IMAGE := shelve-dev`, `ROOT`, `CONFIG`, UID/GID,
`APP_VERSION := $(shell node -p "require('./package.json').version" 2>/dev/null
|| echo 0.0.0)`.

`CACHE_FLAGS` gains `-v shelve-dev-electron:/root/.cache/electron
-v shelve-dev-electron-builder:/root/.cache/electron-builder` alongside the Go
module and npm caches.

| Target | Action |
|---|---|
| `dev-image` / `ensure-image` | build/inspect the new `shelve-dev` image |
| `build` | `go build -o bin/shelve-backend ./cmd/shelve-backend` → `npm ci` → `npm run build:renderer` → `npm run build:electron` → `npx electron-builder --linux dir` → `bin/linux-unpacked/`; `fix-owner` |
| `run` | `./bin/linux-unpacked/shelve` (host needs the Electron runtime libs; documented in E5/README) |
| `dev` | container, X11-forwarded, `xvfb`-free: `npm run dev:renderer` + `npm run dev:electron` via a tiny supervisor (`concurrently` not required; a shell `&`/`wait` is enough) — document the `make build && make run` fallback |
| `test` / `test-race` / `test-integration` / `smoke-vault` | unchanged Go commands |
| `lint` | gofmt + `go vet` + `npm run typecheck` |
| `seed` / `unseed` | unchanged |
| `clean` | `rm -rf bin frontend/dist dist-electron node_modules` |
| `package` | alias of `appimage` (defined in E5) |

- Remove `wails-init`; remove `appimage-alt`, `appimage-alt-image`,
  `ensure-appimage-alt-image`; remove `deb`, `rpm`, `build-win`, `build-darwin`,
  `flatpak` stubs; leave `appimage`/`appimage-check` to E5.
- `fix-owner` list: `/app/bin /app/frontend/dist /app/dist-electron
  /app/node_modules`.

### 8. `.gitignore` / `.dockerignore`

- `.gitignore`: drop `frontend/bindings/`; add `dist-electron/`. Keep `bin/`,
  `node_modules/`, `frontend/dist/`, `tmp/`. Remove the now-dead Wails entries
  (`build/linux/appimage/build`, `build/windows/nsis/...`, `.task`, iOS/Android
  overlays) — those paths are deleted in E6.
- `.dockerignore`: `bin`, `node_modules`, `frontend/node_modules`,
  `frontend/dist`, `dist-electron`, `.git`, `tmp`.

## Verification

- `make dev-image` succeeds; image contains `node`, `npm`, `go`, `xvfb`; does
  **not** contain `wails3`.
- `make build` from a clean container produces:
  `bin/shelve-backend`, `dist-electron/main.cjs`, `dist-electron/preload.cjs`,
  `frontend/dist/index.html`, `bin/linux-unpacked/shelve`.
- `make test` (`-race`) green (Go only; unchanged from E1).
- `make lint` green (gofmt/vet + both `tsc` projects).
- Manual in-container smoke: `xvfb-run -a ./bin/linux-unpacked/shelve` with a
  temp `XDG_CONFIG_HOME` starts, spawns the backend, reads the handshake, and
  logs the placeholder page load — no `wails://`, no WebKit errors. Exit is
  clean (backend receives SIGTERM, no orphan process; `pgrep shelve-backend`
  empty afterwards).
- Single-instance: launching the app twice focuses the first window.
- `rg "wails" Makefile Dockerfile.dev frontend/package.json` → no matches
  (frontend source still has Wails imports; that is E3).

## Gate (this phase)

`make build` + `make test` + `make lint` green in a clean container; the
in-container `xvfb` smoke opens the placeholder window and exits cleanly.
`make run` on the host is validated in E5 (after packaging/documenting the host
runtime libs).

## Exit criteria

The Wails toolchain is gone from `Dockerfile.dev`/`Makefile`; the Electron shell
spawns and supervises the E1 backend, opens one sandboxed window, and the whole
thing builds in Docker through `make build`. One root `package.json` and one
lockfile own the JS build.
