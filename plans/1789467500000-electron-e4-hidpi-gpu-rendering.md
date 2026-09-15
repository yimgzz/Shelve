# Phase E4 — HiDPI, multi-monitor, GPU acceleration & rendering parity

**Type:** Electron main + frontend rendering. **Prereq:** E3. **Next:** E5.
**This phase owns hard requirements 2, 3 and 4 of the migration.**

**Master plan:** [`1789467100000-master-plan.md`](1789467100000-master-plan.md) —
read **§5, §6, §11**; roadmap
[`1789467100000-electron-migration-roadmap.md`](1789467100000-electron-migration-roadmap.md)
**§1, §7**.

**Reference implementation (verified against `microsoft/vscode@main`, 2026-09-15):**
`src/main.ts` (`configureCommandlineSwitchesSync`, the `--sandbox`/`--no-sandbox`
block), `src/vs/platform/windows/electron-main/windows.ts`
(`defaultBrowserWindowOptions`: `zoomFactor`, `sandbox: true`,
`spellcheck: false`, `enableWebSQL: false`, `autoplayPolicy`),
`src/vs/platform/windows/electron-main/windowImpl.ts` (preload,
`backgroundThrottling`), `src/vs/platform/native/electron-main/nativeHostMainService.ts:152`
(debounced `onDidChangeDisplay`), `src/vs/base/browser/pixelRatio.ts`
(`matchMedia('(resolution: ')` DPR monitor), `src/vs/base/browser/browser.ts`
(zoom → `setZoomLevel`), `src/vs/base/node/osDisplayProtocolInfo.ts` (X11 vs
Wayland). VSCode pins **Electron 42.10.0**.

## Goal

1. **HiDPI correctness** at 100 / 125 / 150 / 175 / 200 %, including moving a
   window between monitors with different scale factors, on both X11 and
   Wayland sessions: crisp text, correct terminal cell metrics, no blur, no
   clipping, no freeze, no white flash.
2. **GPU acceleration on by default**, matching VSCode: never force software
   rendering; the only off-switch is an explicit
   `app.disableHardwareAcceleration()`; enable VSCode's GPU-channel features;
   degrade gracefully on GPU-process loss.
3. **Performance parity or better** and **delete every WebKitGTK workaround**
   that exists only to paper over WebKit bugs.

**Scope guard:** no feature changes (the optional zoom setting is flagged);
no packaging (E5); no cleanup-only deletions (E6).

## What VSCode actually does (the model to copy)

| Concern | VSCode behavior (source) | Our equivalent |
|---|---|---|
| GPU off | Only `app.disableHardwareAcceleration()` (via argv.json `disable-hardware-acceleration` / CLI alias). It **never** appends `--disable-gpu`/`--disable-gpu-compositing`. (`src/main.ts`) | same; accept `--disable-gpu` and `--disable-hardware-acceleration` |
| GPU features | enable `EarlyEstablishGpuChannel`, `EstablishGpuChannelAsync`; **disable `CalculateNativeWinOcclusion`**; Linux: `GlobalShortcutsPortal`, `xdg-portal-required-version=4`, `lang`; `max-active-webgl-contexts=32`. No `--ignore-gpu-blocklist`, no `--enable-gpu-rasterization`, no `--use-gl`/`--use-angle`. | same switch set |
| DPI signal | main: debounced (100 ms) `screen` `display-metrics-changed` (ignore work-area-only) + `display-added`/`display-removed`; renderer: `matchMedia('(resolution: Ndppx)')` DPR monitor; **re-measure fonts, never reload** (`FontMeasurements.clearAllFontInfos`, throttled 2 s) | `ui/dpi.ts` DPR monitor + `display:changed` IPC; refit terminals, clear metrics |
| Zoom vs DPI | separate: user zoom = `1.2 ** zoomLevel`, clamped ±8, applied at window creation via `webPreferences.zoomFactor` and at runtime via `webFrame.setZoomLevel`/`setZoomFactor`, persisted per window | separate `ui.zoomLevel` (optional, default 0) applied via `webFrame`; OS scale untouched |
| Linux backend | default X11/XWayland; **no Ozone switch** unless the user opts in (`ELECTRON_OZONE_PLATFORM_HINT=auto` / `--ozone-platform-hint=auto`) | see below: default Wayland-on-Wayland-session to get fractional scaling, with an override |
| Sandbox | `--sandbox` → `app.enableSandbox()`; else default appends `--no-sandbox` + `disable-gpu-sandbox` (compat) | renderer `sandbox: true`; no global disable; AppImage fallback in E5 |
| Resize/zoom | `backgroundThrottling: false` only for sessions windows | always off (terminal output must stay live when unfocused) |

