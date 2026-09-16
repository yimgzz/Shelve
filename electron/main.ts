// electron/main.ts — the Shelve Electron main process (master plan §5, phase E2).
//
// Responsibilities:
//   * single instance (A7);
//   * spawn the E1 Go backend, read its one-line stdout handshake
//     ({"event":"ready","addr":"127.0.0.1:PORT","token":"…"}) and provision it
//     to the renderer through the preload bridge;
//   * open one sandboxed, context-isolated BrowserWindow showing the Vite
//     renderer (frontend/dist/index.html, or SHELVE_DEV_URL in dev);
//   * provide the reviewed native surface (single-file picker, clipboard,
//     debounced window geometry, debounced display/DPI changes) over IPC;
//   * own the backend lifecycle: bounded SIGTERM → SIGKILL on quit, error
//     dialog + quit if it dies unexpectedly (no orphan);
//   * own the process-wide GPU/display command line and report GPU status
//     (phase E4: VSCode-parity acceleration, per-monitor DPI, `--gpu-info`).
//
// Stdio is lifecycle only (E2-D5): stdout is the handshake, stderr is inherited
// logs. No application data crosses stdio.
import {
    BrowserWindow,
    Menu,
    app,
    clipboard,
    dialog,
    ipcMain,
    screen,
    shell,
    type IpcMainEvent,
    type IpcMainInvokeEvent,
} from "electron";
import { spawn, type ChildProcess } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

// The IPC payloads are declared once for main + preload + renderer in
// frontend/src/rpc/ipc.ts (type-only import: erased from the bundle; the
// frameless flag constant is the one shared runtime value).
import type { BridgeEndpoint, DisplayChanged, WindowState } from "../frontend/src/rpc/ipc";
import { FRAMELESS_TITLEBAR_FLAG } from "../frontend/src/rpc/ipc";

// ------------------------------------------------------------------ config ---

const SHUTDOWN_TIMEOUT_MS = 3000;
const HANDSHAKE_TIMEOUT_MS = 10_000;
// Cap on unterminated stdout bytes before the handshake is parsed. stdout is
// documented as handshake-only, so exceeding this means a stray writer; drop it
// instead of accumulating for the process lifetime.
const MAX_STDOUT_BUFFER = 64 * 1024;
const DEFAULT_WIDTH = 1280;
const DEFAULT_HEIGHT = 800;
// Mirror of the BrowserWindow minimums (master plan A7): the persisted
// geometry is clamped to these before it is handed to the renderer.
const MIN_WIDTH = 960;
const MIN_HEIGHT = 540;
// Debounce for window:state pushes on resize/move (master plan A7).
const WINDOW_STATE_DEBOUNCE_MS = 300;
// Debounce for display:changed pushes (E4 T3, mirroring VSCode's 100 ms
// `Event.debounce` over the `screen` display events).
const DISPLAY_DEBOUNCE_MS = 100;

// Custom renderer-drawn title bar (frameless window). `SHELVE_TITLEBAR=native`
// keeps the OS frame and hides the renderer bar (escape hatch for compositors
// that manage frameless resize/move poorly).
const FRAMELESS_TITLEBAR =
    (process.env.SHELVE_TITLEBAR ?? "").trim().toLowerCase() !== "native";

// Main-process diagnostics go to stderr (inherited from the shell / Makefile).
// The per-run token is never logged.
function log(...args: unknown[]): void {
    console.error("[shelve]", ...args);
}

// ---------------------------------------------------- GPU & display (E4) ---
// VSCode parity (`src/main.ts` `configureCommandlineSwitchesSync`): the switch
// set below is exactly what VSCode applies and nothing more. In particular we
// NEVER append --disable-gpu / --disable-gpu-compositing /
// --ignore-gpu-blocklist / --enable-gpu-rasterization / --use-gl /
// --use-angle / --disable-lcd-text. The only way to force software rendering
// is the explicit `--disable-gpu` / `--disable-hardware-acceleration` switch,
// which maps to `app.disableHardwareAcceleration()`.
//
// Everything here must run at module top level, BEFORE `app.whenReady()`.

/** True when the CLI carried `--<name>` (bare or `=value`). */
function hasArgvSwitch(name: string): boolean {
    const flag = `--${name}`;
    return process.argv.some((arg) => arg === flag || arg.startsWith(`${flag}=`));
}

