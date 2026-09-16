// rpc/ipc.ts — the IPC payload contract shared across the process boundary
// (phase E3).
//
// All three sides reference these declarations:
//   * electron/main.ts   — sends `window:state` + `display:changed`, resolves
//     `bridge:endpoint`;
//   * electron/preload.ts — forwards them over contextBridge;
//   * the renderer (rpc/types.ts) — consumes them.
//
// They live here (types only, no runtime code, no `Window` augmentation) so a
// rename in main/preload can never drift silently from the renderer, which is
// exactly the failure mode a redeclared copy would hide. The one exception is
// FRAMELESS_TITLEBAR_FLAG: a shared runtime token both processes must agree on.

/**
 * Additional-argument token main passes to the sandboxed preload to signal the
 * frameless title bar (see `webPreferences.additionalArguments`). Shared by
 * electron/main.ts and electron/preload.ts so the two cannot drift.
 */
export const FRAMELESS_TITLEBAR_FLAG = "--shelve-frameless";

/** The per-run loopback endpoint printed by the Go backend's handshake. */
export interface BridgeEndpoint {
    addr: string;
    token: string;
}

/** Debounced window geometry pushed by the Electron main process (A7). */
export interface WindowState {
    width: number;
    height: number;
    /** True while the window is maximized (custom title bar restore glyph). */
    maximized: boolean;
}

/**
 * Debounced display/DPI change pushed by the Electron main process (E4 T3).
 * The renderer's own `devicePixelRatio` is authoritative for cell metrics
 * (`ui/dpi.ts`); this signal only says "re-measure now" — the numbers are for
 * diagnostics/logging.
 */
export interface DisplayChanged {
    /** Scale factor of the display the window currently occupies. */
    scaleFactor: number;
    /** Scale factor of the primary display. */
    primaryScaleFactor: number;
}
