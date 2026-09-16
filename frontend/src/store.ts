// store.ts — tiny typed pub/sub store (master plan §5).
//
// RULE: components subscribe to the store; they never call
// `events.on` directly. main.ts is the single owner of the rpc event
// bus and routes every event here via set(). This keeps one source of
// truth and prevents listener growth across lock/unlock cycles.
//
// LISTENER AUDIT (Phase 4d task 6): main.ts subscribes once to every
// Go→JS event; long-lived modules (ui/shortcuts, ui/autolock, search)
// install their document/global listeners exactly once at boot; per-mount
// component listeners (tree, tabs, terminal-view, statusbar, search box)
// are added on mount and released on unmount via the unsubscribe function
// `subscribe()` returns. Verify with DevTools that no listener grows after
// 50 modal cycles / 50 renders.
//
// The store also owns the few user actions that mutate backend state
// and must reconcile async results with the UI (connectSession,
// closeTab): those bridge the rpc service proxies and the reactive
// state in one place so components stay presentation-only.

import { AppService, CredentialService, JumpHostService, SessionService, TerminalService } from "./rpc";
import type {
    CredentialDTO,
    NodeDTO,
    SavedJumpHostDTO,
    SessionDTO,
    Settings,
    SftpPanelSide,
} from "./rpc/types";
import { toast } from "./components/toasts";
import { initTheme } from "./ui/theme";
import { applyZoomLevel } from "./ui/zoom";

// The wire contract lives in ONE place: rpc/types.ts mirrors the Go json
// tags. These re-exports keep every existing `type X from "../store"` import
// working without a second, drift-prone copy of the same shape.
export type {
    CredentialDTO,
    CredentialInput,
    JumpHostDTO,
    NodeDTO,
    SavedJumpHostDTO,
    SavedJumpHostInput,
    SearchResultDTO,
    SessionDTO,
    Settings,
    SftpEntryDTO,
    SftpPanelSide,
    TerminalSettings,
    WindowSettings,
} from "./rpc/types";

export type VaultState = "create" | "locked" | "unlocked";
export type TabState = "connecting" | "ready" | "error" | "closed";

export interface Tab {
    id: string;
    session: SessionDTO;
    state: TabState;
    errorMessage?: string;
    /** Present only after the remote shell reported an exit code. */
    exitStatus?: number;
}

/** Port-forward lifecycle view cached per tab (Phase 4c, ssh:forward). */
export interface ForwardDTO {
    spec: string;
    state: "listening" | "closed" | "failed";
    localAddr?: string;
    error?: string;
}

/** One SFTP listing row (SftpEntryDTO is defined in rpc/types.ts). */

/** Cached latest state of one in-flight transfer (Phase 5c, sftp:progress). */
export interface SftpTransfer {
    transferID: string;
    direction: "up" | "down";
    fileName: string;
    doneBytes: number;
    totalBytes: number;
    error?: string;
    /** True once the terminal event arrived (done == total, or an error). */
    finished: boolean;
}

/**
 * Latest system-monitor snapshot for a tab (plan P004, monitor:metrics).
 * Raw numbers only — formatting (MB/GB/TB, uptime) happens in the
 * monitor-bar component. `updatedAt` drives the stale-dimming heuristic.
 */
export interface MonitorMetrics {
    hostname: string;
    cpuPercent: number;
    memUsedBytes: number;
    memTotalBytes: number;
    netUpBps: number;
    netDownBps: number;
    uptimeSeconds: number;
    diskUsedPct: number;
    diskRoot: string;
    /** Full `df -h` output for the hover tooltip. */
    dfText: string;
    /** Epoch ms of the last monitor:metrics event for this tab. */
    updatedAt: number;
}