/**
 * Value of a switch supplied on the ORIGINAL command line (null when absent,
 * "" for a bare `--<name>`). `app.commandLine` also carries switches Electron
 * and Chromium add themselves — notably the `--ozone-platform` they select
 * from the session — so reading it there would mistake a platform the app
 * chose for a user override.
 */
function cliSwitch(name: string): string | null {
    const prefix = `--${name}=`;
    for (const arg of process.argv) {
        if (arg.startsWith(prefix)) {
            return arg.slice(prefix.length);
        }
    }
    return process.argv.includes(`--${name}`) ? "" : null;
}

/**
 * Locale for Chromium's `--lang` switch. VSCode passes the resolved user/OS
 * locale; on Linux the equivalent source is the POSIX locale environment.
 * Returns null when nothing usable is set (Chromium then keeps its default).
 */
function systemLocale(): string | null {
    const raw = process.env.LC_ALL || process.env.LC_MESSAGES || process.env.LANG;
    if (!raw) {
        return null;
    }
    // "ru_RU.UTF-8@euro" -> "ru-RU"
    const base = raw.split(".")[0].split("@")[0].trim();
    if (!base) {
        return null;
    }
    return base.replace("_", "-");
}

/** True on a Wayland session (native or XWayland over a Wayland desktop). */
function waylandSession(): boolean {
    return process.env.XDG_SESSION_TYPE === "wayland" || !!process.env.WAYLAND_DISPLAY;
}

/**
 * The platform Chromium will actually use: an explicit `--ozone-platform`
 * (whether the user's or the one Electron derives from the session) always
 * beats `--ozone-platform-hint`, so the hint alone cannot switch backends.
 */
function effectivePlatform(): string {
    return app.commandLine.getSwitchValue("ozone-platform") || "(ozone default)";
}

/**
 * Linux display backend (E4 T2). Chromium's *fractional* device scale is
 * reliable on the native Wayland backend (`wp_fractional_scale_v1`); X11
 * exposes one global scale instead. A Wayland session therefore requests
 * `--ozone-platform-hint=auto` (per-monitor fractional scaling).
 *
 * Escape hatches, checked in order:
 *   1. `--ozone-platform=x11|wayland` on the command line (passthrough);
 *   2. `ELECTRON_OZONE_PLATFORM_HINT` (Electron reads it natively);
 *   3. `SHELVE_DISPLAY_BACKEND=x11|wayland|auto` (documented in README).
 *
 * Note on the hint: Electron pre-selects `--ozone-platform` from the session
 * before this runs, and Chromium ignores the hint once a platform is pinned —
 * so `auto` never *switches* away from Electron's choice, it only asks for the
 * automatic selection. The `x11`/`wayland` overrides use `--ozone-platform`
 * and therefore do take effect. Every log line reports the effective platform
 * so support output cannot mislead.
 *
 * `--force-device-scale-factor` is Chromium's own switch and stays a pure
 * passthrough; it is only logged here.
 */
function configureLinuxDisplayBackend(): void {
    const cli = cliSwitch("ozone-platform");
    if (cli !== null) {
        log(`display backend: ${cli || "?"} (--ozone-platform)`);
    } else if (process.env.ELECTRON_OZONE_PLATFORM_HINT) {
        const hint = process.env.ELECTRON_OZONE_PLATFORM_HINT;
        log(`display backend: hint=${hint}, effective=${effectivePlatform()} (ELECTRON_OZONE_PLATFORM_HINT)`);
    } else {
        const override = (process.env.SHELVE_DISPLAY_BACKEND ?? "").trim().toLowerCase();
        if (override === "auto") {
            app.commandLine.appendSwitch("ozone-platform-hint", "auto");
            log(`display backend: hint=auto, effective=${effectivePlatform()} (SHELVE_DISPLAY_BACKEND)`);
        } else if (override === "x11" || override === "wayland") {
            app.commandLine.appendSwitch("ozone-platform", override);
            log(`display backend: ${override} (SHELVE_DISPLAY_BACKEND)`);
        } else if (override) {
            log(`display backend: ignoring unknown SHELVE_DISPLAY_BACKEND=${override}`);
        } else if (waylandSession()) {
            app.commandLine.appendSwitch("ozone-platform-hint", "auto");
            log(`display backend: hint=auto, effective=${effectivePlatform()} — Wayland session (per-monitor fractional scale)`);
        } else {
            log("display backend: X11 (Chromium global scale from Xft.dpi/GTK XSettings)");
        }
    }
    const forced = cliSwitch("force-device-scale-factor");
    if (forced) {
        log(`display backend: --force-device-scale-factor=${forced}`);
    }
}

