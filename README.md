# Dummy SSH Manager

A lightweight, fast, fully local SSH session manager. Go backend
(`golang.org/x/crypto/ssh`) + Wails v3 frontend (vanilla TypeScript + Vite).
Sessions live in an encrypted vault (Argon2id + AES-256-GCM) under
`$XDG_CONFIG_HOME/dummy-ssh-manager`; no cloud, no telemetry, no accounts.

**Status:** Phase 1 — project scaffold & build system. The app boots a
minimal "scaffold ok" window. Roadmap: `.kilo/plans/` (master plan + phases 2-6).

## Prerequisites

- **Docker** — the only host tool required for development & builds.
  No Go, Node, GTK or WebKit packages are needed on the host for
  `make dev` / `make build` / `make test` / `make lint`.
- To run the built binary directly on the host (`make run`): the host
  distro's GTK4 + WebKitGTK 6.0 **runtime** libraries
  (e.g. `gtk-4`, `webkitgtk-6.0` on ALT).

## Development workflow

| Command | Action |
|---|---|
| `make dev-image` | Build the `dsm-dev` toolchain image (lazy: `dev`/`build` do it automatically) |
| `make dev` | `wails3 dev` in the container — hot reload; the window opens on your desktop |
| `make build` | Release build in the container → `bin/dummy-ssh-manager` |
| `make run` | Run the built binary on the host (X11) |
| `make test` | Go unit tests in the container |
| `make test-race` | Go unit tests with the race detector |
| `make test-integration` | SFTP/SSH integration tests via testcontainers (needs the Docker socket; builds `docker/sshd`) |
| `make lint` | `gofmt` + `go vet` (container) + frontend `tsc --noEmit` |
| `make clean` | Remove `bin/`, `frontend/dist/`, `frontend/bindings/` |
| `make wails-init` | Re-merge the pinned Wails template (recreates frontend/build scaffolding) |

`make dev` details:

- X11 forwarding: mounts `/tmp/.X11-unix`, passes `DISPLAY` (and
  `XAUTHORITY` if `~/.Xauthority` exists). Works on X11 and on Wayland via
  XWayland — no native Wayland socket forwarding in v1.
- `--network host` so the app can reach real SSH hosts.
- `~/.config/dummy-ssh-manager` (created 0700) is mounted into the
  container so the real vault/settings persist across runs.
- Hot reload: frontend edits → Vite reloads the UI without a rebuild;
  Go edits → the app is rebuilt and restarted.

If the window fails to open under `make dev`:

1. Temporarily allow local X clients and retry: `xhost +local:`
   (revoke with `xhost -local:` afterwards).
2. On some desktops the in-container window still cannot start (GTK/DBus
   session restrictions inside the container, sandbox/namespace limits).
   In that case use the host-run workflow — it is fully supported and is
   the reliable path:

   ```
   make build   # compile in the container -> bin/dummy-ssh-manager
   make run     # launch the binary on the host (window on your desktop)
   ```

## Development notes

- **Seed tool** (`cmd/seed`): generates a disposable vault with folders and
  sessions (e.g. a 300-session fixture for search/performance smoke tests).
  Build and run it inside the container, pointing it at a scratch config dir;
  it never touches your real vault unless you tell it to.
- **In-container window limitation vs `make run`:** `make dev` runs the app
  *inside* the container. On some desktops (observed: GNOME/XWayland on ALT
  Linux) the in-container window cannot start due to GTK/DBus session
  restrictions inside the container plus WebKit sandbox namespace limits. In
  that case the always-supported workflow is `make build` + `make run` (build
  in the container, run the binary on the host). The in-container hot-reload
  loop is then not available — but UI phases are QA'd via the host-run binary.
- **Settings** (gear menu → Settings): theme, auto-lock minutes, SFTP browser
  toggle, terminal font/size/scrollback, and the text-editor command are saved
  to `settings.json`. Theme and terminal options apply live.
- **SFTP temp files & edit logs:** the app keeps per-edit temp copies and
  editor logs under the config-directory `tmp/` (i.e.
  `$XDG_CONFIG_HOME/dummy-ssh-manager/tmp/`). `tmp/edit-*.log` captures the
  text editor's stdout/stderr — handy when a configured editor misbehaves.
  Temp files are 0600 and are swept on vault lock, app exit, and at startup
  (stale-sweep); a clean exit leaves `tmp/` empty.
- **Text editor command (SFTP "Edit as text"):** set in Settings → Files →
  "Text editor command". It is run verbatim (`strings.Fields`: a bare
  executable plus space-separated arguments) with the temporary file path
  appended **last** — e.g. `nano` or `xdg-open` work as-is; for an editor with
  flags use something like `code --wait`. The app re-uploads automatically
  once the edited copy has been stable for 3 s.
- **SFTP scope (v1):** the left panel replaces the session tree while the
  active session is ready — browse, upload, download, mkdir, rename, delete,
  and "edit text file with system editor". Drag & drop uploads are **out of
  scope for v1** (D4).

## SFTP browser

Enable it with **Ctrl+Shift+E** or Settings → General → "SFTP browser". With a
connected (ready) tab active, the left panel becomes an SFTP browser rooted at
the remote `$HOME`: breadcrumb navigation, [Upload] / [New folder] / [Refresh],
double-click to open a folder, edit a text-like file in your system editor, or
download others; right-click rows for Edit / Download / Upload to here / New
folder / Rename / Delete. The footer shows live transfer progress. When the
browser is enabled but no session is ready, the session tree shows with a hint
line.

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
| Ctrl+Shift+E | Toggle SFTP browser (mirrors the Settings checkbox) |

Shortcuts are suppressed while you are typing in a form field (except Esc,
which the dialogs/search handle themselves).

## Pinned versions

- Wails v3 CLI: **v3.0.0-beta.15** — `Dockerfile.dev` (`WAILS3_VERSION`)
  and `go.mod` (module `dummy-ssh-manager`, `github.com/wailsapp/wails/v3`).
- Base image: `golang:1.25-trixie` (Debian 13, GTK 4.18, WebKitGTK 6.0/2.52,
  Node 20).

## Layout

`main.go` + `internal/` packages per the master plan (§5): `app` (composition
root), `config` (XDG paths, settings.json), `model`, `store`, `vault`,
`sshx`, `sshengine`, `sftp`, `wailsvc` (the only Go code besides `main.go`
touching the Wails API). Frontend under `frontend/` (bindings generated into
`frontend/bindings/`, gitignored).
