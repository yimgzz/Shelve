// electron/preload.ts — the renderer's complete main-process surface (E2 task 3).
//
// Bundled to CommonJS (dist-electron/preload.cjs) because ESM preload scripts
// are unsupported in sandboxed renderers. Phase E2 exposes only
// `bridgeEndpoint()`; E3 adds clipboard, pickFile, windowState, onThemeChange
// and quit. Every addition is reviewed against master plan §8 (no secrets, no
// Node in the renderer).
import { contextBridge, ipcRenderer } from "electron";

contextBridge.exposeInMainWorld("shelve", {
    // The per-run loopback endpoint ({addr, token}) printed by the Go backend
    // on stdout. The token is never logged or persisted; it rides this IPC
    // call only and then the loopback query string of the WebSocket upgrades.
    bridgeEndpoint: () => ipcRenderer.invoke("bridge:endpoint"),
});