// ------------------------------------------------------- sandbox (E5) ---
// The Chromium sandbox helper (`chrome-sandbox`) must be root-owned setuid
// 4755; an AppImage mounts read-only and FUSE strips setuid, so the packaged
// app falls back to `--no-sandbox` + `--disable-gpu-sandbox` where the
// environment cannot support the sandbox (VSCode's documented fallback).
// `webPreferences.sandbox: true` is kept in every case — this decision is only
// about the OS-level Chromium sandbox, not the renderer's own isolation
// (contextIsolation + the minimal preload stay in force — master plan §8.11).
const SANDBOX_ON_VALUES = new Set(["1", "true", "on", "yes"]);
const SANDBOX_OFF_VALUES = new Set(["0", "false", "off", "no"]);

/**
 * Unprivileged user namespaces are how Chromium's sandbox works without a
 * setuid helper. Debian/Ubuntu expose a dedicated sysctl that can disable them
 * independently of the upstream limit, so when that file exists it decides;
 * otherwise fall back to the portable `user.max_user_namespaces` limit
 * (0 disables user namespaces entirely).
 */
function unprivilegedUsernsAvailable(): boolean {
    try {
        return readFileSync("/proc/sys/kernel/unprivileged_userns_clone", "utf8").trim() !== "0";
    } catch {
        // Not Debian/Ubuntu — consult the upstream limit below.
    }
    try {
        const max = Number.parseInt(readFileSync("/proc/sys/user/max_user_namespaces", "utf8").trim(), 10);
        return Number.isFinite(max) && max > 0;
    } catch {
        return false;
    }
}

/**
 * Decide whether to append the packaged-AppImage sandbox fallback. Precedence:
 *   1. `SHELVE_SANDBOX=1` forces the sandbox on (the `--no-sandbox` /
 *      `--disable-gpu-sandbox` switches are removed from Chromium's command
 *      line);
 *   2. `SHELVE_SANDBOX=0` forces it off;
 *   3. an explicit `--no-sandbox` on the command line;
 *   4. an AppImage run on a host without unprivileged user namespaces.
 * Otherwise the sandbox is left untouched (Electron/Chromium default).
 */
function resolveSandbox(): void {
    const raw = (process.env.SHELVE_SANDBOX ?? "").trim().toLowerCase();
    const forcedOn = SANDBOX_ON_VALUES.has(raw);
    const forcedOff = SANDBOX_OFF_VALUES.has(raw);
    if (raw.length > 0 && !forcedOn && !forcedOff) {
        log(`sandbox: ignoring unknown SHELVE_SANDBOX=${raw}`);
    }

    if (forcedOn) {
        // `process.argv` is only Node's view; Chromium's zygote reads the native
        // command line, so clear the switches through Electron's API.
        app.commandLine.removeSwitch("no-sandbox");
        app.commandLine.removeSwitch("disable-gpu-sandbox");
        log("sandbox: enabled (SHELVE_SANDBOX)");
        return;
    }

    let disable = false;
    let reason = "";
    if (forcedOff) {
        disable = true;
        reason = "SHELVE_SANDBOX";
    } else if (hasArgvSwitch("no-sandbox")) {
        disable = true;
        reason = "--no-sandbox";
    } else if (process.env.APPIMAGE && !unprivilegedUsernsAvailable()) {
        disable = true;
        reason = "AppImage without unprivileged user namespaces";
    }

    if (disable) {
        app.commandLine.appendSwitch("no-sandbox");
        app.commandLine.appendSwitch("disable-gpu-sandbox");
        log(`sandbox: disabled (${reason})`);
    } else {
        log("sandbox: enabled");
    }
}

