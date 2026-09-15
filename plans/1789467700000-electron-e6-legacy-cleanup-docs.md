# Phase E6 — Legacy removal, obsolete-plan deletion, docs (AGENTS.md + README)

**Type:** Cleanup + documentation. **Prereq:** E5. **Next:** E7.
**This phase owns the requested "remove unnecessary plans" and "remove all legacy
and unused files" tasks, and the AGENTS.md update.**

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read all; roadmap
[`1789467100000-electron-migration-roadmap.md`](1789467100000-electron-migration-roadmap.md)
**§1, §2, §6**.

## Goal

After this phase the tree contains **no Wails, WebKitGTK, `appimage-alt`,
non-Linux or otherwise dead artifact**, and the documentation (master plan,
AGENTS.md, README) describes only the Electron architecture.

**Ordering rule (critical):** the new master plan is the single source of truth.
**Port every durable rule out of an old plan into the master plan *before*
deleting that plan.** "Durable rule" = data-model/validation semantics, settings
fields and defaults, UI/UX behavior, event/DTO contract, security invariants,
build/test workflow. If a rule is not yet captured, the deletion of that plan is
blocked.

## 1. Delete Wails/WebKit/AppImage-alt legacy files

**Wails build template (tracked, 79 files):**
- `Taskfile.yml` (root, wails-generated)
- `build/Taskfile.yml`, `build/config.yml`
- `build/linux/Taskfile.yml`, `build/linux/desktop`,
  `build/linux/shelve.desktop`, `build/linux/nfpm/**` (nfpm.yaml + scripts),
  `build/linux/appimage/**` (`build.sh` + the gitignored extracted AppDir)
- `build/windows/**`, `build/darwin/**`, `build/ios/**`, `build/android/**`
- `build/docker/**` (Dockerfile.cross, Dockerfile.server)
- `build/appicon.icon/**` (wails icon bundle)
- `Dockerfile.appimage-alt` (already removed in E5 — assert it is gone)
- `.task/` (wails cache; gitignored, delete on disk)

**Kept from `build/`:** the 1024×1024 PNG, moved/renamed to `build/icon.png`
(the `electron-builder.yml` `buildResources`/`icon` target). Delete everything
else under `build/`.

**Wails frontend/backend artifacts:**
- `frontend/bindings/**` (generated, gitignored)
- `frontend/package.json`, `frontend/package-lock.json` (merged into root in E2)
  — assert gone; keep `frontend/.npmrc` by moving it to the repo root (it holds
  the `minimum-release-age` supply-chain policy; npm ignores it today, but it is
  forward-compatible with pnpm/bun).
- `main.go` (root Wails bootstrap — deleted in E1; assert gone)
- `internal/wailsvc/**` (renamed to `internal/api` in E1; assert gone)
- `go.mod`/`go.sum`: no `github.com/wailsapp/wails/v3` (E1); assert.

**Dead code inside kept packages:**
- `internal/termws`: remove the `/termws-port` endpoint (`handlePort` + its
  test) — the Electron handshake provisions the endpoint now, and the bridge
  routes only `/terminal`. Keep the framing/server/backpressure behavior
  untouched.
- Any remaining `WEBKIT_*`/dmabuf references in Go or frontend (E4 removed the
  frontend ones; the Go env var died with `main.go`).

**Obsolete plans (see §2).**

**Scratch directories (untracked, empty):** `cdp-xdg/`, `perf-xdg/`,
`tmp/m4probe/` — delete. `tmp/` itself remains a gitignored runtime dir.

**.gitignore / .dockerignore cleanup:**
- `.gitignore`: delete the Wails-specific lines
  (`build/linux/appimage/build`, `build/windows/nsis/...`,
  `build/ios/xcode/...`, `build/android/...`, `.task`,
  `**/**/**/.gradle`, `**/**/**/problems-report.html`) and the
  `frontend/bindings/` line; add `dist-electron/`; keep `bin/`,
  `frontend/dist/`, `node_modules/`, `tmp/`.
- `.dockerignore`: add `dist-electron`, `frontend/dist`, `bin` (already), and
  drop nothing else.

## 2. Delete/absorb the Wails-era plans

`plans/` currently holds 34 files; 28 are Wails-era. **All 28 are superseded**
by the new master plan + the E-roadmap, which aggregate their durable content.
After the port-before-delete rule above is satisfied, delete:

```
1787912690309-master-plan.md                      (old master plan)
1787912690309-phase-1-scaffold-build-system.md
1787912690309-phase-2-vault-session-store.md
1788003650840-phase-3a-args-parser.md
1788003650840-phase-3b-auth-hostkeys.md
1788003650840-phase-3c-engine-core.md
1788003650840-phase-3d-engine-forwards-testconn.md
1788003650840-phase-4a-shell-unlock.md
1788003650840-phase-4b-tree-search-editor.md
1788003650840-phase-4c-terminal-tabs.md
1788003650840-phase-4d-settings-lock-shortcuts.md
1788003650840-phase-5a-sftp-core-ops.md
1788003650840-phase-5b-sftp-transfers-editing.md
1788003650840-phase-5c-sftp-panel.md
1788003650840-phase-5d-sftp-panel-polish.md
1788265606865-plan-p003-terminal-mouse-clipboard.md
1788265606865-plan-p004-monitor-panel.md
1788277620000-plan-p004-linux-appimage.md
1788280000000-plan-p005-terminal-ws-transport.md
1788444206238-plan-p008-layout-independent-hotkeys.md
1788516816654-sftp-right-panel.md
1788531512181-sftp-remove-open-command.md
1788868849169-bastion-jump-mode.md
P001-multi-theme-support.md
P002-sftp-browser-enhancements.md
P003-credential-manager.md
P006-jump-host-manager.md
P007-performance-optimization.md
```

