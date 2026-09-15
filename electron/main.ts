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
//     debounced window geometry) over IPC;
//   * own the backend lifecycle: bounded SIGTERM → SIGKILL on quit, error
//     dialog + quit if it dies unexpectedly (no orphan).
//
// Stdio is lifecycle only (E2-D5): stdout is the handshake, stderr is inherited
// logs. No application data crosses stdio.
import { BrowserWindow, Menu, app, clipboard, dialog, ipcMain, shell, type IpcMainInvokeEvent } from "electron";
import { spawn, type ChildProcess } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

// The IPC payloads are declared once for main + preload + renderer in
// frontend/src/rpc/ipc.ts (type-only import: erased from the bundle).
import type { BridgeEndpoint, WindowState } from "../frontend/src/rpc/ipc";

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

    w.once("ready-to-show", () => w.show());
    wireWindowState(w);
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
