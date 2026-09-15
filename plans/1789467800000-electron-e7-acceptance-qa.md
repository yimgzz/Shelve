# Phase E7 — Acceptance: functional parity, HiDPI/GPU matrix, packaging

**Type:** QA / gate. **Prereq:** E6. **This closes the migration.**

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read **§6, §8, §9, §12**; roadmap
[`1789467100000-electron-migration-roadmap.md`](1789467100000-electron-migration-roadmap.md)
**§1, §6, §8**.

## Goal

Prove that the Electron app is **functionally identical** to the shipped Wails
v1.1 build, meets the HiDPI/GPU/performance requirements, and is deliverable as
`make build && make run` and `make appimage`. Every item is executed and its
result annotated in the commit message (project convention).

## Deliverables

1. Annotated QA checklist (below) with pass/fail per item.
2. `make test`, `make test-race`, `make test-integration`, `make lint`,
   `make build`, `make appimage` all green from a clean container.
3. A parity statement listing any item that could **not** be reproduced, with a
   root cause and a follow-up plan (expected: none).

## A. Functional parity (host-run `make build && make run`; real sshd)

Use a fresh `$XDG_CONFIG_HOME/shelve` unless stated. Check every row.

**Vault & lifecycle**
1. First run: create master password (min length, confirm mismatch), unlock.
2. Wrong password → inline error + shake; correct → shell.
3. Auto-lock after N minutes (setting) locks without confirm; activity resets.
4. Lock with active ready tabs → confirm → unlock gate → terminals destroyed.
5. Lock/unlock ×20: no listener/timer growth, no console errors, no duplicate
   toasts.
6. Corrupt `vault.json` → refuses to unlock, guidance shown, file not
   overwritten.
7. Perms: config dir 0700, `settings.json`/`known_hosts` 0600; neither contains
   secrets.
8. `make seed` → 300-node tree → `make unseed`.

**Tree / search / editor**
9. Create/rename/duplicate/move/delete folders+sessions; delete shows affected
   count; moving into a descendant is impossible; F2/Delete keys work.
10. Search matches Name/Host/User, shows folder path, highlights, Esc clears;
    measured < 10 ms over the seed; no-results text correct.
11. Session editor: password, key (+ `[Browse…]` full path via Electron dialog),
    jump hosts, **bastion** (checkbox, only-one/last/port-22/no-ProxyJump
    validation), Extra Args inline validation (`-L`, `-D`, `-o`, `ProxyJump`),
    password-merge on edit, `[Test connection]` (success + host-key prompt +
    wrong password).
12. Named credentials and saved jump hosts: create/edit/delete (affected-session
    confirm), reference from a session, soft-null on delete, inline snapshot
    still connects.

**Terminal & tabs**
13. Double-click connects; tab dot amber→green; error → red with overlay +
    [Retry]/[Close].
14. Resize window/splitter → remote pty resizes (`stty size`).
15. Ctrl+C interrupts (even with a selection); Shift+Backspace word-deletes;
    Ctrl+Shift+V pastes; right-click pastes; select-to-copy on release;
    bracketed paste safe; vim/htop mouse-mode unaffected.
16. 3 tabs: switch preserves per-tab scrollback; middle-click/Ctrl+W close;
    Ctrl+Tab cycle; Ctrl+1…9 activate; focus follows the active tab.
17. Remote `exit` → exit overlay with code; Retry re-dials.
18. Flood `yes | head -c 1000000` and a 3 MB/s `tail -f`: responsive, no crash,
    no stalled presentation.
19. All 18 theme variants: terminal palette + cursor/selection follow the
    variant; system mode follows the OS live.

**Prompts / forwards / SFTP / monitor**
20. Host-key accept persists to `known_hosts`; reject fails with a clear message;
    reconnect does not re-prompt when cached.
21. Key passphrase prompt (correct/wrong/timeout).
22. Bastion keyboard-interactive: 2 rounds, stored-password prefill in masked
    inputs, submit, cancel, timeout, partial-failure error attribution.
