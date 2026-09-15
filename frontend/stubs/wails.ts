// frontend/stubs/wails.ts — TRANSITIONAL, phase E2 only (deleted in E3).
//
// The Wails-generated `frontend/bindings/` tree and the `@wailsio/runtime`
// package are gone as of E1/E2, but the renderer source is only rewired to the
// new RPC client in E3 (roadmap: "frontend transport swap"). Vite still bundles
// the current renderer for `make build`, so vite.config.ts aliases the removed
// modules here.
//
// The E2 window loads `electron/placeholder.html` and never executes this
// bundle, so the stub is never reached at runtime; any accidental use throws
// loudly instead of silently misbehaving.
const unavailable = new Error(
    "wails transport removed (phase E2) — the renderer is rewired in E3",
);

const stub: unknown = new Proxy(
    {},
    {
        get(_target, prop) {
            // Let module interop probes through; throw on real API access.
            if (typeof prop === "symbol" || prop === "then" || prop === "__esModule") {
                return undefined;
            }
            throw unavailable;
        },
    },
);

export const AppService = stub;
export const VaultService = stub;
export const SessionService = stub;
export const CredentialService = stub;
export const JumpHostService = stub;
export const TerminalService = stub;
export const SftpService = stub;
export const MonitorService = stub;
export const Events = stub;
export const Clipboard = stub;