export interface StoreState {
    settings: Settings;
    vaultState: VaultState;
    /**
     * SFTP panel open state (ephemeral, like tabs — A3). Defaults to false so
     * the left-docked default shows the session tree. In right mode the store
     * auto-opens it when a ready tab becomes active while the setting is on;
     * in left mode the user toggles it from the top of the left column. When
     * docked right it only controls the right-hand SFTP column; when docked
     * left it switches the left column between the tree and the browser.
     */
    sftpPanelOpen: boolean;
    /** Current right-docked SFTP panel width in px (mirrors window.sftpWidth
     * live); unused while the panel is docked left. */
    sftpPanelWidth: number;
    tree: NodeDTO[];
    selectedID: string | null;
    searchQ: string;
    tabs: Tab[];
    activeTabID: string | null;
    leftPanelWidth: number;
    /**
     * Saved named credentials (plan P003): secret-free DTOs used by the
     * session editor's dropdown and the credential manager. Refreshed on
     * unlock and after every credential mutation.
     */
    credentials: CredentialDTO[];
    /**
     * Saved jump hosts (plan P006): secret-free DTOs used by the session
     * editor's dropdown and the jump host manager. Refreshed on unlock
     * and after every saved-jump-host mutation.
     */
    savedJumpHosts: SavedJumpHostDTO[];
    /**
     * tabID → ordered list of transfer snapshots (sftp:progress events,
     * Phase 5c). The SFTP panel footer derives its progress line from this
     * cache. Never persisted.
     */
    sftpTransfers: Record<string, SftpTransfer[]>;
    /**
     * tabID → port-forward lifecycle cache (ssh:forward events, Phase 4c),
     * used by the status bar. One entry per unique spec (latest state wins).
     * Never persisted.
     */
    forwards: Record<string, ForwardDTO[]>;
    /**
     * tabID → latest system-monitor snapshot (monitor:metrics events, plan
     * P004), rendered by the bottom monitor bar. Never persisted.
     */
    monitor: Record<string, MonitorMetrics>;
}

export const DEFAULT_LEFT_WIDTH = 320;
export const DEFAULT_SFTP_WIDTH = 320;

/**
 * Normalize an arbitrary dock-side value (config.Settings.SftpPanelSide): only
 * an explicit "right" keeps the browser on the right; anything else
 * (absent/unknown) falls back to "left". The single renderer-side copy of the
 * rule (Go normalize() keeps its own copy at the persistence boundary).
 */
export function normalizeSftpPanelSide(value: unknown): SftpPanelSide {
    return value === "right" ? "right" : "left";
}

/**
 * Map an arbitrary backend settings object onto our Settings shape (the single
 * normalization point: boot in main.ts and import reconciliation here).
 */
export function normalizeSettings(raw: Record<string, unknown>): Settings {
    const win = (raw.window ?? {}) as Record<string, unknown>;
    const term = (raw.terminal ?? {}) as Record<string, unknown>;
    const ui = (raw.ui ?? {}) as Record<string, unknown>;
    return {
        theme: typeof raw.theme === "string" ? raw.theme : "system",
        themeVariant: typeof raw.themeVariant === "string" ? raw.themeVariant : "",
        autoLockMinutes: Number(raw.autoLockMinutes ?? 0),
        sftpBrowserEnabled: Boolean(raw.sftpBrowserEnabled),
        // Dock side (plan sftp-panel-side): only an explicit "right" keeps the
        // browser on the right; anything else (absent/unknown) is "left".
        sftpPanelSide: normalizeSftpPanelSide(raw.sftpPanelSide),
        // Plan P004: presence-aware default ON (the backend also forces
        // `true` when the key is absent, so `?? true` is belt-and-braces).
        monitoringEnabled: Boolean(raw.monitoringEnabled ?? true),
        terminal: {
            fontFamily: String(term.fontFamily ?? "monospace"),
            fontSize: Number(term.fontSize ?? 13),
            scrollback: Number(term.scrollback ?? 10000),
        },
        textEditorCommand: String(raw.textEditorCommand ?? "xdg-open"),
        sftpInitialPath: String(raw.sftpInitialPath ?? "~"),
        window: {
            width: Number(win.width ?? 1280),
            height: Number(win.height ?? 800),
            leftWidth: Number(win.leftWidth ?? 320) || 320,
            sftpWidth: Number(win.sftpWidth ?? 320) || 320,
        },
        // E4 T7: user zoom, separate from OS DPI; absent -> 0 (no zoom).
        ui: { zoomLevel: Number(ui.zoomLevel ?? 0) || 0 },
    };
}

