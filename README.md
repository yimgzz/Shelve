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
