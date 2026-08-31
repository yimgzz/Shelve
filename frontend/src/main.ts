// main.ts — application bootstrap + single owner of the Wails event bus.
//
// Master plan §5: waits for WindowRuntimeReady, loads settings → theme,
// reads the vault status, then renders the unlock gate or the app shell.
// It subscribes ONCE to every Go→JS event and routes them into the store
// or UI primitives. Components never call Events.On directly.

import { Events } from "@wailsio/runtime";
import { AppService, VaultService, SessionService } from "../bindings/shelve/internal/wailsvc";

import { store, type Settings, type VaultState, type NodeDTO, type TabState } from "./store";
import { initTheme, onThemeApplied, refreshFromSystem } from "./ui/theme";
import { renderUnlockGate, type UnlockMode } from "./components/unlock";
import { renderShell } from "./components/shell";
import {
    showHostKeyPrompt,
    showKeyPrompt,
    type HostKeyPromptPayload,
    type KeyPromptPayload,
} from "./components/prompts";
import { toast } from "./components/toasts";
import { TermPool } from "./terminal/xterm";
import { b64ToBytes } from "./ui/b64";
import { initShortcuts } from "./ui/shortcuts";
import { initAutoLock } from "./ui/autolock";

// Common Wails events (pinned @wailsio/runtime v3 beta). These live under
// Events.Types.Common in this version (master §5 names them events.Common.*).
const Common = Events.Types.Common;

// Event names (master plan §5; the backend constants live in
// internal/wailsvc + internal/sshengine).
const EV = {
    VaultStateChanged: "vault:state-changed",
    HostKeyPrompt: "vault:hostkey-prompt",
    KeyPrompt: "vault:key-prompt",
    TerminalStatus: "terminal:status",
    TerminalData: "terminal:data",
    TerminalExit: "terminal:exit",
    Forward: "ssh:forward",
    SftpProgress: "sftp:progress",
    AppToast: "app:toast",
};

const root = document.getElementById("app-root")!;

/** Map an arbitrary backend settings object onto our Settings shape. */
function toSettings(raw: Record<string, unknown>): Settings {
   const win = (raw.window ?? {}) as Record<string, unknown>;
   const term = (raw.terminal ?? {}) as Record<string, unknown>;
   return {
       theme: typeof raw.theme === "string" ? raw.theme : "system",
       themeVariant: typeof raw.themeVariant === "string" ? raw.themeVariant : "",
       autoLockMinutes: Number(raw.autoLockMinutes ?? 0),
        sftpBrowserEnabled: Boolean(raw.sftpBrowserEnabled),
        terminal: {
            fontFamily: String(term.fontFamily ?? "monospace"),
            fontSize: Number(term.fontSize ?? 13),
            scrollback: Number(term.scrollback ?? 10000),
        },
        textEditorCommand: String(raw.textEditorCommand ?? "xdg-open"),
        sftpInitialPath: String(raw.sftpInitialPath ?? "~"),
        sftpOpenCommand: String(raw.sftpOpenCommand ?? "xdg-open"),
        window: {
            width: Number(win.width ?? 1280),
            height: Number(win.height ?? 800),
            leftWidth: Number(win.leftWidth ?? 320) || 320,
        },
    };
}

/** Tear down every pooled terminal instance on vault lock (Phase 4c). */
function destroyTerminals(): void {
    TermPool.destroyAll();
}

/** Render the gate or shell depending on the vault state. */
async function mount(vaultState: VaultState): Promise<void> {
    if (vaultState === "unlocked") {
        try {
            const tree = (await SessionService.Tree()) as unknown as NodeDTO[];
            store.set({ tree });
        } catch (err) {
            console.error("Failed to load session tree:", err);
        }
        // Saved named credentials for the session editor dropdown and the
        // credential manager (plan P003).
        void store.refreshCredentials();
        renderShell(root);
    } else {
        const mode: UnlockMode = vaultState === "create" ? "create" : "locked";
        renderUnlockGate(root, mode);
    }
}

