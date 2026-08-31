# Phase 1 — Project Scaffold & Build System

**Type:** Backend + build. **Runs first; no prerequisites.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§1, §3, §5 (package layout), §6 (unlock screen placeholder only), §7** before starting.

## Goal
A Wails v3 project that boots a minimal window, built 100% inside Docker via a Makefile, with the final repo structure (packages may be empty stubs) in place.

## Tasks
1. Scaffold:
   - `go` toolchain 1.25 available only inside Docker (host has Docker only).
   - Create `go.mod` (module `shelve`) — do this by hand or generate inside the container; do NOT install Go on the host.
   - `wails3 init -n shelve -t vanilla` run **inside the container** (via `make wails-init` helper or a one-off `docker run`), then move `frontend/`, `build/`, `Taskfile.yml`, `main.go` into the repo and restructure:
     - create `internal/app internal/config internal/model internal/store internal/vault internal/sshx internal/sshengine internal/sftp internal/wailsvc` (each with a package doc stub + a trivial `doc_test.go` or empty file so `go build ./...` passes).
     - trim `main.go`: keep Wails bootstrap, remove template `Greetservice`; register a stub `AppService` with `GetVersion() string` (in `internal/wailsvc`, constructed by `internal/app`).
     - `frontend/`: keep Vite/TS setup; delete template JS/CSS/HTML bodies; replace with a minimal page: app title, version via binding call, a placeholder box that will become the unlock screen (Phase 4) and a note “scaffold ok”.
   - Pin the exact Wails v3 version used in `go.mod` (record it in `build/README-pin.md` is NOT needed — just commit the go.mod/go.sum).
2. `Dockerfile.dev` (per master §7): `golang:1.25-trixie` + gtk4/webkitgtk-6.0 dev packages + nodejs + npm + wails3 CLI pinned. Verify each package name exists on trixie at implementation time; adjust names, not the architecture.
3. `Makefile` with ALL targets from master §7 table (targets may `@echo "not implemented"` until their phase, except: `dev-image`, `wails-init`, `dev`, `build`, `run`, `clean`, `lint` (stub-level), `test` (trivial test)). Requirements:
   - every target idempotent; image built lazily by `make dev`/`make build` if missing.
   - `dev` forwards X11 + `--network host` + mounts `~/.config/shelve` (create dir first, 0700).
   - `run` launches `bin/shelve` on the host.
4. `internal/config`:
   - `Path()` → `$XDG_CONFIG_HOME/shelve` (fallback `~/.config`), `EnsureDir()` (0700), file helpers with 0600 creation.
   - `Settings` struct per master §4 + `Load()/Save()` (atomic tmp+rename; missing file → defaults).
5. `.gitignore` (bin/, frontend/dist/, frontend/bindings/, node_modules/, tmp/), `.dockerignore` (bin, node_modules, .git), minimal `README.md` (project title, status, build prerequisites: Docker only).
6. Placeholder icon 1024×1024 PNG at `build/appicon.png` (simple solid-color “D” mark; final art deferred — packaging phase cancelled 2026-08-29).
7. Placeholder `build/linux/nfpm/nfpm.yaml` + `build/linux/Taskfile.yml` vars per Wails packaging docs (name `shelve`, version 0.1.0).

## Verification
- `make dev-image` succeeds.
- `make build` produces `bin/shelve`; `make run` opens a window showing “scaffold ok” + version (X11).
- `make dev` hot-reloads: edit `frontend` → UI updates without rebuild; edit Go → app restarts.
- `make test` green (trivial), `go build ./...` clean inside container.
- No Go/npm/gtk packages were installed on the host (Docker is the only added tool).

## Exit criteria
Window runs on the developer desktop from a container-only toolchain; repo layout matches master §5; all Makefile targets exist and documented ones work.