23. `-L` dial-through and `-D` SOCKS5; failed bind → toast + connection still
    ready; forward summary/`ssh:forward` states correct.
24. SFTP: right panel auto-opens beside the terminal (tree always visible);
    editable path bar (navigate/Esc/blur); list sorting; mkdir/rename/delete
    (non-empty dir blocked); upload (progress), download, `EditRemoteText`
    auto-save + cancel; > 2 MiB / binary → correct errors; `tmp/` swept on
    lock/exit/start; **no file bytes over the transport** (paths only).
25. Monitor bar: hostname, CPU, RAM, up/down, uptime, disk + `df -h` tooltip;
    2 s cadence; active-tab switching; disable stops the ticker; stale → dimmed.

**App shell**
26. All shortcuts (US + RU): Ctrl+K/L/T/W/Tab/1-9/,/Shift+E, F2, Delete, Esc.
27. Single instance: second launch focuses the first.
28. Window size/position memory across restart (A7).
29. Backend killed externally → error dialog + clean quit, no orphan; app quit →
    `pgrep shelve-backend` empty.
30. Backend logs on stderr only; stdout carries only the handshake line.

## B. HiDPI / multi-monitor matrix

From E4, re-run as acceptance:

31. 100 / 125 / 150 / 175 / 200 %: crisp text, correct terminal metrics
    (`stty size` matches visual), dialogs/prompts aligned, no clipping.
32. Move window 100 ↔ 200 % (Wayland) and 100 ↔ 150 % (X11): reflow, refit,
    input retained, no freeze, no white flash.
33. Maximize on the high-DPI monitor under terminal output: no presentation
    stall.
34. Wayland session (native) and X11/XWayland session both render correctly.
35. Zoom and OS DPI are independent (if T7 zoom was approved); default 0 leaves
    behavior unchanged.

## C. GPU / rendering matrix

36. GPU on by default: `--gpu-info` shows compositing/rasterization enabled on a
    GPU host; xterm WebGL addon active; no Chromium software-rendering warning.
37. `--disable-gpu` / `--disable-hardware-acceleration`: app runs on canvas,
    terminal usable.
38. GPU process forced to crash (or driver reset): Chromium recovers; the app
    stays usable; no data loss in open terminals.
39. No `--no-sandbox` needed on a normal host; AppImage on a userns-disabled
    host falls back and still runs (sandbox state logged).

## D. Performance budgets

40. Search over 300 nodes < 10 ms (log the timing).
41. Tree DOM rebuild < 50 ms.
42. Terminal 100 KB/s sustained: input latency low, UI responsive (record a
    manual FPS/latency note); 3 MB/s flood survives without crash or stall.
43. Idle memory and CPU comparable to the Wails build (record both, if the old
    build is still available).

## E. Security

44. No plaintext credentials on disk (`rg` the config dir) — vault only.
45. No plaintext credentials over the RPC/terminal transport; the **only** IPC
    exception remains the bastion `vault:kbdint-prompt.payload.prefill`.
46. Loopback listeners are `127.0.0.1` only, token-gated; a wrong/missing token
    is rejected; no other network calls.
47. Renderer has no Node access (`window.require` undefined); `contextIsolation`
    true; preload surface is only the reviewed API; CSP present.
48. `tmp/` files 0600 and swept; vault writes atomic; master password/derived
    key zeroized.

## F. Build & packaging

49. Clean `shelve-dev` image: `make build`, `make test`, `make test-race`,
    `make test-integration`, `make lint`, `make appimage` — all green.
50. `make appimage` artifact runs on a host without GTK4/WebKitGTK
    (pristine `debian:13-slim` + xvfb, or ALT p11); `make appimage-check`
    passes.
51. No host toolchain installed beyond Docker.
52. No reference to Wails/WebKit/`appimage-alt` anywhere outside git history.

## G. Parity statement