**Kept plans after E6:** the new master plan, the migration roadmap, and the
E1–E7 phase plans (E7 will have been executed). Rationale to record in the
master plan §10: the old plans were per-feature/implementation task lists for a
substrate that no longer exists; their durable rules live in the master plan.

**Alternative for the owner (one line, decide at implementation time):** move
the 28 files to `plans/archive/wails/` with a one-paragraph README instead of
deleting. The default in this plan is **delete**; the port-before-delete rule
makes deletion safe.

**Absorption checklist (prove the master plan already states each):**
- DTO/event names + payloads + producers → master plan §5.
- settings.json schema, defaults, presence-aware flags (`sftpBrowserEnabled`,
  `monitoringEnabled`) → §4.
- Model + validation incl. bastion rules, credential/jump-host references and
  soft-null semantics → §4.
- UI/UX: tree/search/editor/tabs/terminal/overlays/status→monitor bar/SFTP
  right panel/dialogs/toasts/confirm/unlock; layout grid with `leftWidth` and
  `sftpWidth` → §6.
- Shortcuts incl. physical-key rule, Ctrl+C, Shift+Backspace, Ctrl+Shift+V,
  Ctrl+Shift+E → §6.
- 12 theme variants + terminal palette binding → §6.
- Security invariants incl. the P009 kbdint prefill exception → §8.
- Terminal WS framing/backpressure, RPC/event transport → §5.
- Vault crypto/perms/atomic writes, tmp/ hygiene → §4, §8.
- Perf budgets, seed tool, integration tests → §9.

## 3. Rewrite `AGENTS.md`

Target: a Wails-free document that matches the new tree. Sections:

1. **Read these first** — new master plan + migration roadmap + phase plans.
   Remove the Wails/§ roadmap references.
2. **Project layout** — `electron/` (main.ts, preload.ts), `cmd/shelve-backend/`,
   `internal/api/` (services + DTOs), `internal/bridge/` (RPC/events/token),
   `internal/termws/`, `frontend/src/rpc/`, `electron-builder.yml`,
   `Dockerfile.dev`, `build/icon.png`.
3. **Build & development** — Docker-only dev; `make build`/`make run`/
   `make appimage`; Electron runtime libs for unpacked `make run`; no wails3.
4. **Tech stack & key rules** — Go ≥ 1.25 backend child process; Electron
   (pinned, matching VSCode's major); Chromium renderer; xterm.js; the new
   layering rule: **no package imports a GUI toolkit; all frontend↔backend
   traffic goes through `internal/bridge` (RPC/events) + the terminal WS; the
   renderer↔main surface is the reviewed preload API only.**
5. **Architecture** — process model, stdout handshake, token-gated loopback,
   `/rpc` + `/terminal`, event contract table (names unchanged), threading/
   failure modes (backend crash, GPU crash, display change).
6. **Security** — §8 invariants (unchanged), the P009 prefill exception, CSP,
   contextIsolation/sandbox, AppImage sandbox fallback, no secrets over RPC.
7. **Conventions** — HiDPI/zoom model, Linux display-backend knobs
   (`--ozone-platform-hint`, `SHELVE_DISPLAY_BACKEND`), GPU switches and the
   `disable-hardware-acceleration` off-switch, shortcuts (unchanged), the
   no-functional-change rule.
8. **Testing & QA** — Go unit/integration gates unchanged; Electron manual
   matrix (scale factors, X11/Wayland, GPU on/off, AppImage).
9. **Do's and don'ts** — replace Wails rules with Electron ones (never add Node
   to the renderer; never edit `dist-electron/`; never log secrets; no new
   non-loopback listeners; no cross-platform targets).

## 4. Rewrite `README.md`

- **Overview:** Electron + Go backend (remove "Go + Wails v3").
- **Prerequisites:** build = Docker only; run = Electron/Chromium runtime libs
  (list); AppImage = none.
- **Development workflow:** `make dev-image|build|run|test|lint|seed|unseed`;
  remove `wails-init`/`wails3 dev`; describe the backend process + RPC.
- **Development notes:** backend bridge, terminal WS, where secrets live.
- **Packaging (AppImage):** from E5.
- **Troubleshooting:** remove every WebKitGTK/mixed-DPI/dmabuf/GPU-blank-window
  entry; add: fractional scaling (Wayland default, X11 note), GPU on/off,
  `--gpu-info`, AppImage sandbox/`SHELVE_SANDBOX`, Wayland/X11 selection.
- **Pinned versions:** Electron version + Chromium/Node, Go, xterm.js.
- **Layout:** update to the new tree.
- Keep the SFTP browser, monitor bar, shortcuts, first-run, security, session
  card sections as-is (behavior unchanged), fixing only substrate wording.

## Verification

- `git ls-files | rg -i "wails|webkit|appimage-alt|nfpm|linuxdeploy"` → no
  matches; `ls build/` → only `icon.png` (+ `electron-builder` outputs, ignored).
- `rg -i "wails|@wailsio|WEBKIT_|_renderService|appimage-alt" --glob '!plans/**'
  .` → no matches.
- `ls plans/` → master plan + roadmap + E-plans only.
- `make build`, `make test`, `make lint` green after the deletions.
- `make appimage` still works (nothing in the deleted set was needed).
- AGENTS.md/README contain no Wails/WebKit references and match the tree.

## Gate (this phase)

The deletions + doc rewrites land with `make build`/`make test`/`make lint`
green; the rg sweeps above are empty; a fresh clone builds with no reference to
any deleted file.

## Exit criteria

No legacy/obsolete file remains; the old Wails-era plans are gone with their
durable rules captured in the master plan; AGENTS.md and README describe the
Electron architecture accurately.
