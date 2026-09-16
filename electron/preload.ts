// electron/preload.ts — the renderer's complete main-process surface
// (phase E2/E3; master plan §8.11).
//
// Bundled to CommonJS (dist-electron/preload.cjs) because ESM preload scripts
// are unsupported in sandboxed renderers. The surface is deliberately tiny
// and reviewed: the backend endpoint, the clipboard, the native file pickers
// (single/multi-file + directory), the debounced window geometry/display
// changes and the zoom level. No Node, no arbitrary IPC, no secrets (the
// per-run token rides `bridgeEndpoint()` only, never a log).
import { contextBridge, ipcRenderer, webFrame, type IpcRendererEvent } from "electron";

// The IPC payloads are declared once for main + preload + renderer in
// frontend/src/rpc/ipc.ts (type-only import: erased from the bundle; the
// frameless flag constant is the one shared runtime value).
import type { BridgeEndpoint, DisplayChanged, WindowState } from "../frontend/src/rpc/ipc";
import { FRAMELESS_TITLEBAR_FLAG } from "../frontend/src/rpc/ipc";

contextBridge.exposeInMainWorld("shelve", {
    // The per-run loopback endpoint ({addr, token}) printed by the Go backend
    // on stdout. The token is never logged or persisted; it rides this IPC
    // call only and then the loopback query string of the WebSocket upgrades.
    bridgeEndpoint: (): Promise<BridgeEndpoint> => ipcRenderer.invoke("bridge:endpoint"),

    // System clipboard via the Electron main process (works regardless of
    // secure-context restrictions on the web Clipboard API).
    clipboard: {
        readText: (): Promise<string> => ipcRenderer.invoke("clipboard:readText"),
        writeText: (text: string): Promise<void> => ipcRenderer.invoke("clipboard:writeText", text),
    },

    // Native single-file picker; resolves "" on cancel (the old
    // AppService.PickFile contract).
    pickFile: (): Promise<string> => ipcRenderer.invoke("dialog:pickFile"),

    // Configuration export/import pickers (plan config-export-import): a save
    // dialog seeded with defaultName and an open dialog filtered to .shelve
    // files. Both resolve "" on cancel; only paths cross the bridge.
    pickSaveFile: (defaultName: string): Promise<string> =>
        ipcRenderer.invoke("dialog:pickSaveFile", defaultName),
    pickOpenFile: (): Promise<string> => ipcRenderer.invoke("dialog:pickOpenFile"),

    // SFTP upload/download pickers: a native multi-file open dialog for
    // uploads (resolves [] on cancel) and a directory picker for downloads
    // (resolves "" on cancel). Only the selected paths cross the bridge.
    pickFiles: (): Promise<string[]> => ipcRenderer.invoke("dialog:pickFiles"),
    pickDirectory: (): Promise<string> => ipcRenderer.invoke("dialog:pickDirectory"),

    // Debounced window geometry from the main process; the renderer merges it
    // into settings and persists through AppService.SaveSettings (A7).
    windowState: {
        onChange: (cb: (state: WindowState) => void): (() => void) => {
            const listener = (_event: IpcRendererEvent, state: WindowState): void => cb(state);
            ipcRenderer.on("window:state", listener);
            return () => {
                ipcRenderer.removeListener("window:state", listener);
            };
        },
    },

    // Debounced display/DPI changes from the main process (E4 T3). The
    // renderer's own devicePixelRatio stays authoritative for cell metrics;
    // this is the "re-measure now" signal (VSCode keeps both).
    display: {
        onChange: (cb: (state: DisplayChanged) => void): (() => void) => {
            const listener = (_event: IpcRendererEvent, state: DisplayChanged): void => cb(state);
            ipcRenderer.on("display:changed", listener);
            return () => {
                ipcRenderer.removeListener("display:changed", listener);
            };
        },
    },

    // User zoom, kept strictly separate from OS DPI (E4 T7, the VSCode model:
    // zoom = 1.2 ** level). Mechanism only in E4 — no UI exposes it; the
    // renderer applies the persisted settings value once at boot.
    zoom: {
        setLevel: (level: number): void => webFrame.setZoomLevel(level),
        getLevel: (): number => webFrame.getZoomLevel(),
    },

    // Custom title bar controls (frameless window). `frameless` is a sync flag
    // passed by main through webPreferences.additionalArguments; the three
    // controls are fire-and-forget window commands. No secrets, no Node objects.
    titleBar: {
        frameless: process.argv.includes(FRAMELESS_TITLEBAR_FLAG),
        minimize: (): void => ipcRenderer.send("window:minimize"),
        toggleMaximize: (): void => ipcRenderer.send("window:toggle-maximize"),
        close: (): void => ipcRenderer.send("window:close"),
    },
});