For each of A1–F52: PASS, or FAIL with root cause + a follow-up plan. The
migration is accepted only when all functional (A), HiDPI (B), GPU (C), perf
(D), security (E) and build (F) rows pass.

## Exit criteria

All rows pass; `internal/{vault,model,store,sshx,sshengine,sftp,monitor,config}`
diff against the pre-migration tree shows only the mechanical
`wailsvc`→`api` rename and no semantic change; AGENTS.md/README/master plan
match the shipped architecture; the migration is complete.

---

# E7 acceptance run — results (2026-09-15)

**Environment.** Docker-only execution from a non-interactive agent session. A
Wayland session exists on the host, but the agent cannot drive the GUI, move a
window across mixed-DPI monitors, force a GPU-process crash, or click through
dialogs; no real external sshd is available for manual `make run` QA (the
integration suite uses the testcontainers `docker/sshd`). The host has no
`node`/`npm`; a system `go` exists and was not installed by this phase. The host
config dir holds a real vault, so the seed tool was exercised with its `--dir`
override instead of `make seed`.

## Fixes this gate produced

- **Real data race fixed** (`internal/termws/server.go`): the package-global
  `fallbackGrace` was written by a test while server goroutines read it, which
  `-race` reported as `WARNING: DATA RACE`. It is now a per-`Server` field
  (default 3 s, behavior unchanged) read under the server mutex; the test uses a
  locked setter.
- **Deterministic test de-flaking** (`internal/sshengine`, `internal/sftp`,
  `internal/termws` tests): `CapturingEmitter.WaitEvent` only observed events
  emitted after it registered, so an echo, a keyboard-interactive round, or an
  exit emitted synchronously by the action under test was missed and failed on a
  15 s timeout. Tests now arm before the action (`Arm`/`EventWaiter`) or assert
  on the recorded event log / the staged temp on disk. The racy API was removed;
  every assertion is unchanged.
- **Automated D40 coverage added**: `TestStore300SessionsSearchBudget` measures
  search over the 331-node fixture and asserts the 10 ms budget.

## Row-by-row

Legend: **PASS** = executed here green; **PARTIAL** = backend half executed by
the Go unit/integration suites, interactive UI half not executable here;
**NOT RUN** = manual-only, cannot be executed from this session.

### A. Functional parity
- A1–A7 (vault create/wrong-password/auto-lock/lock/corrupt/perms) — PARTIAL:
  vault+config unit suites cover create/unlock/wrong-password/tamper/corrupt/
  lock/perms; the modal/shake/auto-lock timing UI is NOT RUN.
- A8 — PARTIAL: seed/unseed run against an isolated `--dir` (300 sessions +
  bastion; 0700/0600; `vault.json` ciphertext contains no plaintext secret;
  unseed removes `vault.json` and leaves `settings.json`). `make seed`/`unseed`
  against the host config dir NOT RUN (would overwrite a real vault).
- A9–A13, A15–A22, A24–A30 — PARTIAL: the underlying engine/API paths are green
  in `make test` + `make test-integration` (password/key auth, PTY echo, jump
  chains, local forwards, host-key mismatch, disconnect events, SFTP ops + edit,
  monitor metrics, 300-session perf smoke); no interactive desktop, so the
  UI-level rows (tree/editor dialogs, tabs, clipboard, prompts, SFTP panel,
  monitor bar, shell/shortcuts/single-instance/window/shutdown) are NOT RUN.
- A14, A23 — PARTIAL: pty resize and `-L`/`-D` forwards + failed-bind are
  exercised by the engine/integration suites; the live-window resize and the
  `ssh:forward` toast UI are NOT RUN.

### B. HiDPI / multi-monitor
- B31–B35 — NOT RUN. Root cause: needs a real desktop with mixed-scale monitors
  on both X11 and Wayland (and the interactive T7 zoom check). Follow-up: run
  the E4 verification rows 1–10 + zoom on a physical multi-monitor host; the
  E4 report already covers the escape-hatch paths, DPR 1/1.5/2 device emulation
  and `--disable-gpu` headlessly.

