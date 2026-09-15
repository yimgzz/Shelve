// rpc/ipc.ts — the IPC payload contract shared across the process boundary
// (phase E3).
//
// All three sides reference these declarations:
//   * electron/main.ts   — sends `window:state`, resolves `bridge:endpoint`;
//   * electron/preload.ts — forwards them over contextBridge;
//   * the renderer (rpc/types.ts) — consumes them.
//
// They live here (types only, no runtime code, no `Window` augmentation) so a
// rename in main/preload can never drift silently from the renderer, which is
// exactly the failure mode a redeclared copy would hide.

/** The per-run loopback endpoint printed by the Go backend's handshake. */
export interface BridgeEndpoint {
    addr: string;
    token: string;
}

/** Debounced window geometry pushed by the Electron main process (A7). */
export interface WindowState {
    width: number;
    height: number;
}