export const initialState: StoreState = {
    settings: {
        theme: "system",
        themeVariant: "",
        autoLockMinutes: 0,
        // Phase 5d: the SFTP browser is on by default (mirrors the backend
        // default; AppService.GetSettings() at boot overrides with the
        // persisted value via normalizeSettings). Same rule for the plan-P004
        // system monitor.
        sftpBrowserEnabled: true,
        sftpPanelSide: "left",
        monitoringEnabled: true,
        terminal: { fontFamily: "monospace", fontSize: 13, scrollback: 10000 },
        textEditorCommand: "xdg-open",
        sftpInitialPath: "~",
        window: { width: 1280, height: 800, leftWidth: DEFAULT_LEFT_WIDTH, sftpWidth: DEFAULT_SFTP_WIDTH },
        // Phase E4 T7: user zoom (0 = none), separate from the OS device
        // scale; applied at boot by main.ts via ui/zoom.
        ui: { zoomLevel: 0 },
    },
    vaultState: "locked",
    sftpPanelOpen: false,
    sftpPanelWidth: DEFAULT_SFTP_WIDTH,
    tree: [],
    selectedID: null,
    searchQ: "",
    tabs: [],
    activeTabID: null,
    leftPanelWidth: DEFAULT_LEFT_WIDTH,
    credentials: [],
    savedJumpHosts: [],
    forwards: {},
    sftpTransfers: {},
    monitor: {},
};

type Listener = (state: StoreState) => void;

let tempCounter = 0;

class Store {
    private state: StoreState = initialState;
    private listeners = new Set<Listener>();

    getState(): StoreState {
        return this.state;
    }

    /** Merge a partial update and notify subscribers (new object identity). */
    set(partial: Partial<StoreState>): void {
        this.state = { ...this.state, ...partial };
        // forEach avoids Set iteration, which needs target >= ES2015 or
        // --downlevelIteration (tsconfig sets neither).
        this.listeners.forEach((fn) => fn(this.state));
    }

    subscribe(fn: Listener): () => void {
        this.listeners.add(fn);
        return () => {
            this.listeners.delete(fn);
        };
    }

    // ------------------------------------------------------------ actions ---

    /** Re-fetch the session tree into the store (called after mutations). */
    async refreshTree(): Promise<void> {
        try {
            const tree = await SessionService.Tree();
            this.set({ tree });
        } catch (err) {
            toast("error", String(err));
        }
    }

    /**
     * Re-fetch the saved credentials (plan P003). Called on unlock, when
     * the session editor opens, and after credential create/update/delete.
     */
    async refreshCredentials(): Promise<void> {
        try {
            const credentials = await CredentialService.List();
            this.set({ credentials });
        } catch (err) {
            toast("error", String(err));
        }
    }

    /**
     * Re-fetch the saved jump hosts (plan P006). Called on unlock, when
     * the session editor opens, and after saved-jump-host mutations.
     */
    async refreshSavedJumpHosts(): Promise<void> {
        try {
            const savedJumpHosts = await JumpHostService.List();
            this.set({ savedJumpHosts });
        } catch (err) {
            toast("error", String(err));
        }
    }

