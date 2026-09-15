// electron/main.ts — the Shelve Electron main process (master plan §5, phase E2).
//
// Responsibilities:
//   * single instance (A7);
//   * spawn the E1 Go backend, read its one-line stdout handshake
//     ({"event":"ready","addr":"127.0.0.1:PORT","token":"…"}) and provision it
//     to the renderer through the preload bridge;
//   * open one sandboxed, context-isolated BrowserWindow showing the E2
//     placeholder page (the real frontend lands in E3);
//   * own the backend lifecycle: bounded SIGTERM → SIGKILL on quit, error
//     dialog + quit if it dies unexpectedly (no orphan).
//
// Stdio is lifecycle only (E2-D5): stdout is the handshake, stderr is inherited
// logs. No application data crosses stdio.
import { BrowserWindow, Menu, app, dialog, ipcMain } from "electron";
import { spawn, type ChildProcess } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";

// ------------------------------------------------------------------ config ---

const SHUTDOWN_TIMEOUT_MS = 3000;
const HANDSHAKE_TIMEOUT_MS = 10_000;
// Cap on unterminated stdout bytes before the handshake is parsed. stdout is
// documented as handshake-only, so exceeding this means a stray writer; drop it
// instead of accumulating for the process lifetime.
const MAX_STDOUT_BUFFER = 64 * 1024;
const DEFAULT_WIDTH = 1280;
const DEFAULT_HEIGHT = 800;

interface BridgeEndpoint {
    addr: string;
    token: string;
}

// Main-process diagnostics go to stderr (inherited from the shell / Makefile).
// The per-run token is never logged.
function log(...args: unknown[]): void {
    console.error("[shelve]", ...args);
}

// -------------------------------------------------------------- GPU (E2) ---
// E2 baseline: hardware acceleration ON (VSCode parity — VSCode never appends
// --disable-gpu; its only off-switch is app.disableHardwareAcceleration()).
// E4 adds the DPI/GPU feature switches. Must run before `app` is ready.
if (
    process.argv.includes("--disable-gpu") ||
    process.argv.includes("--disable-hardware-acceleration")
) {
    app.disableHardwareAcceleration();
}

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

// E2 only: placeholder content; E3 switches this to frontend/dist/index.html.
function rendererFile(): string {
    return path.join(app.getAppPath(), "electron", "placeholder.html");
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

function createWindow(): BrowserWindow {
    const { width, height } = readWindowSize();
    const w = new BrowserWindow({
        width,
        height,
        minWidth: 960,
        minHeight: 540,
        backgroundColor: "#06070f",
        show: false,
        autoHideMenuBar: true,
        webPreferences: {
            preload: path.join(__dirname, "preload.cjs"),
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

    w.once("ready-to-show", () => w.show());
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
    // Gates the renderer until the backend handshake exists: rejects (the
    // HTTP-503 equivalent over IPC) before ready.
    ipcMain.handle("bridge:endpoint", () => {
        if (!endpoint) {
            throw new Error("bridge not ready");
        }
        return endpoint;
    });
}

// ------------------------------------------------------------------ boot ---

if (!app.requestSingleInstanceLock()) {
    app.quit();
} else {
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
            startBackend();
        })
        .catch((err: unknown) => {
            dialog.showErrorBox("Shelve failed to start", String(err));
            app.exit(1);
        });
}