/** The complete VSCode-parity command line (E4 T1/T2). */
function configureCommandLine(): void {
    // Sandbox decision first: it may append --no-sandbox / --disable-gpu-sandbox
    // (or strip --no-sandbox when forced on) before Chromium's zygote starts.
    resolveSandbox();

    // The ONLY off-switch for hardware acceleration (VSCode parity): there is
    // no forced-software path, and no --disable-gpu is ever appended.
    if (hasArgvSwitch("disable-gpu") || hasArgvSwitch("disable-hardware-acceleration")) {
        app.disableHardwareAcceleration();
        log("GPU: hardware acceleration disabled by request");
    }

    // GPU-channel features VSCode enables. An existing --enable-features value
    // is preserved (an advanced user may already have passed one).
    app.commandLine.appendSwitch(
        "enable-features",
        [
            "EarlyEstablishGpuChannel",
            "EstablishGpuChannelAsync",
            ...(process.platform === "linux" ? ["GlobalShortcutsPortal"] : []),
            app.commandLine.getSwitchValue("enable-features"),
        ]
            .filter((value) => value.length > 0)
            .join(","),
    );
    // Native window occlusion tracking misfires under compositors and stalls
    // rendering; VSCode disables it on every platform.
    app.commandLine.appendSwitch(
        "disable-features",
        ["CalculateNativeWinOcclusion", app.commandLine.getSwitchValue("disable-features")]
            .filter((value) => value.length > 0)
            .join(","),
    );
    // Each xterm instance may hold a WebGL context; the 16-context default is
    // too low for a window full of tabs.
    app.commandLine.appendSwitch("max-active-webgl-contexts", "32");

    if (process.platform === "linux") {
        // xdg-desktop-portal >= 4 supports the file dialog's current_folder.
        app.commandLine.appendSwitch("xdg-portal-required-version", "4");
        const lang = systemLocale();
        if (lang) {
            app.commandLine.appendSwitch("lang", lang);
        }
        configureLinuxDisplayBackend();
    }
}

configureCommandLine();

// ------------------------------------------------------------------ state ---

let win: BrowserWindow | null = null;
let backend: ChildProcess | null = null;
let endpoint: BridgeEndpoint | null = null;
let quitting = false;
let shutdownComplete = false;

// ------------------------------------------------------------------ paths ---

// E2-D4: the Go backend is a plain child process. Production ships it as an
// extraResource at `resources/backend/shelve-backend`; dev uses the repo build.
function backendPath(): string {
    return app.isPackaged
        ? path.join(process.resourcesPath, "backend", "shelve-backend")
        : path.join(app.getAppPath(), "bin", "shelve-backend");
}

// E3: the real renderer bundle built by Vite (base "./" so the relative
// assets resolve under file://). Shipped at frontend/dist by electron-builder.
function rendererFile(): string {
    return path.join(app.getAppPath(), "frontend", "dist", "index.html");
}

// The dev Vite URL is honored only for an unpackaged app, and only for a
// loopback http(s) origin: the window's preload exposes the per-run backend
// token, so allowing an arbitrary origin would leak it (master plan §8).
function devRendererURL(): string | null {
    if (app.isPackaged) {
        return null;
    }
    const raw = process.env.SHELVE_DEV_URL;
    if (!raw || raw.length === 0) {
        return null;
    }
    try {
        const url = new URL(raw);
        const loopback = url.hostname === "127.0.0.1" || url.hostname === "localhost" || url.hostname === "[::1]";
        if ((url.protocol === "http:" || url.protocol === "https:") && loopback) {
            return url.toString();
        }
        log(`ignoring non-loopback SHELVE_DEV_URL: ${url.origin}`);
    } catch {
        log("ignoring malformed SHELVE_DEV_URL");
    }
    return null;
}

function configDir(): string {
    const xdg = process.env.XDG_CONFIG_HOME;
    return xdg && xdg.length > 0
        ? path.join(xdg, "shelve")
        : path.join(homedir(), ".config", "shelve");
}

// Window geometry: read the persisted window.width/height (read-only,
// best-effort; fall back to 1280x800). Persisting *changes* is wired in E3
// through the renderer's SaveSettings.
function readWindowSize(): { width: number; height: number } {
    try {
        const raw = readFileSync(path.join(configDir(), "settings.json"), "utf8");
        const parsed = JSON.parse(raw) as {
            window?: { width?: unknown; height?: unknown };
        };
        const width = Number(parsed.window?.width);
        const height = Number(parsed.window?.height);
        return {
            width: Number.isFinite(width) && width > 0 ? Math.round(width) : DEFAULT_WIDTH,
            height: Number.isFinite(height) && height > 0 ? Math.round(height) : DEFAULT_HEIGHT,
        };
    } catch {
        return { width: DEFAULT_WIDTH, height: DEFAULT_HEIGHT };
    }
}