    /**
     * Reconcile the UI after a configuration import (plan
     * config-export-import §6). Merge keeps live tabs and just refreshes the
     * tree/credentials/jump hosts. Replace tears the local tab state down (the
     * backend already disconnected the sessions), re-reads and applies the
     * imported settings (theme + zoom, no reload), then refreshes everything.
     */
    async afterImport(mode: "merge" | "replace"): Promise<void> {
        if (mode === "replace") {
            this.set({
                tabs: [],
                activeTabID: null,
                selectedID: null,
                forwards: {},
                sftpTransfers: {},
                monitor: {},
            });
            try {
                const raw = (await AppService.GetSettings()) as unknown as Record<string, unknown>;
                const settings = normalizeSettings(raw);
                this.set({ settings });
                initTheme(settings.theme, settings.themeVariant);
                applyZoomLevel(settings.ui.zoomLevel);
            } catch (err) {
                toast("error", String(err));
            }
        }
        await this.refreshTree();
        await this.refreshCredentials();
        await this.refreshSavedJumpHosts();
    }

    /** Set the live search query (empty string restores the tree). */
    setSearchQ(q: string): void {
        this.set({ searchQ: q });
    }

    /** Select a tree node by ID (null clears the selection). */
    selectNode(id: string | null): void {
        this.set({ selectedID: id });
    }

    /**
     * Open a terminal tab for a stored session (Phase 4b task 4): resolve
     * the secret-free snapshot, optimistically create a "connecting" tab,
     * then drive TerminalService.Connect; a returned Connect error marks the
     * tab "error". `terminal:status` events keep the tab's state current.
     */
    async connectSession(sessionID: string): Promise<void> {
        let dto: SessionDTO;
        try {
            dto = await SessionService.Session(sessionID);
        } catch (err) {
            toast("error", String(err));
            return;
        }
        const tempId = `tab-pending-${(tempCounter++).toString(36)}`;
        this.set({
            tabs: [...this.state.tabs, { id: tempId, session: dto, state: "connecting" }],
            activeTabID: tempId,
        });
        try {
            const tabID = await TerminalService.Connect(sessionID);
            // The user may have closed the optimistic tab while Connect was
            // still in flight (the temp id is clickable immediately). Tear the
            // now-orphaned backend session down instead of leaking it.
            if (!this.state.tabs.some((t) => t.id === tempId)) {
                try {
                    await TerminalService.Disconnect(tabID);
                } catch {
                    /* session already gone; nothing to clean up */
                }
                return;
            }
            this.replaceTab(tempId, tabID);
        } catch (err) {
            this.setTabState(tempId, "error", String(err));
        }
    }

    /** Activate (focus) a tab by ID. */
    activateTab(tabID: string): void {
        const { tabs, settings } = this.state;
        const tab = tabs.find((t) => t.id === tabID);
        const patch: Partial<StoreState> = { activeTabID: tabID };
        // Auto-open the SFTP panel when activating a ready tab — only when it
        // is docked right. Left-docked keeps the tree as the default view (the
        // user toggles to the browser); see plan sftp-panel-side.
        if (
            tab &&
            tab.state === "ready" &&
            settings.sftpBrowserEnabled &&
            settings.sftpPanelSide === "right"
        ) {
            patch.sftpPanelOpen = true;
        }
        this.set(patch);
    }

    /**
     * Close a tab: disconnect it first (unless already closed), remove it
     * and activate a neighbour. No confirmation (master plan A3).
     */
    async closeTab(tabID: string): Promise<void> {
        const { tabs, activeTabID } = this.state;
        const idx = tabs.findIndex((t) => t.id === tabID);
        if (idx === -1) {
            return;
        }
        const tab = tabs[idx];
        if (tab.state !== "closed") {
            try {
                await TerminalService.Disconnect(tabID);
            } catch (err) {
                toast("error", String(err));
            }
        }
        const remaining = tabs.filter((t) => t.id !== tabID);
        let nextActive = activeTabID;
        if (activeTabID === tabID) {
            const neighbour = remaining[idx] ?? remaining[idx - 1] ?? null;
            nextActive = neighbour ? neighbour.id : null;
        }
        const forwards = { ...this.state.forwards };
        delete forwards[tabID];
        const sftpTransfers = { ...this.state.sftpTransfers };
        delete sftpTransfers[tabID];
        const monitor = { ...this.state.monitor };
        delete monitor[tabID];
        this.set({ tabs: remaining, activeTabID: nextActive, forwards, sftpTransfers, monitor });
    }

