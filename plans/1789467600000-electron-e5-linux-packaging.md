# Phase E5 — Linux packaging: local build/run + AppImage

**Type:** Build system + packaging. **Prereq:** E4. **Next:** E6.
**Hard requirement 5: Linux only — `make build && make run` and
`make appimage`; `make appimage-alt` and all related files deleted.**

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read **§7, §8, §12**; roadmap
[`1789467100000-electron-migration-roadmap.md`](1789467100000-electron-migration-roadmap.md)
**§1, §7**.

**Related files:** `Makefile`, `Dockerfile.dev`, `bin/`, `build/` (icon),
`scripts/verify-appimage.sh`, `Dockerfile.appimage-alt` (delete),
`electron/main.ts` (sandbox fallback), `README.md` (packaging section).

## Goal

1. `make build` → a locally runnable Electron app (`bin/linux-unpacked/shelve`)
   spawning the Go backend; `make run` launches it on the host.
2. `make appimage` → a self-contained
   `bin/shelve-<version>-x86_64.AppImage` built entirely in Docker.
3. **Delete the entire ALT/p11 AppImage pipeline**: `Dockerfile.appimage-alt`,
   `make appimage-alt`, `make appimage-alt-image`, `ensure-appimage-alt-image`,
   the `shelve-dev-appimage-alt` image references, and their cache volumes.
4. Remove the DEB/RPM/Windows/macOS/Flatpak stubs (Linux-only delivery).

## Why the alt pipeline dies