/** Route one Go→JS event; every branch updates the store or UI. */
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
                    tabs: [],
                    activeTabID: null,
                    selectedID: null,
                    searchQ: "",
                    credentials: [],
                    pendingSessions: {},
                    forwards: {},
                    sftpTransfers: {},
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
        case EV.TerminalStatus: {
            const tabID = String(p.tabID ?? "");
            const state = String(p.state ?? "connecting") as TabState;
            updateTabState(tabID, state, String(p.message ?? ""));
            break;
        }
        case EV.TerminalData: {
            // Batched b64 output → this tab's pooled terminal. Routed
            // directly (not through store.set) so high-throughput data
            // never triggers a full UI re-render (perf guard, §2 A6).
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
        default:
            console.warn(`[main] unhandled event ${name}`, payload);
    }
}

/**
 * Apply a terminal:status event to a tab. If the tab isn't in the store
 * yet (its `connecting` event can beat the optimistic-tab reconciliation,
 * Phase 4b task 4), create it on demand from the recorded pending session.
 */
function updateTabState(tabID: string, state: TabState, message: string): void {
    const { tabs } = store.getState();
    if (tabs.some((t) => t.id === tabID)) {
        store.set({
            tabs: tabs.map((t) => (t.id === tabID ? { ...t, state, errorMessage: message || undefined } : t)),
        });
        return;
    }
    const sess = store.getPendingSession(tabID);
    if (!sess) {
        return;
    }
    store.set({
        tabs: [...tabs, { id: tabID, session: sess, state, errorMessage: message || undefined }],
    });
}

/** Extract the payload from a WailsEvent (callback arg is `{ data, name }`). */
function eventData(ev: unknown): unknown {
    return (ev as { data?: unknown }).data;
}

/** Subscribe once to every master-plan §5 event. */
function subscribeEvents(): void {
    Events.On(EV.VaultStateChanged, (ev) => handleEvent(EV.VaultStateChanged, eventData(ev)));
    Events.On(EV.AppToast, (ev) => handleEvent(EV.AppToast, eventData(ev)));
    Events.On(EV.HostKeyPrompt, (ev) => handleEvent(EV.HostKeyPrompt, eventData(ev)));
    Events.On(EV.KeyPrompt, (ev) => handleEvent(EV.KeyPrompt, eventData(ev)));
    Events.On(EV.TerminalStatus, (ev) => handleEvent(EV.TerminalStatus, eventData(ev)));
    Events.On(EV.TerminalData, (ev) => handleEvent(EV.TerminalData, eventData(ev)));
    Events.On(EV.TerminalExit, (ev) => handleEvent(EV.TerminalExit, eventData(ev)));
    Events.On(EV.Forward, (ev) => handleEvent(EV.Forward, eventData(ev)));
    Events.On(EV.SftpProgress, (ev) => handleEvent(EV.SftpProgress, eventData(ev)));

    // OS theme changes (only honored in system mode).
    Events.On(Common.ThemeChanged, () => {
        console.debug("[main] ThemeChanged");
        refreshFromSystem();
    });
}

/** Resolve when both the DOM and the Wails runtime are ready. */
async function whenReady(): Promise<void> {
    const domReady =
        document.readyState === "loading"
            ? new Promise<void>((res) => document.addEventListener("DOMContentLoaded", () => res(), { once: true }))
            : Promise.resolve();
    const runtimeReady = new Promise<void>((res) => {
        const off = Events.On(Common.WindowRuntimeReady, () => {
            off();
            res();
        });
    });
    await domReady;
    await runtimeReady;
}

async function boot(): Promise<void> {
    await whenReady();

    // Dev flag kept for dev tooling (search timing in tree.ts, dev notes);
    // the 4a/4b QA buttons it toggled were removed in 4d.
    window.__dsmDev = true;

    subscribeEvents();

    // Global keyboard shortcuts + opt-in auto-lock (Phase 4d). Both install
    // their document listeners exactly once at boot (listener-audit rule).
    initShortcuts();
    initAutoLock();

    // Re-paint live terminals whenever the applied theme changes (mode,
    // variant, or OS theme switch in system mode). Plan P001 §5.
    onThemeApplied(() => TermPool.applyTheme());

    try {
        const settings = (await AppService.GetSettings()) as unknown as Record<string, unknown>;
        const normalized = toSettings(settings);
        store.set({ settings: normalized });
        initTheme(normalized.theme, normalized.themeVariant);
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
        renderUnlockGate(root, "locked");
    }
}

void boot();
