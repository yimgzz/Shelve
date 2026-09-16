// main.ts — application bootstrap + single owner of the rpc event bus.
//
// Master plan §5: waits for the DOM and the bridge socket, loads settings →
// theme, reads the vault status, then renders the unlock gate or the app
// shell. It subscribes ONCE to every backend event and routes them into the
// store or UI primitives. Components never call events.on directly.

import { events, AppService, VaultService, SessionService } from "./rpc";
import type { WindowState } from "./rpc/types";

import {
    store,
    normalizeSettings,
    activeTabID,
    type Settings,
    type VaultState,
    type TabState,
} from "./store";
import { initTheme, onThemeApplied } from "./ui/theme";
import { renderUnlockGate, type UnlockMode } from "./components/unlock";
import { renderShell } from "./components/shell";
import {
    showHostKeyPrompt,
    showKeyPrompt,
    showKbdintPrompt,
    type HostKeyPromptPayload,
    type KeyPromptPayload,
    type KbdintPromptPayload,
} from "./components/prompts";
import { toast } from "./components/toasts";
import { TermPool } from "./terminal/xterm";
import { initTerminalWs, isTerminalWsActive, setTerminalOutputHandler } from "./terminal/ws";
import { b64ToBytes } from "./ui/b64";
import { initShortcuts } from "./ui/shortcuts";
import { initAutoLock } from "./ui/autolock";
import { initDpi, onDpiChanged } from "./ui/dpi";
import { applyZoomLevel } from "./ui/zoom";

// Event names (master plan §5; the backend constants live in
// internal/api + internal/sshengine).
const EV = {
    VaultStateChanged: "vault:state-changed",
    HostKeyPrompt: "vault:hostkey-prompt",
    KeyPrompt: "vault:key-prompt",
    KbdintPrompt: "vault:kbdint-prompt",
    TerminalStatus: "terminal:status",
    TerminalData: "terminal:data",
    TerminalExit: "terminal:exit",
    Forward: "ssh:forward",
    SftpProgress: "sftp:progress",
    MonitorMetrics: "monitor:metrics",
    AppToast: "app:toast",
};

const root = document.getElementById("app-root")!;

/** Tear down every pooled terminal instance on vault lock (Phase 4c). */
function destroyTerminals(): void {
    TermPool.destroyAll();
}

/**
 * Restore keyboard focus to the active terminal. Moving the window between
 * monitors can move DOM focus to <body>, silently killing xterm input —
 * keydowns still reach the document but never the terminal's hidden
 * textarea. Re-focus on window focus/visibility change; a form field
 * (search, dialog) owns the keyboard and must not be stolen from. DPI
 * changes are handled separately by ui/dpi.ts, which refits rather than
 * repaints.
 */
function restoreTerminalFocus(): void {
    const st = store.getState();
    const id = activeTabID(st);
    if (st.vaultState !== "unlocked" || !id) {
        return;
    }
    const ae = document.activeElement;
    if (
        ae instanceof HTMLElement &&
        (ae.tagName === "INPUT" || ae.tagName === "TEXTAREA" || ae.tagName === "SELECT" || ae.isContentEditable)
    ) {
        return;
    }
    TermPool.activate(id);
}

// Tracks whether the app shell is currently rendered. The unlock success
// path is driven from BOTH the resolved Unlock promise and the
// vault:state-changed event (H2); this guard makes the double signal
// idempotent.
let shellMounted = false;

/** Render the gate or shell depending on the vault state. */
async function mount(vaultState: VaultState): Promise<void> {
    if (vaultState === "unlocked") {
        if (shellMounted) {
            return; // already swapped (promise + event both fired)
        }
        shellMounted = true;
        try {
            const tree = await SessionService.Tree();
            store.set({ tree });
        } catch (err) {
            console.error("Failed to load session tree:", err);
        }
        // Saved named credentials for the session editor dropdown and the
        // credential manager (plan P003); saved jump hosts likewise (plan P006).
        void store.refreshCredentials();
        void store.refreshSavedJumpHosts();
        renderShell(root);
    } else {
        shellMounted = false;
        const mode: UnlockMode = vaultState === "create" ? "create" : "locked";
        renderUnlockGate(root, mode, enterUnlocked);
    }
}

/** Swap to the app shell once the create/unlock call succeeded. */
function enterUnlocked(): void {
    if (shellMounted) {
        return;
    }
    store.set({ vaultState: "unlocked" });
    void mount("unlocked");
}