## Task list

### T1 — `configureCommandLine()` in `electron/main.ts` (VSCode parity)

- Must run at module top level, **before** `app.whenReady()`.
- `--disable-gpu` / `--disable-hardware-acceleration` → `app.disableHardwareAcceleration()`.
- `app.commandLine.appendSwitch("enable-features", "EarlyEstablishGpuChannel,EstablishGpuChannelAsync" + (linux ? ",GlobalShortcutsPortal" : ""))`.
- `app.commandLine.appendSwitch("disable-features", "CalculateNativeWinOcclusion")`.
- `app.commandLine.appendSwitch("max-active-webgl-contexts", "32")`.
- Linux: `app.commandLine.appendSwitch("xdg-portal-required-version", "4")`; set `--lang` from `LC_ALL`/`LANG` as VSCode does.
- **Do not** append `--disable-gpu`, `--ignore-gpu-blocklist`, `--enable-gpu-rasterization`, `--use-gl`, `--use-angle`, or `--disable-lcd-text`.

### T2 — Linux display backend (X11 vs Wayland)

Requirement 2 (crisp 125/150 %) is the deciding factor: Chromium's **fractional**
device scale is reliable under Wayland (`wp_fractional_scale_v1`) but on X11 the
scale is global and historically integer-rounded. Therefore:

- Detect a Wayland session (`XDG_SESSION_TYPE === "wayland"` or
  `WAYLAND_DISPLAY` set) and append
  `--ozone-platform-hint=auto` so Chromium uses the native Wayland backend with
  per-monitor fractional scaling.
- Escape hatches (checked before the above, in order):
  1. `--ozone-platform=x11|wayland` on the CLI (passthrough; Chromium rejects
     any other value with `FATAL: Invalid ozone platform`, so `auto` belongs to
     the hint switch only);
  2. `ELECTRON_OZONE_PLATFORM_HINT` env (Electron-native, passthrough);
  3. `SHELVE_DISPLAY_BACKEND=x11|wayland|auto` (app-specific, documented in README).
- X11 session: rely on Chromium reading the global scale (Xft.dpi / GTK
  XSettings). If the primary display reports a scale the user wants forced,
  accept `--force-device-scale-factor=<n>` passthrough. Document the X11
  single-global-scale limitation in README troubleshooting (this replaces the
  old mixed-DPI WebKitGTK limitation note).
- Log the chosen backend + detected scale at startup (stderr) for support.

### T3 — Main-process DPI/display signal

- Mirror VSCode's debounce: `Event.debounce`-style helper over
  `screen.on("display-metrics-changed", (_e, _d, changed) => changed)`
  (ignore `["workArea"]`-only changes), `screen.on("display-added")`,
  `screen.on("display-removed")` with a 100 ms debounce.
- On fire: `win.webContents.send("display:changed", { scaleFactor:
  screen.getPrimaryDisplay().scaleFactor })` (plus the window's own scale via
  `win.getScaleFactor()` where available).
- Do **not** recreate or reload the window (VSCode does not).

### T4 — Renderer DPI monitor `frontend/src/ui/dpi.ts` (new)

- Port VSCode's `pixelRatio.ts` pattern: keep a
  `matchMedia(\`(resolution: ${window.devicePixelRatio}dppx)\`)` listener;
  re-arm after every change; expose
  `onDpiChanged(cb: (dpr: number) => void): () => void` and `initDpi()`.
- Also subscribe to `window.shelve.on("display:changed", …)` (E3 preload) and
  treat it as a DPI re-measure signal.
- On any change, notify listeners throttled to ≤1 per 250 ms.

### T5 — Terminal DPI/renderer rework (`frontend/src/terminal/xterm.ts`)

**Delete** (all WebKit-only):
- `recoverRenderer()`, `recoverAll()`, the `_renderService` private-API pokes,
  the `_isPaused`/dimension reads, the mixed-DPI `window` resize net, and the
  `shouldUseCanvasRenderer()` WebKit branch.
- The `recoverAll` throttle and the "focus recovery forces renderer recovery"
  behavior (P007 F3) — recovery is no longer a thing.