    /**
     * Close every tab except tabID (tab context menu "Close Others").
     * Keeps tabID as the active tab.
     */
    async closeOtherTabs(tabID: string): Promise<void> {
        const { tabs } = this.state;
        const toClose = tabs.filter((t) => t.id !== tabID).map((t) => t.id);
        if (toClose.length === 0) {
            return;
        }
        await this.dropTabs(toClose, tabID);
    }

    /** Close every open tab (tab context menu "Close All Tabs"). */
    async closeAllTabs(): Promise<void> {
        const { tabs } = this.state;
        if (tabs.length === 0) {
            return;
        }
        await this.dropTabs(tabs.map((t) => t.id), null);
    }

    /**
     * Close every tab to the right of tabID (tab context menu "Close Tabs
     * to the Right"). The anchor tab stays; if the previously active tab
     * was closed, tabID becomes active.
     */
    async closeTabsToRight(tabID: string): Promise<void> {
        const { tabs } = this.state;
        const idx = tabs.findIndex((t) => t.id === tabID);
        if (idx === -1 || idx === tabs.length - 1) {
            return;
        }
        await this.dropTabs(tabs.slice(idx + 1).map((t) => t.id), tabID);
    }

    /**
     * Shared batch close: disconnect each non-closed tab (a failure never
     * aborts the rest — per-tab error toast, mirroring closeTab), drop the
     * tabs plus their per-tab caches, then pick the next active tab.
     * `anchorID` is the tab that survives (close-others / close-right) or
     * null for close-all.
     */
    private async dropTabs(ids: string[], anchorID: string | null): Promise<void> {
        const { tabs, activeTabID } = this.state;
        for (const id of ids) {
            const tab = tabs.find((t) => t.id === id);
            if (tab && tab.state !== "closed") {
                try {
                    await TerminalService.Disconnect(id);
                } catch (err) {
                    toast("error", String(err));
                }
            }
        }
        const closing = new Set(ids);
        const remaining = tabs.filter((t) => !closing.has(t.id));
        // Next active: close-all → none; a surviving active tab stays put;
        // otherwise fall back to the anchor tab.
        let nextActive: string | null = null;
        if (anchorID !== null) {
            const activeSurvives = remaining.some((t) => t.id === activeTabID);
            nextActive = activeSurvives && activeTabID ? activeTabID : anchorID;
        }
        const forwards = { ...this.state.forwards };
        const sftpTransfers = { ...this.state.sftpTransfers };
        const monitor = { ...this.state.monitor };
        for (const id of ids) {
            delete forwards[id];
            delete sftpTransfers[id];
            delete monitor[id];
        }
        this.set({ tabs: remaining, activeTabID: nextActive, forwards, sftpTransfers, monitor });
    }

    /**
     * Reorder the tab strip (pure frontend state — tabs are ephemeral and
     * never persisted, master plan A3). `toIndex` is clamped to range.
     */
    moveTab(tabID: string, toIndex: number): void {
        const { tabs } = this.state;
        const from = tabs.findIndex((t) => t.id === tabID);
        if (from === -1) {
            return;
        }
        const clamped = Math.max(0, Math.min(tabs.length - 1, toIndex));
        if (from === clamped) {
            return;
        }
        const next = tabs.slice();
        const [tab] = next.splice(from, 1);
        next.splice(clamped, 0, tab);
        this.set({ tabs: next });
    }

    /** Cache an ssh:forward lifecycle event for a tab (latest state per spec). */
    setForward(tabID: string, fwd: ForwardDTO): void {
        const current = this.state.forwards[tabID] || [];
        const withoutSpec = current.filter((f) => f.spec !== fwd.spec);
        this.set({
            forwards: { ...this.state.forwards, [tabID]: [...withoutSpec, fwd] },
        });
    }