// ------------------------------------------------------------- backend ---

function parseHandshake(line: string): BridgeEndpoint | null {
    try {
        const obj = JSON.parse(line) as { event?: unknown; addr?: unknown; token?: unknown };
        if (
            obj.event === "ready" &&
            typeof obj.addr === "string" &&
            obj.addr.length > 0 &&
            typeof obj.token === "string" &&
            obj.token.length > 0
        ) {
            return { addr: obj.addr, token: obj.token };
        }
    } catch {
        // Not a JSON stdout line — ignore (stdout is reserved for the handshake).
    }
    return null;
}

function startBackend(): void {
    const bin = backendPath();
    const child = spawn(bin, [], { stdio: ["ignore", "pipe", "inherit"] });
    backend = child;

    const timer = setTimeout(() => {
        if (endpoint) {
            return;
        }
        quitting = true;
        dialog.showErrorBox(
            "Shelve backend did not start",
            `No ready handshake from ${bin} within ${HANDSHAKE_TIMEOUT_MS / 1000} s.`,
        );
        app.quit();
    }, HANDSHAKE_TIMEOUT_MS);

    // stdout carries exactly one line (the handshake); stderr carries the logs.
    // Detach and stop reading as soon as the handshake is parsed so a stray
    // newline-less write cannot grow the buffer for the process lifetime.
    let buf = "";
    const onStdout = (chunk: string): void => {
        if (endpoint) {
            return;
        }
        buf += chunk;
        let nl: number;
        while ((nl = buf.indexOf("\n")) >= 0) {
            const line = buf.slice(0, nl).trim();
            buf = buf.slice(nl + 1);
            if (line.length === 0) {
                continue;
            }
            const parsed = parseHandshake(line);
            if (parsed) {
                endpoint = parsed;
                clearTimeout(timer);
                log(`backend ready at ${parsed.addr}`);
                child.stdout?.off("data", onStdout);
                child.stdout?.pause();
                return;
            }
        }
        if (buf.length > MAX_STDOUT_BUFFER) {
            log(`discarding ${buf.length} bytes of unterminated backend stdout`);
            buf = "";
        }
    };
    child.stdout?.setEncoding("utf8");
    child.stdout?.on("data", onStdout);

    child.on("error", (err) => {
        clearTimeout(timer);
        quitting = true;
        dialog.showErrorBox("Shelve backend failed to start", String(err));
        app.quit();
    });

    // A dead backend leaves no working app (A3 parity): report and quit.
    child.on("exit", (code, signal) => {
        clearTimeout(timer);
        const how = signal ? `signal ${signal}` : `exit code ${code}`;
        log(`backend exited (${how})`);
        if (quitting) {
            return;
        }
        quitting = true;
        dialog.showErrorBox(
            "Shelve backend stopped",
            `The Shelve backend exited unexpectedly (${how}).`,
        );
        app.quit();
    });
}

async function shutdownBackend(): Promise<void> {
    const child = backend;
    if (!child || child.exitCode !== null || child.signalCode !== null) {
        return;
    }
    const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
    child.kill("SIGTERM");
    let deadline: NodeJS.Timeout | undefined;
    const timedOut = await Promise.race([
        exited.then(() => false),
        new Promise<boolean>((resolve) => {
            deadline = setTimeout(() => resolve(true), SHUTDOWN_TIMEOUT_MS);
        }),
    ]);
    if (deadline) {
        clearTimeout(deadline);
    }
    if (timedOut && child.exitCode === null && child.signalCode === null) {
        child.kill("SIGKILL");
    }
}

// ------------------------------------------------------------------ window ---

/**
 * Push debounced geometry to the renderer on resize/move (master plan A7).
 * The renderer merges it into settings and persists through the single Go
 * writer, so main never touches settings.json itself.
 */
