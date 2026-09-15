// rpc/endpoint.ts — resolve the backend's per-run loopback endpoint.
//
// The Electron main process spawns the Go backend, reads its one-line stdout
// handshake ({"event":"ready","addr":"127.0.0.1:PORT","token":"…"}) and
// provisions it to the renderer through the preload bridge (master plan §5).
// The window can exist before that handshake lands, so resolution retries
// with backoff and never rejects: callers simply await it.
//
// The endpoint is per-process stable (the bridge binds once and keeps the
// same token for the process lifetime), so the first success is cached and
// every later caller reuses it — reconnects never re-provision.
import type { BridgeEndpoint } from "./types";

const INITIAL_BACKOFF_MS = 100;
const MAX_BACKOFF_MS = 2000;

let cached: BridgeEndpoint | null = null;
let inflight: Promise<BridgeEndpoint> | null = null;

function sleep(ms: number): Promise<void> {
    return new Promise((resolve) => window.setTimeout(resolve, ms));
}

/** True when the preload bridge exposed a usable provisional endpoint. */
function isValid(ep: BridgeEndpoint | null | undefined): ep is BridgeEndpoint {
    return !!ep && typeof ep.addr === "string" && ep.addr.length > 0 && typeof ep.token === "string" && ep.token.length > 0;
}

/**
 * Resolve the `{addr, token}` endpoint, retrying with backoff until the
 * backend handshake is available. The promise never rejects and the result
 * is cached for the process lifetime.
 */
export function resolveEndpoint(): Promise<BridgeEndpoint> {
    if (cached) {
        return Promise.resolve(cached);
    }
    if (inflight) {
        return inflight;
    }
    inflight = (async () => {
        let delay = INITIAL_BACKOFF_MS;
        for (;;) {
            try {
                const api = window.shelve;
                if (api && typeof api.bridgeEndpoint === "function") {
                    const ep = await api.bridgeEndpoint();
                    if (isValid(ep)) {
                        cached = { addr: ep.addr, token: ep.token };
                        return cached;
                    }
                }
            } catch {
                // Backend handshake not ready yet (or bridge rejected): retry.
            }
            await sleep(delay);
            delay = Math.min(delay * 2, MAX_BACKOFF_MS);
        }
    })();
    return inflight;
}
