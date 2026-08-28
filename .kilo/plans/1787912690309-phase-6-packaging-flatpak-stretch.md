# Phase 6 — Packaging, Distribution & Polish (Stretch: Flatpak)

**Type:** Build/DevOps + docs. **Prereq: Phase 5.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§1, §2 (D1), §6 (icon/app name), §7 (packaging notes), §11 (WebKitGTK/NVIDIA risks), §12 (global acceptance)** before starting.

## Goal
Shippable artifacts from a clean container: AppImage (primary), DEB, RPM — plus final icon/artwork, README/docs, release hygiene. Flatpak pipeline as a clearly-separated stretch section.

## Tasks
1. Final artwork: real `build/appicon.png` (1024²) + generated sizes (Wails tasks produce platform icons; at minimum: Linux `.png`, `.desktop` icon 512/256/128/64/48/32/24/16 in `build/linux/` layout per Wails packaging), app name “Dummy SSH Manager” in all metadata (`nfpm.yaml` name `dummy-ssh-manager`, description, license MIT — confirm license choice with owner; `Taskfile.yml` LINUX vars: APP_NAME, EXEC, ICON, CATEGORIES `Development;Network;`).
2. Packaging wiring & fixes:
   - `wails3 package GOOS=linux` in `dsm-dev` container; make `make package` produce ALL of `bin/dummy-ssh-manager-*.AppImage/.deb/.rpm`; single-format targets `make appimage|deb|rpm` via `wails3 task linux:create:*`.
   - Fix all packaging gaps empirically (linuxdeploy runtime deps inside the image; desktop entry Exec path; icon embedding).
   - AppImage: verify on a CLEAN Debian 13 container (no repo access) and on the developer host (X11) — zero local deps, starts < 2 s, window renders (NVIDIA blank-window check per §11: if reproduces, document `WEBKIT_DISABLE_DMABUF_RENDERER=1`).
   - DEB/RPM: `dpkg -i` in Debian 13 container, `rpm -i` in Fedora container — app appears in menu, runs.
   - ARM64 spot build in container (`make build-linux-arm64` → `wails3 build GOOS=linux GOARCH=arm64` via wails-cross/zig) — compile-only gate (no runtime test available), keep target documented.
3. Release hygiene:
   - `Makefile`: `make version VERSION=x.y.z` updates all metadata consistently (nfpm.yaml, Taskfile vars, `AppService.GetVersion` build-time ldflags `-X`).
   - `CHANGELOG.md` (Keep-a-Changelog), `LICENSE` (MIT placeholder → owner confirms), `CONTRIBUTING.md` (Docker-only workflow, plan files location).
   - `README.md` FULL: overview, features, install (AppImage drag-and-run / DEB / RPM / from source via Docker), first-run & master password, security model (vault format, known_hosts TOFU, what is NEVER stored), session card reference (incl. Extra Args supported grammar — auto-consistent with `internal/sshx/args` docs), shortcuts table, SFTP usage, development (make targets, X11 forwarding note for Wayland users, vault dir location), troubleshooting (NVIDIA, blank window, AppImage FUSE, corrupted vault guidance, host-key mismatch).
   - `.desktop` file sanity: Name, Icon, Categories, Terminal=false, NoShowInApp? no — default; `make run-desktop-entry-test` optional helper (skip if heavy).
4. **Stretch — Flatpak** (only after core artifacts pass; separate Makefile target `make flatpak` and its own image `Dockerfile.flatpak` so the main pipeline never depends on it):
   - `flatpak/dummy-ssh-manager.yaml` (flatpak manifest, non-SDK branch of org.freedesktop.Sdk with `gtk4` + `webkitgtk6.0` runtime — pick the exact Sdk version at implementation time and pin it; `build-options` cflags for the webkitgtk6 pkg-config) + `modules` for the app binary: build the Go binary inside the flatpak context via a custom build command (the Sdk has no Go module — either (a) add a `go` module (from a static Go toolchain tarball as a flatpak module) running `wails3 build`-equivalent `go build` + npm frontend build with node module, or (b) build the static binary outside and `copy` it in — choose (b) for v1 simplicity: frontend built by docker npm, Go compiled with cgo against the runtime's GTK/WebKit via a compile-time module; document whichever works; the binary must be built INSIDE the context against the exact runtime libs to avoid symbol drift).
   - `finish-args`: `--socket=network` (SSH outbound), `--share=ipc`, `--talk-name=org.freedesktop.impl.portal.FileChooser` (native file dialogs via xdg-desktop-portal), `--filesystem=xdg-config/dummy-ssh-manager` (XDG data access), `--device=dri` (webgl), `--allow=modules`? no. `--app-id=dev.dummysshmanager.Manager` (owner to confirm app-id).
   - Build in docker with `flatpak-builder --user` on a runtime with bubblewrap+flatpak; validate: app runs in flatpak portal env, can reach SSH (network socket granted), file dialogs work, data isolated under `~/.var/app/<app-id>/config/dummy-ssh-manager`.
   - Gate: flatpak ships only if the AppImage/DEB/RPM pipeline is green; otherwise it ships in the repo as an experimental target with a “known issues” section in README.
5. QA regression (re-run full master §12 list), plus:
   - cold-start timing measurement (AppImage on container + host, record in CHANGELOG release notes),
   - 300-session perf smoke on the PACKAGED binary (not just `make dev`),
   - final `rg` secret-leak audit over the repo (no hardcoded test credentials in shipped code paths; test fixtures under `internal/.../testdata` are fine),
   - `git tag v0.1.0`-ready state (owner pushes tags; we do not push).

## Verification
- `make package` from a clean `dsm-dev` container (fresh image rebuild) → all three artifacts.
- AppImage on clean Debian 13 container + host: runs, connects to a real SSH host (network test), SFTP round-trip, lock/unlock.
- DEB/RPM install+run in their respective distro containers.
- `make build-linux-arm64` compiles.
- Stretch: flatpak app installed (flatpak-builder user deploy) and the three functional checks above pass — or documented blocked status.
- README accuracy pass: every command in README executed verbatim in a clean environment (fresh clone + Docker only).

## Exit criteria
Master §12 acceptance criteria 1–6 all met; artifacts + metadata consistent (one version string everywhere); docs complete; stretch either shipped or explicitly marked experimental/blocked with a follow-up ticket in README.