function wireWindowState(w: BrowserWindow): void {
    let timer: NodeJS.Timeout | null = null;
    const send = (): void => {
        if (w.isDestroyed()) {
            return;
        }
        const [width, height] = w.getSize();
        const state: WindowState = {
            width: Math.max(MIN_WIDTH, width),
            height: Math.max(MIN_HEIGHT, height),
            maximized: w.isMaximized(),
        };
        w.webContents.send("window:state", state);
    };
    const schedule = (): void => {
        if (timer) {
            clearTimeout(timer);
        }
        timer = setTimeout(send, WINDOW_STATE_DEBOUNCE_MS);
    };
    w.on("resize", schedule);
    w.on("move", schedule);
    // Maximize/restore keeps the same geometry but must update the renderer's
    // restore glyph + tooltip (custom title bar).
    w.on("maximize", schedule);
    w.on("unmaximize", schedule);
}

// --------------------------------------------------------- display (E4) ---

/** The display state pushed to the renderer (see rpc/ipc.ts). */
function currentDisplayState(): DisplayChanged {
    const primaryScaleFactor = screen.getPrimaryDisplay().scaleFactor;
    // BrowserWindow exposes no getScaleFactor(); the display the window
    // actually occupies is the one that governs the window's DPR.
    const scaleFactor =
        win && !win.isDestroyed()
            ? screen.getDisplayMatching(win.getBounds()).scaleFactor
            : primaryScaleFactor;
    return { scaleFactor, primaryScaleFactor };
}

let displayWired = false;

/**
 * Push debounced display changes to the renderer (E4 T3, VSCode parity).
 * Mirrors `screen`'s three display events with a 100 ms debounce; work-area
 * only changes (panels/docks) are ignored because they cannot change the scale.
 * The window is never recreated or reloaded — the renderer re-measures and
 * refits, exactly like VSCode's `FontMeasurements.clearAllFontInfos` path.
 */
function wireDisplayChanges(w: BrowserWindow): void {
    if (displayWired) {
        return;
    }
    displayWired = true;
    let timer: NodeJS.Timeout | null = null;
    const schedule = (): void => {
        if (timer) {
            clearTimeout(timer);
        }
        timer = setTimeout(() => {
            timer = null;
            if (!w.isDestroyed()) {
                w.webContents.send("display:changed", currentDisplayState());
            }
        }, DISPLAY_DEBOUNCE_MS);
    };
    screen.on("display-metrics-changed", (_event, _display, changed) => {
        if (changed.length > 0 && changed.every((metric) => metric === "workArea")) {
            return;
        }
        schedule();
    });
    screen.on("display-added", schedule);
    screen.on("display-removed", schedule);
}

/**
 * Chromium child processes (GPU, network, renderer…). A GPU-process crash is
 * recovered by Chromium itself (it restarts the process; VSCode only
 * special-cases this on macOS), so this is diagnostics only.
 */
function wireProcessDiagnostics(): void {
    app.on("child-process-gone", (_event, details) => {
        log(`child process gone: type=${details.type} reason=${details.reason} exitCode=${details.exitCode}`);
    });
}