/** Route one backend event; every branch updates the store or UI. */
function handleEvent(name: string, payload: unknown): void {
    const p = (payload ?? {}) as Record<string, unknown>;

    switch (name) {
        case EV.VaultStateChanged: {
            const unlocked = Boolean(p.unlocked);
            const next: VaultState = unlocked ? "unlocked" : "locked";
            if (unlocked) {
                store.set({ vaultState: next });
                void mount(next);
            } else {
                destroyTerminals();
                store.set({
                    vaultState: next,
                    tree: [],
                    groups: [],
                    activeGroupID: null,
                    selectedID: null,
                    searchQ: "",
                    credentials: [],
                    savedJumpHosts: [],
                    forwards: {},
                    sftpTransfers: {},
                    monitor: {},
                });
                void mount(next);
            }
            break;
        }
        case EV.AppToast:
            toast((p.level === "error" ? "error" : "info") as "info" | "error", String(p.message ?? ""));
            break;
        case EV.HostKeyPrompt:
            showHostKeyPrompt(p as unknown as HostKeyPromptPayload);
            break;
        case EV.KeyPrompt:
            showKeyPrompt(p as unknown as KeyPromptPayload);
            break;
        case EV.KbdintPrompt:
            showKbdintPrompt(p as unknown as KbdintPromptPayload);
            break;
        case EV.TerminalStatus: {
            const tabID = String(p.tabID ?? "");
            const state = String(p.state ?? "connecting") as TabState;
            updateTabState(tabID, state, String(p.message ?? ""));
            break;
        }
        case EV.TerminalData: {
            // Plan P005: while the terminal WebSocket is active it is the
            // authoritative byte channel (the engine routes output to the
            // sink, so this event normally never fires then). This legacy
            // path remains as the pre-connect / headless fallback.
            if (isTerminalWsActive()) {
                break;
            }
            const tabID = String(p.tabID ?? "");
            TermPool.write(tabID, b64ToBytes(String(p.data ?? "")));
            break;
        }
        case EV.TerminalExit: {
            const tabID = String(p.tabID ?? "");
            const exitStatus = p.exitStatus != null ? Number(p.exitStatus) : undefined;
            store.setTabExited(tabID, exitStatus);
            break;
        }
        case EV.Forward: {
            const tabID = String(p.tabID ?? "");
            const state = String(p.state ?? "listening") as "listening" | "closed" | "failed";
            store.setForward(tabID, {
                spec: String(p.spec ?? ""),
                state,
                localAddr: p.localAddr != null ? String(p.localAddr) : undefined,
                error: p.error != null ? String(p.error) : undefined,
            });
            break;
        }
        case EV.SftpProgress: {
            // Per-tab transfer cache (Phase 5c): feed the SFTP panel's
            // footer progress line. Finished when an error is set or the
            // terminal event reports done == total.
            const tabID = String(p.tabID ?? "");
            const done = Number(p.doneBytes ?? 0);
            const total = Number(p.totalBytes ?? 0);
            const err = p.error != null ? String(p.error) : undefined;
            store.setSftpTransfer(tabID, {
                transferID: String(p.transferID ?? ""),
                direction: p.direction === "down" ? "down" : "up",
                fileName: String(p.fileName ?? ""),
                doneBytes: done,
                totalBytes: total,
                error: err,
                finished: !!err || (total > 0 && done >= total),
            });
            break;
        }
        case EV.MonitorMetrics: {
            // Bottom monitor bar cache (plan P004). Raw numbers; formatting
            // happens in monitor-bar.ts.
            const tabID = String(p.tabID ?? "");
            store.setMonitorMetrics(tabID, {
                hostname: String(p.hostname ?? ""),
                cpuPercent: Number(p.cpuPercent ?? 0),
                memUsedBytes: Number(p.memUsedBytes ?? 0),
                memTotalBytes: Number(p.memTotalBytes ?? 0),
                netUpBps: Number(p.netUpBps ?? 0),
                netDownBps: Number(p.netDownBps ?? 0),
                uptimeSeconds: Number(p.uptimeSeconds ?? 0),
                diskUsedPct: Number(p.diskUsedPct ?? 0),
                diskRoot: String(p.diskRoot ?? ""),
                dfText: String(p.dfText ?? ""),
                updatedAt: Date.now(),
            });
            break;
        }
        default:
            console.warn(`[main] unhandled event ${name}`, payload);
    }
}

/**
 * Apply a terminal:status event to a tab. Delegates to store.setTabState,
 * which updates the tab (creating it on demand from the recorded pending
  * session when the event beats the optimistic-tab reconciliation, Phase 4b
  * task 4) and auto-opens the SFTP right panel on "ready" — the hook logic
  * lives in the store (D5d-1).
  */
function updateTabState(tabID: string, state: TabState, message: string): void {
    store.setTabState(tabID, state, message);
}

/** Subscribe once to every master-plan §5 event. */
function subscribeEvents(): void {
    events.on(EV.VaultStateChanged, (payload) => handleEvent(EV.VaultStateChanged, payload));
    events.on(EV.AppToast, (payload) => handleEvent(EV.AppToast, payload));
    events.on(EV.HostKeyPrompt, (payload) => handleEvent(EV.HostKeyPrompt, payload));
    events.on(EV.KeyPrompt, (payload) => handleEvent(EV.KeyPrompt, payload));
    events.on(EV.KbdintPrompt, (payload) => handleEvent(EV.KbdintPrompt, payload));
    events.on(EV.TerminalStatus, (payload) => handleEvent(EV.TerminalStatus, payload));
    events.on(EV.TerminalData, (payload) => handleEvent(EV.TerminalData, payload));
    events.on(EV.TerminalExit, (payload) => handleEvent(EV.TerminalExit, payload));
    events.on(EV.Forward, (payload) => handleEvent(EV.Forward, payload));
    events.on(EV.SftpProgress, (payload) => handleEvent(EV.SftpProgress, payload));
    events.on(EV.MonitorMetrics, (payload) => handleEvent(EV.MonitorMetrics, payload));
}

