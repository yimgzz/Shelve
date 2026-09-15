// ui/dpi.ts — device-pixel-ratio monitor (phase E4 T4).
//
// Ports VSCode's `src/vs/base/browser/pixelRatio.ts` pattern: Chromium fires a
// `change` event on `matchMedia('(resolution: <dpr>dppx)')` exactly once, when
// the ratio crosses that value, so the listener must be re-armed around the
// new DPR after every change. This is the renderer's per-monitor DPI signal —
// the main process's debounced `display:changed` message (E4 T3) is the
// cross-check and is treated as an additional "re-measure now" trigger.
//
// Listeners are notified at most once per THROTTLE_MS so a monitor move (which
// fires several signals in a row) causes one refit, not a storm; a trailing
// flush guarantees the final value is still delivered.

import type { DisplayChanged } from "../rpc/types";

/** Minimum spacing between listener notifications. */
const THROTTLE_MS = 250;

type DpiListener = (dpr: number) => void;

const listeners = new Set<DpiListener>();

let currentDpr = window.devicePixelRatio;
let lastFiredAt = 0;
let pendingDpr: number | null = null;
let flushTimer: number | null = null;
let armed: MediaQueryList | null = null;
let initialized = false;

/** Subscribe to DPI/display changes; returns an unsubscribe function. */
export function onDpiChanged(cb: DpiListener): () => void {
    listeners.add(cb);
    return () => {
        listeners.delete(cb);
    };
}

function emit(dpr: number): void {
    lastFiredAt = Date.now();
    // forEach avoids Set iteration (tsconfig has no --downlevelIteration).
    listeners.forEach((cb) => {
        try {
            cb(dpr);
        } catch (err) {
            console.error("[dpi] listener failed:", err);
        }
    });
}

/** Record `dpr` and notify listeners, throttled to THROTTLE_MS. */
function notify(dpr: number): void {
    currentDpr = dpr;
    const elapsed = Date.now() - lastFiredAt;
    if (elapsed >= THROTTLE_MS && flushTimer === null) {
        emit(dpr);
        return;
    }
    pendingDpr = dpr;
    if (flushTimer !== null) {
        return;
    }
    flushTimer = window.setTimeout(() => {
        flushTimer = null;
        const value = pendingDpr;
        pendingDpr = null;
        if (value !== null) {
            emit(value);
        }
    }, Math.max(0, THROTTLE_MS - elapsed));
}

function onDprMediaChange(): void {
    const dpr = window.devicePixelRatio;
    armDprListener();
    if (dpr !== currentDpr) {
        notify(dpr);
    }
}

/** (Re-)arm the one-shot resolution query around the current DPR. */
function armDprListener(): void {
    if (armed) {
        armed.removeEventListener("change", onDprMediaChange);
        armed = null;
    }
    armed = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
    armed.addEventListener("change", onDprMediaChange);
}

/**
 * Install the DPR monitor and the main-process display signal (idempotent;
 * call once at boot). Never reloads anything: display changes are handled by
 * re-measuring and refitting in place.
 */
export function initDpi(): void {
    if (initialized) {
        return;
    }
    initialized = true;
    currentDpr = window.devicePixelRatio;
    armDprListener();

    const api = window.shelve;
    if (api && api.display) {
        api.display.onChange((_state: DisplayChanged) => {
            // Re-arm around whatever the ratio is now, then force a
            // notification even when the ratio did not change: a monitor move
            // can alter the geometry with an identical scale factor.
            armDprListener();
            notify(window.devicePixelRatio);
        });
    }
}
