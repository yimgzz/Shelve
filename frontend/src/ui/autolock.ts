// ui/autolock.ts — opt-in vault auto-lock (Phase 4d task 4; master plan D3).
//
// Only active when settings.autoLockMinutes > 0 AND the vault is unlocked.
// A 60 s interval checks `now - lastActivity`; `lastActivity` is refreshed
// on pointerdown/keydown (throttled to 5 s so busy UIs don't hammer the
// clock). When the vault has been idle too long it calls VaultService.Lock()
// WITHOUT confirmation. The real teardown happens in main.ts on the
// resulting `vault:state-changed` event.
//
// Listeners are installed exactly once at boot (matches the listener-audit
// rule: main.ts / long-lived modules subscribe once; per-mount component
// listeners are removed on unmount).

import { VaultService } from "../rpc";
import { store } from "../store";
import { toast } from "../components/toasts";

const CHECK_INTERVAL_MS = 60_000;
const REFRESH_THROTTLE_MS = 5_000;

let lastActivity = Date.now();
let initialized = false;

/** Record activity (throttled): reset the idle clock for the next 5 s. */
function refreshActivity(): void {
    const now = Date.now();
    if (now - lastActivity < REFRESH_THROTTLE_MS) {
        return;
    }
    lastActivity = now;
}

/** 60 s tick: lock when the unlocked vault has been idle too long. */
function tick(): void {
    const s = store.getState();
    if (s.vaultState !== "unlocked") {
        return;
    }
    const minutes = s.settings.autoLockMinutes;
    if (!(minutes > 0)) {
        return;
    }
    if (Date.now() - lastActivity >= minutes * 60_000) {
        // Reset the clock so a failed Lock doesn't re-fire every interval.
        lastActivity = Date.now();
        void VaultService.Lock().catch((err) => toast("error", String(err)));
    }
}

/** Install the auto-lock activity listeners + interval. Call once at boot. */
export function initAutoLock(): void {
    if (initialized) {
        return;
    }
    initialized = true;

    document.addEventListener("pointerdown", refreshActivity);
    document.addEventListener("keydown", refreshActivity);
    window.setInterval(tick, CHECK_INTERVAL_MS);

    // Reset the idle clock whenever the vault transitions to unlocked, so a
    // fresh unlock isn't immediately eligible for auto-lock.
    let wasUnlocked = store.getState().vaultState === "unlocked";
    store.subscribe((s) => {
        const isUnlocked = s.vaultState === "unlocked";
        if (isUnlocked && !wasUnlocked) {
            lastActivity = Date.now();
        }
        wasUnlocked = isUnlocked;
    });
}