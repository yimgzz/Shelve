# Shelve

Stash your shells in Shelve!

A lightweight, fast, fully local SSH session manager. Go backend
(`golang.org/x/crypto/ssh`) + Electron (Chromium) shell with a vanilla
TypeScript + Vite renderer. Sessions live in an encrypted vault (Argon2id +
AES-256-GCM) under `$XDG_CONFIG_HOME/shelve`; no cloud, no telemetry, no
accounts.

**Status:** v2.0 — **Electron architecture**. The Go backend runs as a child
process of Electron main; the renderer is vanilla TypeScript + Vite + xterm.js
talking to the backend over a token-gated loopback RPC/terminal WebSocket. The
app is a working SSH session manager: encrypted vault, session tree + live
search, terminal tabs, an SFTP browser in a right-hand panel that is **enabled
by default** (browse / upload / download / mkdir / rename / delete /
edit-text-with-system-editor), and a MobaXterm-style **system monitor bar**
under the terminal (hostname / CPU / RAM / network / uptime / disk — enabled
by default). Roadmap: `plans/` (Electron master plan + migration roadmap + E1–E7
phase plans).

## Prerequisites

- **Docker** — the only host tool required for development & builds.
  No Go or Node packages are needed on the host for `make dev` / `make build` /
  `make appimage` / `make test` / `make lint`.
- To run the unpacked build directly on the host (`make run`): the
  **Electron/Chromium runtime libraries**. On a stock desktop most are present
  already; on a minimal install add (Debian/Ubuntu/ALT package names):
  `libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 libgtk-3-0
  libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3
  libxrandr2 libgbm1 libpango-1.0-0 libcairo2 libasound2 libxss1 libxtst6
  libx11-xcb1 libxext6 libxrender1 libxi6 libxcursor1 libxshmfence1 fontconfig
  libsecret-1-0`. No system webview runtime is needed — Electron bundles its
  own Chromium. The authoritative list is `scripts/host-runtime-libs.txt` (shared
  with the `make run` hint and the AppImage smoke test).