`appimage-alt` existed to lower the glibc/libstdc++ floor produced by the
WebKitGTK/linuxdeploy toolchain (trixie glibc 2.41 vs alt:p11 glibc 2.38).
Electron ships its own Chromium plus most of its runtime and does not link the
host GTK/WebKit stack; electron-builder produces the AppImage with its own
`app-builder`, so the cross-build-image shim is no longer needed. The Electron
baseline (glibc within Chromium's supported range) is documented once in README.

## Tasks

### 1. `electron-builder.yml` (new, repo root)

```yaml
appId: io.shelve.app
productName: Shelve
executableName: shelve
copyright: "(c) 2026, Shelve"
directories:
  output: bin
  buildResources: build
files:
  - package.json
  - dist-electron/**
  - frontend/dist/**
extraResources:
  - from: bin/shelve-backend
    to: backend/shelve-backend
asar: true
npmRebuild: false
linux:
  target:
    - AppImage
  category: Network
  icon: build/icon.png
  artifactName: shelve-${version}-${arch}.AppImage
  synopsis: Lightweight local SSH session manager
  description: Local-first SSH session manager with an encrypted vault.
  desktop:
    Name: Shelve
    Comment: Local-first SSH session manager
    Categories: Network;Utility;
appImage:
  license: null
```

- `extraResources` keeps the Go binary **outside** the asar, at
  `resources/backend/shelve-backend`, matching the E2 path resolution.
- Icon: reuse the existing `build/appicon.png` (1024×1024) as
  `build/icon.png` (or point `icon` at it). Final artwork is still deferred.
- `npmRebuild: false` because there are no native modules.
- Add `build/*.png` to `files`/gitignore handling as appropriate; do not let
  `buildResources` pull the whole legacy `build/` tree into the app (E6 deletes
  it).

### 2. Sandbox in the packaged AppImage (`electron/main.ts`)

The chromium sandbox helper (`chrome-sandbox`) must be root-owned setuid 4755;
an AppImage mount is read-only, and FUSE strips setuid. Mitigation (VSCode's
documented fallback):

- Default: keep the renderer sandbox (`webPreferences.sandbox: true`) and do not
  disable anything.
- If the environment cannot support the sandbox, append
  `--no-sandbox` + `--disable-gpu-sandbox`:
  - explicit `--no-sandbox` CLI flag, or
  - `APPIMAGE` set **and** unprivileged user namespaces unavailable
    (`/proc/sys/kernel/unprivileged_userns_clone` reads `0` or the file is
    absent).
- Escape hatch: `SHELVE_SANDBOX=1` forces the sandbox on; `SHELVE_SANDBOX=0`
  forces it off. Log the choice.
- README security note: the sandbox protects the renderer from itself; the
  renderer is our own bundled code with `contextIsolation` and a minimal
  preload. `--no-sandbox` does not change the vault/transport security model
  (§8) — secrets still never leave the vault, the loopback socket stays
  token-gated.

### 3. `Makefile` — final target set

| Target | Action |
|---|---|
| `dev-image` / `ensure-image` | new Electron `shelve-dev` image |
| `build` | Go backend + renderer + electron bundle + `electron-builder --linux dir` → `bin/linux-unpacked/` |
| `run` | `./bin/linux-unpacked/shelve`; if exec fails with a missing `.so`, print the README prerequisites line |
| `dev` | container hot-reload (Vite + Electron, X11); `make build && make run` is the always-supported loop |
| `appimage` | `npx electron-builder --linux AppImage` → `bin/shelve-<version>-x86_64.AppImage` |
| `appimage-check` | `scripts/verify-appimage.sh` (rewritten) |
| `package` | alias of `appimage` |
| `test` / `test-race` / `test-integration` / `smoke-vault` | unchanged Go commands |
| `lint` | gofmt + `go vet` + `npm run typecheck` |
| `seed` / `unseed` | unchanged |
| `clean` | `rm -rf bin frontend/dist dist-electron node_modules` |

**Removed targets:** `appimage-alt`, `appimage-alt-image`,
`ensure-appimage-alt-image`, `wails-init`, `deb`, `rpm`, `build-win`,
`build-darwin`, `flatpak`.
**Removed variables:** `APPIMAGE_ALT_IMAGE`, `PACKAGE_ARCH` if unused (keep if
electron-builder output naming needs it), the old AppImage `cp` alias line.

### 4. Delete the alt pipeline files

- `Dockerfile.appimage-alt` — delete.
- `shelve-dev-appimage-alt` references and its `-v shelve-dev-npm:/root/.npm`
  volume usage — delete from the Makefile.
- Any `build/linux/appimage/**` generated scratch — deleted with the Wails
  `build/` tree in E6 (see that plan; it is gitignored but present on disk).

### 5. `scripts/verify-appimage.sh` (rewrite)

- `bin/shelve-*.AppImage --appimage-extract` into a temp dir.
- Assert: `shelve` binary, `resources/app.asar`,
  `resources/backend/shelve-backend` (executable), `chrome-sandbox`,
  `shelve.desktop`, `.DirIcon`, and the Chromium `*.so` set.
- Assert the Go backend is **not** inside `app.asar`.
- Zero-dep sanity: inside a pristine `debian:13-slim` (no GTK/WebKit), run
  `--appimage-extract-and-run` under `xvfb-run` and assert the process reaches
  the "backend ready" log line without a missing-library error (best-effort;
  skip cleanly if the container cannot run xvfb).
- Assert no test credentials in the extracted payload
  (`rg` for the `docker/sshd` fixtures' strings).

### 6. README packaging + prerequisites

- Rewrite "Packaging (AppImage)": `make appimage` → artifact name; AppImage is
  self-contained (bundles Chromium); FUSE note +
  `--appimage-extract-and-run` fallback; `SHELVE_SANDBOX` and
  `--ozone-platform-hint` / `SHELVE_DISPLAY_BACKEND` knobs.
- Rewrite "Prerequisites" for `make run`: host needs Docker only to *build*;
  running the unpacked build needs the Electron/Chromium Linux runtime libs
  (list the Debian/ALT package names): `libnss3 libatk1.0-0
  libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1
  libxfixes3 libxrandr2 libgbm1 libpango-1.0-0 libcairo2 libasound2 libxss1
  libxtst6`. Remove the GTK4/WebKitGTK requirement text.
- Remove all references to `appimage-alt`, `Dockerfile.appimage-alt`, the ALT
  p11 portability rationale, and the WebKitGTK bundling discussion.

## Verification

- From a clean container: `make build` → `bin/linux-unpacked/shelve` exists;
  `make appimage` → `bin/shelve-<version>-x86_64.AppImage` exists;
  `make appimage-check` passes.
- Host: `make run` opens the app (needs the runtime libs); AppImage runs on a
  host without GTK4/WebKitGTK (e.g. pristine `debian:13-slim` + xvfb, or ALT).
- `rg -i "appimage-alt|Dockerfile.appimage-alt|linuxdeploy|webkitgtk|wails3" Makefile Dockerfile.dev scripts README.md` → no matches (except historical
  `plans/*.md`, deleted in E6).
- `rm Dockerfile.appimage-alt` is part of the diff; `git status` shows no
  untracked AppImage scratch left after `make clean`.
- Vault/config perms unchanged (§8.4): 0700 dir / 0600 files after an AppImage
  run; `known_hosts`/`settings.json` secret-free.

## Gate (this phase)

`make build`, `make test`, `make lint`, `make appimage` green from a clean
container; AppImage smoke run annotated in the commit message.

## Exit criteria

The two supported delivery paths work (`make build && make run`, `make
appimage`); the entire `appimage-alt` pipeline and the non-Linux build stubs are
gone; host prerequisites and packaging docs reflect Electron.