function createWindow(): BrowserWindow {
    const { width, height } = readWindowSize();
    const w = new BrowserWindow({
        width,
        height,
        minWidth: MIN_WIDTH,
        minHeight: MIN_HEIGHT,
        backgroundColor: "#06070f",
        show: false,
        frame: !FRAMELESS_TITLEBAR,
        autoHideMenuBar: true,
        webPreferences: {
            preload: path.join(__dirname, "preload.cjs"),
            // Documented way to pass a sync flag to a sandboxed preload (the
            // renderer checks it to decide whether to show its title bar).
            additionalArguments: FRAMELESS_TITLEBAR ? [FRAMELESS_TITLEBAR_FLAG] : [],
            // VSCode parity (windows.ts defaultBrowserWindowOptions +
            // windowImpl.ts): Electron's secure defaults, plus sandbox and the
            // two switches VSCode always sets; backgroundThrottling stays false
            // so terminal output remains live when the window is unfocused.
            contextIsolation: true,
            nodeIntegration: false,
            sandbox: true,
            spellcheck: false,
            enableWebSQL: false,
            autoplayPolicy: "user-gesture-required",
            backgroundThrottling: false,
        },
    });

    const devURL = devRendererURL();
    const appPageURL = devURL ?? pathToFileURL(rendererFile()).toString();

    // The renderer displays untrusted remote content (terminal output can
    // print clickable URLs). A link click must NEVER open a window that
    // inherits this preload — it exposes the per-run bridge token, which
    // grants full /rpc + /terminal access (master plan §8.11). Deny in-app
    // windows, hand http(s) to the OS browser, and block navigation away
    // from the app's own page.
    w.webContents.setWindowOpenHandler(({ url }) => {
        if (url.startsWith("https:") || url.startsWith("http:")) {
            void shell.openExternal(url);
        }
        return { action: "deny" };
    });
    w.webContents.on("will-navigate", (event, url) => {
        if (!url.startsWith(appPageURL)) {
            event.preventDefault();
            log(`blocked navigation to ${url}`);
        }
    });

    if (devURL) {
        void w.loadURL(devURL);
    } else {
        void w.loadFile(rendererFile());
    }

    w.webContents.on("did-finish-load", () =>
        log(`window content loaded: ${w.webContents.getURL()} (title: ${w.webContents.getTitle()})`),
    );
    w.webContents.on("did-fail-load", (_event, code, description, url) =>
        log(`window content failed to load (${code} ${description}): ${url}`),
    );

    // A dead renderer leaves a permanently blank window, i.e. no usable app:
    // report it once and quit (same policy as a dead backend). Chromium's GPU
    // process is handled separately (it restarts itself — see
    // wireProcessDiagnostics).
    w.webContents.on("render-process-gone", (_event, details) => {
        log(`renderer process gone: reason=${details.reason} exitCode=${details.exitCode}`);
        if (quitting || details.reason === "clean-exit") {
            return;
        }
        quitting = true;
        dialog.showErrorBox(
            "Shelve window stopped",
            `The window's renderer process exited unexpectedly (${details.reason}).`,
        );
        app.quit();
    });

    w.once("ready-to-show", () => w.show());
    wireWindowState(w);
    wireDisplayChanges(w);
    return w;
}

function focusWindow(): void {
    if (!win || win.isDestroyed()) {
        return;
    }
    if (win.isMinimized()) {
        win.restore();
    }
    win.show();
    win.focus();
}

// ------------------------------------------------------------------- IPC ---

function registerIpc(): void {
    // Defense in depth (§8.11): only this window's renderer may use the IPC
    // surface. The window-open/navigation guards keep untrusted pages out of
    // the app; this makes any that slipped through fail closed.
    const trusted = (event: IpcMainInvokeEvent): void => {
        if (!win || win.isDestroyed() || event.sender !== win.webContents) {
            throw new Error("unauthorized renderer");
        }
    };

    // The same trust rule for fire-and-forget `on` channels: `send` has no
    // rejection channel, so an untrusted sender is ignored silently.
    const trustedEvent = (event: IpcMainEvent): boolean =>
        !!win && !win.isDestroyed() && event.sender === win.webContents;

    // Custom title bar controls (frameless window). Fire-and-forget: no result
    // is needed and a failure has no recovery path in the renderer.
    ipcMain.on("window:minimize", (event) => {
        if (trustedEvent(event) && win) {
            win.minimize();
        }
    });
    ipcMain.on("window:toggle-maximize", (event) => {
        if (!trustedEvent(event) || !win) {
            return;
        }
        if (win.isMaximized()) {
            win.unmaximize();
        } else {
            win.maximize();
        }
    });
    ipcMain.on("window:close", (event) => {
        if (trustedEvent(event) && win) {
            win.close();
        }
    });

    // Gates the renderer until the backend handshake exists: rejects (the
    // HTTP-503 equivalent over IPC) before ready.
    ipcMain.handle("bridge:endpoint", (event) => {
        trusted(event);
        if (!endpoint) {
            throw new Error("bridge not ready");
        }
        return endpoint;
    });

    // System clipboard (main-process module) for ui/clipboard.ts.
    ipcMain.handle("clipboard:readText", (event) => {
        trusted(event);
        return clipboard.readText();
    });
    ipcMain.handle("clipboard:writeText", (event, text: unknown) => {
        trusted(event);
        clipboard.writeText(typeof text === "string" ? text : String(text ?? ""));
    });

    // Native single-file picker: the SSH key path fields (session editor,
    // credential and jump-host dialogs). Single file, full path, "" on
    // cancel — identical to the old AppService.PickFile behavior.
    ipcMain.handle("dialog:pickFile", async (event) => {
        trusted(event);
        if (!win || win.isDestroyed()) {
            return "";
        }
        const result = await dialog.showOpenDialog(win, {
            title: "Select an SSH private key",
            properties: ["openFile"],
        });
        if (result.canceled) {
            return "";
        }
        return result.filePaths[0] ?? "";
    });

    // Configuration export: a save dialog defaulting to
    // `shelve-config-<date>.shelve`. The path is the ONLY thing shared with
    // the backend; file bytes never cross the transport (master plan A5).
    ipcMain.handle("dialog:pickSaveFile", async (event, defaultName: unknown) => {
        trusted(event);
        if (!win || win.isDestroyed()) {
            return "";
        }
        const name =
            typeof defaultName === "string" && defaultName.trim().length > 0
                ? defaultName.trim()
                : "shelve-config.shelve";
        const result = await dialog.showSaveDialog(win, {
            title: "Export configuration",
            defaultPath: name,
            filters: [{ name: "Shelve configuration", extensions: ["shelve"] }],
        });
        if (result.canceled) {
            return "";
        }
        return result.filePath ?? "";
    });

    // Configuration import: an open dialog filtered to `.shelve` files.
    ipcMain.handle("dialog:pickOpenFile", async (event) => {
        trusted(event);
        if (!win || win.isDestroyed()) {
            return "";
        }
        const result = await dialog.showOpenDialog(win, {
            title: "Import configuration",
            properties: ["openFile"],
            filters: [{ name: "Shelve configuration", extensions: ["shelve"] }],
        });
        if (result.canceled) {
            return "";
        }
        return result.filePaths[0] ?? "";
    });
}