- The **AppImage bundles Chromium and its own runtime libraries**, so it needs
  no distro packages beyond the stock desktop libraries listed above (see
  [Packaging](#packaging-appimage)).

## Development workflow

| Command | Action |
|---|---|
| `make dev-image` | Build the `shelve-dev` toolchain image (lazy: `dev`/`build` do it automatically) |
| `make dev` | Container hot-reload (Vite + Electron over X11); the window opens on your desktop |
| `make build` | Release build in the container → `bin/linux-unpacked/shelve` (+ `bin/shelve-backend`) |
| `make run` | Run the unpacked build on the host (needs the Electron runtime libs) |
| `make test` | Go unit tests in the container |
| `make test-race` | Go unit tests with the race detector |
| `make test-integration` | SFTP/SSH integration tests via testcontainers (needs the Docker socket; builds `docker/sshd`) |
| `make lint` | `gofmt` + `go vet` (container) + renderer & Electron `tsc --noEmit` |
| `make appimage` | Build a self-contained AppImage in the container → `bin/shelve-<version>-x86_64.AppImage` |
| `make appimage-check` | Verify the AppImage payload + self-containment (`scripts/verify-appimage.sh`) |
| `make package` | Alias of `make appimage` |
| `make clean` | Remove `bin/`, `frontend/dist/`, `dist-electron/`, `node_modules/` |

`make dev` details:

- X11 forwarding: mounts `/tmp/.X11-unix`, passes `DISPLAY` (and
  `XAUTHORITY` if `~/.Xauthority` exists). Works on X11 and on Wayland via
  XWayland — no native Wayland socket forwarding in v1.
- `--network host` so the app can reach real SSH hosts and the Vite dev server.
- `~/.config/shelve` (created 0700) is mounted into the
  container so the real vault/settings persist across runs.
- Hot reload: frontend edits → Vite reloads the UI without a rebuild;
  Electron main/preload edits → the bundle is rebuilt and Electron restarts.

If the window fails to open under `make dev`:

1. Temporarily allow local X clients and retry: `xhost +local:`
   (revoke with `xhost -local:` afterwards).
2. On some desktops the in-container window still cannot start (display/DBus
   session restrictions inside the container, sandbox/namespace limits).
   In that case use the host-run workflow — it is fully supported and is
   the reliable path:

   ```
   make build   # compile in the container -> bin/linux-unpacked/shelve
   make run     # launch it on the host (window on your desktop)
   ```

## Development notes

- **Seed tool** (`cmd/seed`): generates a disposable vault with folders and
  sessions (e.g. a 300-session fixture for search/performance smoke tests).
  Build and run it inside the container, pointing it at a scratch config dir;
  it never touches your real vault unless you tell it to.
- **Process model:** the Electron main process spawns the Go backend
  (`bin/shelve-backend`) as a child process. The backend prints a one-line
  JSON handshake (`{"event":"ready","addr":"127.0.0.1:PORT","token":"…"}`) on
  stdout; main hands that loopback endpoint + per-run token to the sandboxed
  renderer. Application data crosses the loopback RPC/terminal WebSocket —
  never stdio, never the renderer's Node.
- **In-container window limitation vs `make run`:** `make dev` runs the app
  *inside* the container. On some desktops (observed: GNOME/XWayland on ALT
  Linux) the in-container window cannot start due to display/DBus session
  restrictions and sandbox/namespace limits inside the container. In that case
  the always-supported workflow is `make build` + `make run` (build in the
  container, run the app on the host). The in-container hot-reload loop is then
  not available — but UI phases are QA'd via the host-run build.
- **Settings** (gear menu → Settings): theme, auto-lock minutes, SFTP browser
  toggle, system-monitor toggle, terminal font/size/scrollback, and the
  text-editor command are saved to `settings.json`. Theme and terminal
  options apply live.
- **SFTP temp files & edit logs:** the app keeps per-edit temp copies and
  editor logs under the config-directory `tmp/` (i.e.
  `$XDG_CONFIG_HOME/shelve/tmp/`). `tmp/edit-*.log` captures the
  text editor's stdout/stderr — handy when a configured editor misbehaves.
  Temp files are 0600 and are swept on vault lock, app exit, and at startup
  (stale-sweep); a clean exit leaves `tmp/` empty.
- **Text editor command (SFTP "Edit as text"):** set in Settings → Files →
  "Text editor command". It is run verbatim (`strings.Fields`: a bare
  executable plus space-separated arguments) with the temporary file path
  appended **last** — e.g. `nano` or `xdg-open` work as-is; for an editor with
  flags use something like `code --wait`. The app re-uploads automatically
  once the edited copy has been stable for 3 s.
- **SFTP scope (v1):** the browser lives in a right-hand panel beside the
  terminal while the active session is ready — browse, upload, download, mkdir,
  rename, delete, and "edit text file with system editor". Drag & drop uploads
  are **out of scope for v1** (D4). The browser is on by default; the `×` in the
  panel header closes it and the left toolbar's **[SFTP]** button reopens it.

## Packaging (AppImage)

`make appimage` produces a self-contained AppImage at
`bin/shelve-<version>-x86_64.AppImage` (e.g. `bin/shelve-0.1.0-x86_64.AppImage`).
Everything runs inside the `shelve-dev` container — the host needs Docker only.
The packaging metadata lives in `electron-builder.yml`; artifacts land in
`bin/` (gitignored).

What is bundled (via `electron-builder --linux AppImage`):

- the Electron runtime with its bundled Chromium: the `shelve` executable, the
  Chromium `*.so` set (`libEGL`, `libGLESv2`, `libffmpeg`, `libvk_swiftshader`,
  `libvulkan`), locales, ICU and the V8 snapshot;
- the app payload in `resources/app.asar` (main/preload + the Vite renderer)
  and the Go backend at `resources/backend/shelve-backend`, **outside** the
  asar;
- the desktop entry (`shelve.desktop`) and `.DirIcon`.

Deliberately *not* bundled (provided by the host desktop): glibc/libstdc++, the
GTK3/X11/Wayland client libraries, the font stack, and the GPU drivers
(GL/EGL/drm/gbm — these must stay host-provided). The AppImage needs **no**
GTK4 or other system webview packages; Electron ships its own Chromium and links
only the stock desktop libraries listed in [Prerequisites](#prerequisites).

```sh
make appimage             # -> bin/shelve-<version>-x86_64.AppImage
make appimage-check       # headless payload + self-containment verification
./bin/shelve-0.1.0-x86_64.AppImage
```

- The AppImage uses FUSE to mount itself. Without FUSE (containers, locked-down
  hosts) use
  `./bin/shelve-0.1.0-x86_64.AppImage --appimage-extract-and-run`.
- **Target platform:** built on Debian 13 (trixie); the floor is glibc ≥ 2.39
  plus the Electron runtime libraries of any recent desktop. Older hosts should
  keep using `make build` + `make run`.
- **Renderer sandbox:** the renderer runs with `contextIsolation`, no Node
  access, and a minimal reviewed preload. The Chromium process sandbox needs a
  root-owned setuid `chrome-sandbox`, which a read-only AppImage mount cannot
  provide; when the environment cannot support it (an AppImage on a host
  without unprivileged user namespaces, or an explicit `--no-sandbox`) the app
  appends `--no-sandbox --disable-gpu-sandbox` and logs the choice. This does
  not change the security model: the vault stays encrypted, secrets never leave
  it, and the loopback RPC/terminal socket stays token-gated. Override with
  `SHELVE_SANDBOX=1` (force on) or `SHELVE_SANDBOX=0` (force off).
- DEB/RPM/Flatpak and Windows/macOS targets are out of scope (Linux-only
  delivery; see master plan §7).

## SFTP browser

The SFTP browser is **enabled by default**. With a connected (ready) tab
active, it appears in a **right-hand panel** beside the terminal, rooted at the
remote `$HOME`. Disable or re-enable it with **Ctrl+Shift+E** or Settings →
General → "SFTP browser".

- **Path bar:** the header shows the current remote directory in an editable
  field. Press **Enter** to navigate: type an absolute path (`/etc`), a
  `~`-relative one (`~/src`), or just a folder name to descend from the
  current directory; `Esc` (or clicking away without Enter) reverts the
  field. The `←` button goes up one level.
- **Buttons & rows:** [Upload] / [New folder] / [Refresh]; double-click a
  folder to open it, or a file to edit it as text in your system editor (the
  backend refuses files >2 MiB or with binary content — use *Download…* for
  those); right-click rows for Edit as text / Download / Upload to here / New
  folder / Rename / Delete. The footer shows live transfer progress.
- **Panel & session tree:** the session tree always stays in the left panel. The
  `×` in the SFTP header closes the right panel; the toolbar's **[SFTP]** button
  reopens it (visible when the setting is on, a ready tab exists, and the panel
  is closed). Connecting a session, or activating a ready tab, auto-shows the
  browser. When it is enabled but no session is ready, the tree shows with a
  hint line.

## System monitor bar

The bottom bar under the terminal shows a live snapshot of the **active**
connection, MobaXterm-style, left to right: remote **hostname**, **CPU** load
(percent gauge), **RAM** (used / total, units chosen automatically between
MB/GB/TB), **upload** and **download** speeds (bytes/s, summed across all
non-loopback interfaces), **uptime**, and **disk** usage of the main partition
(`/`). Hover over the disk item to see the full `df -h` listing.

- **Enabled by default.** Toggle it in Settings → General → "System
  monitoring". When disabled the bar is empty and no metrics are collected.
- **How it works:** every 2 seconds the app runs a small set of **read-only**
  commands on the remote host over the active SSH connection
  (`cat`/`awk`/`df` on `/proc/stat`, `/proc/meminfo`, `/proc/net/dev`,
  `/proc/uptime`, `df`). CPU% and network speeds are computed as deltas
  between consecutive samples. **Linux targets only** — on other hosts the
  bar stays empty.
- **Privileges:** the commands run as your SSH user (the same access the
  terminal already has). No new network connections are made; nothing is
  installed or changed on the remote host.

## Shortcuts

| Keys | Action |
|---|---|
| Ctrl+K / Ctrl+L | Focus search |
| Ctrl+T | Connect the selected session (or open a new-session draft) |
| Ctrl+W | Close the active tab |
| Ctrl+Tab / Ctrl+Shift+Tab | Cycle tabs |
| Ctrl+1…9 | Activate nth tab |
| Ctrl+, | Open Settings |
| F2 / Delete | Rename / delete the selected tree node |
| Esc | Close the topmost modal / clear search |
| Ctrl+Shift+V | Paste the system clipboard into the active terminal (bracketed-paste safe) |
| Ctrl+Shift+C | Copy the terminal selection to the system clipboard (no-op when empty) |
| Ctrl+Shift+E | Toggle SFTP browser (mirrors the Settings checkbox) |

Shortcuts are suppressed while you are typing in a form field (except Esc,
which the dialogs/search handle themselves).

All shortcuts and terminal control keys are keyed to the **physical key
position** (US layout) via `KeyboardEvent.code`, so they fire identically
under any active keyboard layout; plain text input remains layout-aware.

## First run & master password

On the very first launch the app shows the **"Create master password"** screen:
enter a password of at least **8 characters** plus a confirmation. This creates
your encrypted vault (`vault.json` in the config directory). The master password
is **never written to disk** — only a per-file random salt and KDF parameters
are stored, and the derived key lives in memory for the app session.

On every subsequent start you are prompted to **unlock** with the master
password. Wrong passwords show an inline error; the app will not proceed without
it. You can **Lock** explicitly from the gear menu, and opt in to **auto-lock**
after N minutes of inactivity (Settings → General; default `0` = off). A locked
app is unusable without the password — your sessions are safe even if the
config directory is copied off the machine.

The session tree is kept in the encrypted vault. There is no recovery
mechanism for a lost master password: keep a safe backup of your
`vault.json`.

## Backup & migrate

**Gear menu → Export configuration…** writes everything — the session tree,
credentials, saved jump hosts and settings — to a single encrypted `.shelve`
file, protected by a passphrase you choose (independent of the master password).
Use it to move to another machine or to keep a backup. The native save dialog
defaults to `shelve-config-YYYY-MM-DD.shelve`; the file is written `0600`.

**Gear menu → Import configuration…** reads such a file back:

- **Merge** (default) assigns fresh IDs and drops the imported data under one new
  folder named `Imported <date>`. Your existing sessions, credentials, jump hosts
  and settings are untouched, and live terminals stay connected.
- **Replace** overwrites the entire tree and settings with the file's contents.
  Live connections are closed first, and the imported theme/zoom are re-applied
  immediately.

Both modes require the export passphrase; a wrong passphrase changes nothing.
`known_hosts` is intentionally **not** part of an export — host-key trust is
per-machine and is re-established on first connect. Keep both the `.shelve` file
and its passphrase safe: there is no recovery.

## Security model

- **Encryption:** AES-256-GCM (random 12-byte nonce per write) with a 32-byte
  key derived from your master password via **Argon2id** (memory 64 MiB,
  iterations 3, threads 4, 16-byte random salt). Each vault write is atomic
  (temp file + rename) and uses the AAD tag `dsmsv1`.
- **No plaintext credentials on disk:** `vault.json` is the only store of
  secrets; `settings.json` and `known_hosts` are guaranteed by tests to contain
  none. Credentials never cross the renderer transport (the loopback RPC and
  terminal WebSocket) — the SFTP panel passes only file *paths*, and Go streams
  the bytes (large files never cross the transport).
- **Memory hygiene:** the master password and derived key are zeroized after
  use; keys are never logged and errors never include password/key material.
- **Host keys:** app-managed `known_hosts` with TOFU — you must approve a new
  host key; a mismatch is a hard fail (never auto-accepted).
- **SSH key files:** read-only use; the app never writes or rewrites your key
  files. Key passphrases are prompted once per connection and cached only in
  process memory.
- **File/dir permissions:** the config directory is `0700`, all files `0600`.
- **Temp files:** SFTP edit temp copies are `0600` under `tmp/` and are swept
  on lock, exit, and at startup (stale sweep).
- **Network:** the app makes no network calls except to the SSH hosts you
  configure and loopback listeners on `127.0.0.1` (the backend RPC/terminal
  WebSocket, bound to an ephemeral port and gated by a per-run token, plus
  local port-forward sockets). No telemetry, no update checks, no crash
  reporting.
- **Corrupt vault:** the app refuses to unlock a vault it cannot decrypt and
  never auto-overwrites it — keep backups of `vault.json`.
- **Configuration export:** an exported `.shelve` file is encrypted with its own
  passphrase using the same Argon2id + AES-256-GCM scheme but a distinct AAD tag
  (`dsmexp1`), so an export can never be opened as a vault or vice versa. The
  passphrase is never written to disk, and import/export pass only file *paths*
  over the loopback transport — no file bytes.

## Session card & Extra Args

Create or edit a session with these fields:

| Field | Notes |
|---|---|
| Name | required, non-empty |
| Host | required; hostname, IPv4 or IPv6 |
| Port | default `22`, range 1–65535 |
| User | required, non-empty |
| Auth | exactly one of **Password** or **SSH key path** (a key passphrase is asked on connect, never stored) |
| Jump Hosts | optional ordered list; each: host, port, user, auth (structured jump chains) |
| Extra Args | optional strict grammar below; validated inline on save |

**Extra Args grammar** (whitespace-separated; single/double quotes strip quotes
but do not join tokens):

```
-L [bind:]localPort:dstHost:dstPort     # local port forward (default bind 127.0.0.1)
-D [bind:]localPort                     # dynamic SOCKS5 forward
-o ServerAliveInterval=<int>
-o ServerAliveCountMax=<int>
-o ConnectTimeout=<int>
-o StrictHostKeyChecking=no|ask
ProxyJump=[user@]host[:port]            # at most one; port 0 = 22
```

Rules: forwards bind to `127.0.0.1` by default (loops back only, §8.9);
explicit ports must be 1–65535; duplicate `-o` keys — last wins; IPv6 is a
documented v1 limitation and is rejected; unknown flags or bare words are
validation errors shown in the editor. For multiple jump hosts use the
structured Jump Hosts list instead of several `ProxyJump=` tokens.

## Troubleshooting

- **Display backend, HiDPI and GPU** (Electron/Chromium). Hardware
  acceleration is on by default and `--disable-gpu` (or
  `--disable-hardware-acceleration`) is the only way to force software
  rendering; `--gpu-info` prints the GPU feature status and exits. On a
  Wayland session the app defaults to Chromium's native Wayland backend
  (`--ozone-platform-hint=auto`) so per-monitor fractional scaling works at
  125 / 150 / 175 %. On X11 Chromium exposes a **single global scale** for the
  whole desktop (from `Xft.dpi` / GTK XSettings) — moving the window to a
  monitor with a different scale factor cannot re-scale it, which is a
  Chromium/X11 limitation rather than an app bug; terminals still refit and
  never freeze. Overrides, in priority order:
  1. `--ozone-platform=x11|wayland` on the command line (passed through);
  2. `ELECTRON_OZONE_PLATFORM_HINT=x11|wayland|auto` (Electron-native; `auto`
     asks Chromium to pick, it does not force a switch);
  3. `SHELVE_DISPLAY_BACKEND=x11|wayland|auto` (app-specific), e.g.
     `SHELVE_DISPLAY_BACKEND=x11 make run`;
  4. `--force-device-scale-factor=<n>` to force one global Chromium scale.
  The chosen backend and the effective platform are logged to stderr at
  startup.
- **Blank / hung window on some desktops** (NVIDIA/gbm, Wayland sessions):
  force the X11 backend, either with
  `SHELVE_DISPLAY_BACKEND=x11 make run` or directly with
  `--ozone-platform=x11`.
- **In-container window fails under `make dev`** (display/DBus session limits
  inside the container): use the always-supported workflow `make build` +
  `make run` (compile in the container, run the binary on the host). See the
  Development notes above.
- **X11 vs Wayland:** X11 forwarding works on X11 and on Wayland via XWayland;
  native Wayland socket forwarding is out of scope for the containerized dev
  session.
- **SFTP "Edit as text" misbehaves:** check `$XDG_CONFIG_HOME/shelve/tmp/edit-*.log`
  for the configured editor's stdout/stderr. The command is run verbatim
  (`strings.Fields`) with the temp file path appended last — use something like
  `nano`, `xdg-open`, or `code --wait`.
- **Lost master password:** there is no recovery; restore `vault.json` from a
  backup, or start fresh by removing the config directory.
- **Vault won't unlock:** the app refuses to open a vault it cannot decrypt
  rather than overwrite it — restore from a backup or re-create.

## Pinned versions

- **Electron 42.10.0** (exact pin in `package.json`; Chromium 140 / Node 24) —
  the same major VSCode pins, so the Chromium rendering/DPI behavior matches.
- Go **1.25**; base image `golang:1.25-trixie` (Debian 13) with Node 24 LTS.
- Renderer: TypeScript 5 + Vite 8, `@xterm/xterm` 5.5 with the fit / webgl /
  web-links / search addons. Root `package.json` is the only JS manifest.
- Backend: module `shelve`, `golang.org/x/crypto/ssh`, `github.com/pkg/sftp`,
  `github.com/oklog/ulid/v2`.

## Layout

- `electron/` — `main.ts` (main process: backend lifecycle, window, native
  IPC, GPU/DPI) and `preload.ts` (the minimal reviewed renderer bridge).
- `cmd/shelve-backend/` — the standalone Go backend; `cmd/seed/` — the QA
  vault fixture generator.
- `internal/` — `app` (composition root), `config` (XDG paths, settings.json),
  `model`, `store`, `vault`, `sshx`, `sshengine`, `sftp`, `monitor`
  (domain layer), plus `api` (services/DTOs), `bridge` (loopback RPC/events/
  token) and `termws` (terminal WebSocket).
- `frontend/` — `src/rpc` (backend client) + the UI; Vite output in
  `frontend/dist/` (gitignored). `dist-electron/` (gitignored) holds the
  esbuild main/preload bundles.
- `electron-builder.yml`, `Dockerfile.dev`, `build/icon.png`, `scripts/`.