### C. GPU / rendering
- C36, C37 — PARTIAL: `--gpu-info`, GPU flags and the `--disable-gpu` switch are
  covered by the E4 headless runs; a real GPU host + live xterm WebGL check is
  NOT RUN.
- C38, C39 — PARTIAL/NOT RUN: the AppImage no-userns sandbox fallback is
  covered by E5 and the pristine `debian:13-slim` smoke below; forcing a GPU
  crash and the "no `--no-sandbox` needed" host check are NOT RUN here.

### D. Performance
- D40 — PASS: `TestStore300SessionsSearchBudget`, best-of-20 = ~93 µs over 331
  nodes (budget 10 ms); timing logged.
- D41 — NOT RUN for the DOM rebuild (needs a live renderer). The backend
  equivalent (`TestStore300SessionsFixturePerf`) passes with a 50 ms budget.
- D42 — PARTIAL: 100 KB/s / 3 MB/s engine smoke and the interactive-latency test
  are green; the manual FPS/latency note requires a live window.
- D43 — NOT RUN (needs the Wails v1.1 build for comparison, no longer present).

### E. Security
- E44 — PASS: `internal/config`/`vault` unit tests assert no secret fields and
  the isolated seeded `vault.json` scan found no plaintext credentials.
- E45 — PASS (unit-level): read DTOs expose only `AuthType`/`HasPassword`/
  `KeyPath`; the single documented kbdint `prefill` exception is the only path.
- E46 — PASS: the bridge binds `127.0.0.1:0` only and the token gate is asserted
  for `/rpc` and `/terminal` (missing/empty/wrong token rejected).
- E47 — PARTIAL: `contextIsolation: true`, `nodeIntegration: false`,
  `sandbox: true`, the named-channel preload surface and the vite-injected CSP
  are verified by static inspection; the `window.require === undefined` runtime
  assertion needs a live renderer (NOT RUN).
- E48 — PASS (unit-level): tmp files 0600 + sweep on lock/exit/start, atomic
  vault writes (tmp+rename) and key/password zeroization are asserted.

### F. Build & packaging
- F49 — PASS: clean-container `make build`, `make test`, `make test-race`,
  `make test-integration`, `make lint`, `make appimage` all green.
- F50 — PASS: `make appimage-check` extracts and inspects the AppImage and the
  best-effort `debian:13-slim` + xvfb smoke reaches the backend-ready handshake
  with no GTK4/WebKit.
- F51 — PARTIAL: no `node`/`npm` on the host (only a pre-existing system `go`);
  nothing was installed on the host by this phase.
- F52 — PASS: `git grep -i` for wails/wailsvc/wailsio/appimage-alt/linuxdeploy/
  webkitgtk/webview2 over the tracked tree (excluding `plans/`) returns no
  matches.

## G. Parity statement

All automated build/test/lint/packaging rows pass. The **interactive GUI
matrix (A1–A7, A9–A30 UI halves), HiDPI/multi-monitor (B31–B35), live GPU
rendering (C36–C38), DOM perf (D41, D43) and the runtime renderer-hardening
check (E47)** could not be reproduced in this session with no interactive
desktop, no mixed-DPI monitors, no GPU-crash control and no manual sshd.
Root cause: environment, not the app. Follow-up: execute the per-phase manual QA
checklists on a real Linux desktop (X11 + Wayland, 100–200 %, GPU on/off, AppImage
launch) and annotate the results in the commit message per project convention.

One deviation from the strict exit wording: the domain-package diff against the
pre-migration tree (commit `7dc90be`) is comment-only **plus** the additive,
absent-safe `ui.zoomLevel` settings field from E4 T7 (documented in master plan
§4). It is not a behavior change at the default `0`, but it is an addition, not
the pure `wailsvc`→`api` rename the exit criterion describes.