// ------------------------------------------------------------------ boot ---

/** `--gpu-info`: print the GPU feature status + basic info to stderr and exit. */
async function printGpuInfo(): Promise<void> {
    const featureStatus = app.getGPUFeatureStatus();
    let gpuInfo: unknown = null;
    try {
        gpuInfo = await app.getGPUInfo("basic");
    } catch (err) {
        log(`gpu info unavailable: ${String(err)}`);
    }
    process.stderr.write(`${JSON.stringify({ featureStatus, gpuInfo }, null, 2)}\n`);
}

const wantGpuInfo = hasArgvSwitch("gpu-info");

if (wantGpuInfo) {
    // Diagnostics mode: no window, no backend, no single-instance lock — a
    // support run must work while another Shelve instance is open.
    app.whenReady()
        .then(async () => {
            await printGpuInfo();
            app.exit(0);
        })
        .catch((err: unknown) => {
            log(`--gpu-info failed: ${String(err)}`);
            app.exit(1);
        });
} else if (!app.requestSingleInstanceLock()) {
    app.quit();
} else {
    wireProcessDiagnostics();

    app.on("second-instance", () => {
        log("second instance launched; focusing the existing window");
        focusWindow();
    });

    app.on("window-all-closed", () => app.quit());

    app.on("before-quit", (event) => {
        if (shutdownComplete) {
            return;
        }
        event.preventDefault();
        quitting = true;
        void shutdownBackend().finally(() => {
            shutdownComplete = true;
            app.quit();
        });
    });

    app.whenReady()
        .then(() => {
            Menu.setApplicationMenu(null);
            registerIpc();
            win = createWindow();
            // Support diagnostics: the chosen backend was logged pre-ready by
            // configureLinuxDisplayBackend(); the scale only exists post-ready.
            const display = currentDisplayState();
            log(
                `display: window scaleFactor=${display.scaleFactor} primary scaleFactor=${display.primaryScaleFactor}`,
            );
            log(
                `gpu: enable-features=[${app.commandLine.getSwitchValue("enable-features")}] ` +
                    `disable-features=[${app.commandLine.getSwitchValue("disable-features")}] ` +
                    `max-active-webgl-contexts=${app.commandLine.getSwitchValue("max-active-webgl-contexts")}`,
            );
            log(
                FRAMELESS_TITLEBAR
                    ? "title bar: frameless (renderer-drawn); SHELVE_TITLEBAR=native restores the OS frame"
                    : "title bar: native (OS frame)",
            );
            startBackend();
        })
        .catch((err: unknown) => {
            dialog.showErrorBox("Shelve failed to start", String(err));
            app.exit(1);
        });
}