    /**
     * Upsert one transfer snapshot for a tab (sftp:progress, Phase 5c).
     * Order of first appearance is preserved so the footer can show
     * "file i of n" for the active transfer.
     */
    setSftpTransfer(tabID: string, t: SftpTransfer): void {
        const list = (this.state.sftpTransfers[tabID] || []).slice();
        const idx = list.findIndex((x) => x.transferID === t.transferID);
        if (idx >= 0) {
            list[idx] = t;
        } else {
            list.push(t);
        }
        this.set({ sftpTransfers: { ...this.state.sftpTransfers, [tabID]: list } });
    }

    /** Cache the latest monitor snapshot for a tab (monitor:metrics, P004). */
    setMonitorMetrics(tabID: string, m: MonitorMetrics): void {
        this.set({ monitor: { ...this.state.monitor, [tabID]: m } });
    }

    /** Drop a tab's transfer cache (called when the tab closes). */
    clearSftpTransfers(tabID: string): void {
        const { sftpTransfers } = this.state;
        if (!sftpTransfers[tabID]) {
            return;
        }
        const next = { ...sftpTransfers };
        delete next[tabID];
        this.set({ sftpTransfers: next });
    }

    /** Mark a tab closed with an optional remote exit code (terminal:exit). */
    setTabExited(tabID: string, exitStatus?: number): void {
        const { tabs } = this.state;
        this.set({
            tabs: tabs.map((t) =>
                t.id === tabID ? { ...t, state: "closed", exitStatus } : t,
            ),
        });
    }

    /** Swap an optimistic temp tabID for the real tabID Connect returned. */
    replaceTab(oldID: string, newID: string): void {
        const { tabs, activeTabID } = this.state;
        if (!tabs.some((t) => t.id === oldID)) {
            return;
        }
        this.set({
            tabs: tabs.map((t) => (t.id === oldID ? { ...t, id: newID } : t)),
            activeTabID: activeTabID === oldID ? newID : activeTabID,
        });
    }

    /**
     * Update one tab's status (from a status event or a Connect error).
     * Status events never create tabs: the optimistic tab created by
     * connectSession is the only source, so an event for an unknown id is a
     * late event for a tab the user already closed and must be ignored (the
     * bridge writes responses and events from separate goroutines, so the
     * Disconnect response can overtake its own terminal:status "closed"
     * event). Auto-opens the SFTP panel when the active tab turns ready and
     * the setting is on — right-docked only (plan sftp-panel-side).
     */
    setTabState(tabID: string, state: TabState, message?: string): void {
        const { tabs, activeTabID, settings } = this.state;
        if (!tabs.some((t) => t.id === tabID)) {
            return;
        }
        const patch: Partial<StoreState> = {
            tabs: tabs.map((t) =>
                t.id === tabID ? { ...t, state, errorMessage: message || undefined } : t,
            ),
        };
        if (
            state === "ready" &&
            tabID === activeTabID &&
            settings.sftpBrowserEnabled &&
            settings.sftpPanelSide === "right"
        ) {
            patch.sftpPanelOpen = true;
        }
        this.set(patch);
    }
}

/** True when the active tab exists and is "ready" (reused by shell + store). */
export function hasReadyActiveTab(state: StoreState): boolean {
    const tab = state.tabs.find((t) => t.id === state.activeTabID);
    return !!tab && tab.state === "ready";
}

/**
 * Base predicate for whether the SFTP browser should be visible (master plan
 * §6): the setting must be on AND the panel must be open AND the active tab
 * must be ready. Side-agnostic — the shell decides which column it drives
 * (the right-hand column, or the left column in place of the tree). The
 * left-panel "connect first" hint rules are separate.
 */
export function sftpPanelVisible(state: StoreState): boolean {
    if (!state.settings.sftpBrowserEnabled || !state.sftpPanelOpen) {
        return false;
    }
    return hasReadyActiveTab(state);
}

export const store = new Store();