/**
 * Merge the Electron main process's debounced window geometry into the
 * settings object and persist it through the single writer (Go
 * AppService.SaveSettings, the same atomic path as leftWidth/sftpWidth —
 * master plan A7).
 */
function applyWindowState(state: WindowState): void {
    const st = store.getState();
    const width = Math.round(Number(state.width));
    const height = Math.round(Number(state.height));
    if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
        return;
    }
    if (st.settings.window.width === width && st.settings.window.height === height) {
        return;
    }
    const settings: Settings = {
        ...st.settings,
        window: { ...st.settings.window, width, height },
    };
    store.set({ settings });
    void AppService.SaveSettings(settings).catch((err) => {
        console.error("Failed to persist window size:", err);
    });
}

/** Resolve when both the DOM and the backend bridge socket are ready. */
async function whenReady(): Promise<void> {
    const domReady =
        document.readyState === "loading"
            ? new Promise<void>((res) => document.addEventListener("DOMContentLoaded", () => res(), { once: true }))
            : Promise.resolve();
    await domReady;
    // The bridge socket carries every service call and backend event; the
    // window can exist before the backend handshake, so ready() retries.
    await events.ready();
}

async function boot(): Promise<void> {
    await whenReady();

    // Dev flag kept for dev tooling (search timing in tree.ts, dev notes);
    // the 4a/4b QA buttons it toggled were removed in 4d.
    window.__dsmDev = true;

    subscribeEvents();

    // Plan P005: the terminal I/O WebSocket is the primary byte channel.
    // Output frames decode straight into the term pool; start it before any
    // tab can open so keystrokes never wait on the bridge.
    setTerminalOutputHandler((tabID, bytes) => TermPool.write(tabID, bytes));
    initTerminalWs();

    // Global keyboard shortcuts + opt-in auto-lock (Phase 4d). Both install
    // their document listeners exactly once at boot (listener-audit rule).
    initShortcuts();
    initAutoLock();

    // Per-monitor DPI (phase E4): the renderer's matchMedia DPR monitor plus
    // the main process's debounced display signal. Every change re-fits the
    // live terminals through the pool — nothing is reloaded or recreated.
    initDpi();
    onDpiChanged(() => TermPool.applyDpiChange());

    // Restore xterm textarea focus after window moves. window.focus +
    // visibility cover the move itself; the capture-phase keydown fallback
    // catches cases where neither event fires (first keystroke refocuses,
    // the next lands).
    window.addEventListener("focus", restoreTerminalFocus);
    document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "visible") {
            restoreTerminalFocus();
        }
    });
    document.addEventListener(
        "keydown",
        (e) => {
            if (store.getState().vaultState !== "unlocked") {
                return;
            }
            const t = e.target as Element | null;
            const ae = document.activeElement;
            if (
                (!t || t === document.body || t === document.documentElement) &&
                (!ae || ae === document.body || ae === document.documentElement)
            ) {
                restoreTerminalFocus();
            }
        },
        true,
    );

    // Re-paint live terminals whenever the applied theme changes (mode,
    // variant, or OS theme switch in system mode). Plan P001 §5.
    onThemeApplied(() => TermPool.applyTheme());

    try {
        const settings = (await AppService.GetSettings()) as unknown as Record<string, unknown>;
        const normalized = normalizeSettings(settings);
        store.set({ settings: normalized });
        initTheme(normalized.theme, normalized.themeVariant);
        // E4 T7: apply the persisted user zoom (independent of the OS scale).
        // The default 0 is a no-op, so this changes nothing until a plan adds
        // a UI for it.
        applyZoomLevel(normalized.ui.zoomLevel);
        // Install the geometry listener only after settings are in the store:
        // an early event must never persist the defaults over a remembered
        // size or panel widths (master plan A7).
        window.shelve.windowState.onChange(applyWindowState);
    } catch (err) {
        console.error("Failed to load settings:", err);
        initTheme("system");
    }

    try {
        const status = (await VaultService.Status()) as { state: string; unlocked: boolean };
        const vaultState: VaultState =
            status.state === "unlocked" || status.unlocked ? "unlocked" : status.state === "create" ? "create" : "locked";
        store.set({ vaultState });
        void mount(vaultState);
    } catch (err) {
        console.error("Failed to read vault status:", err);
        renderUnlockGate(root, "locked", enterUnlocked);
    }
}

void boot();