**Add:**
- Renderer selection: try `WebglAddon` (Chromium GPU path); on `onContextLoss`
  or init error, dispose and fall back to the default canvas renderer once (no
  retry loop). Software-GL detection stays as a guard.
- `onDpiChanged` → re-run `fit()` (debounced 150 ms) and force a full
  `term.refresh(0, term.rows - 1)` if the renderer reports zero/stale cell
  metrics; then `TerminalService.Resize(tabID, cols, rows)`.
- Keep the per-container `ResizeObserver` fit path (unchanged).

### T6 — `frontend/src/main.ts` focus/DPR wiring

- Simplify `restoreTerminalFocus()`: keep the refocus-on-focus/visibility
  behavior (harmless and useful) but drop `TermPool.recoverAll()`.
- `initDpi()` at boot; register `TermPool` as a DPI listener.
- Remove the last `WebKit`/`wails` mentions from comments; the
  `WEBKIT_DISABLE_DMABUF_RENDERER` env is already gone (root `main.go` deleted in
  E1).

### T7 — Zoom separate from DPI (optional; decision required)

- Mechanism: `ui.zoomLevel` integer, default 0, clamped ±8; applied with
  `webFrame.setZoomLevel(level)` in the renderer; persisted via
  `AppService.SaveSettings` (new field — additive, safe, absent → 0).
- VSCode parity says OS DPI and zoom are separate; this task provides the
  mechanism so a later plan can expose UI without rework.
- **Parity guard:** while the default is 0 and no UI is added, behavior is
  unchanged. Exposing the setting in the Settings dialog is a user-visible
  addition and must be explicitly approved before implementing; if approved,
  add it to the Terminal group and to the settings schema section of the master
  plan. Otherwise ship the mechanism only (dead-but-tested helper) or defer
  entirely.
- Decision recorded in the phase commit message.

### T8 — Diagnostics

- `--gpu-info` debug flag: print `app.getGPUFeatureStatus()` and
  `app.getGPUInfo("basic")` to stderr and exit.
- `child-process-gone` / `render-process-gone`: log type/reason; if the GPU
  process crashes, Chromium restarts it (no app action needed on Linux — VSCode
  only special-cases this on macOS). Add a one-shot toast/`app:toast`-style
  notification only if the UI becomes unusable.

## Verification

**Automated (container):** `make build`, `make test`, `make lint` green;
`--gpu-info` runs headlessly and prints `gpu_compositing`/`rasterization` (may
be software under `xvfb` — that is expected and must not fail the gate).

**Manual HiDPI matrix (host, annotate results in the commit message):**

| # | Scenario | Pass criteria |
|---|---|---|
| 1 | 100 % single monitor, X11 | crisp text, terminal cell metrics exact (`stty size` matches visual cols/rows), no clipping |
| 2 | 125 % single monitor, Wayland session | crisp (no blur), correct metrics, xterm selection aligns with glyphs |
| 3 | 150 % single monitor, Wayland session | same |
| 4 | 175 % and 200 % | same |
| 5 | Move window 100 % → 200 % monitor and back (Wayland) | reflow, terminal refits, focus/input retained, no freeze, no white flash |
| 6 | Move window 100 % → 150 % (X11) | refit or documented X11 limitation; never frozen |
| 7 | Maximize on the high-DPI monitor under a terminal flood | no presentation stall (the old WebKit bug) |
| 8 | GPU on (default) | `--gpu-info` shows compositing enabled; WebGL addon active |
| 9 | `--disable-gpu` | app runs, canvas renderer, no errors |
| 10 | Suspend/resume, workspace switch | terminal input works immediately |

**Performance (record numbers):**
- Terminal at 100 KB/s sustained and the 3 MB/s flood repro: UI responsive,
  input latency low, no crash, memory stable.
- 300-node vault: search < 10 ms, tree rebuild < 50 ms (unchanged budgets).
- 50 tab open/close + 20 lock/unlock: no listener/timer growth, no console
  errors, no GPU-context-lost spam.

## Gate (this phase)

`make build` + `make test` + `make lint` green; the manual matrix above executed
and annotated; the old WebKit workarounds are gone
(`rg -i "webkit|_renderService|WEBKIT_" frontend/src main.go electron` → no
matches).

## Exit criteria

Crisp rendering at all required scale factors on X11 and Wayland; per-monitor
moves handled without freeze/flash/reload; GPU acceleration on by default with
VSCode-identical switches and graceful fallback; terminal perf budgets met; zero
WebKit-specific code remains.
