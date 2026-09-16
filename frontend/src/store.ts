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

/**
 * One VS Code-style terminal group: its own tab strip plus one visible
 * terminal. Groups are ephemeral frontend-only state (master plan A3);
 * `activeTabID` is the visible pane inside the group and the global "active
 * tab" is always the focused group's `activeTabID`.
 */
export interface TabGroup {
    /** Ephemeral "grp-<counter36>". */
    id: string;
    tabs: Tab[];
    activeTabID: string | null;
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
     * the left-docked default shows the session tree. The store auto-opens it
     * when a tab becomes/activates ready while the browser setting is on, on
     * BOTH dock sides (plan ui-ux-refinements §B); the toolbar [SFTP] and the
     * panel-header [Sessions]/[×] buttons toggle it. When docked right it only
     * controls the right-hand SFTP column; when docked left it switches the
     * left column between the tree and the browser.
     */
    sftpPanelOpen: boolean;
    /** Current right-docked SFTP panel width in px (mirrors window.sftpWidth
     * live); unused while the panel is docked left. */
    sftpPanelWidth: number;
    tree: NodeDTO[];
    selectedID: string | null;
    searchQ: string;
    /**
     * Terminal groups, left → right (VS Code editor groups). Each group owns
     * its tab strip and one visible terminal; `activeGroupID` is the focused
     * group. Ephemeral (A3): never persisted, not restored on relaunch.
     */
    groups: TabGroup[];
    /** ID of the focused group (its active tab is the globally active tab). */
    activeGroupID: string | null;
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
    groups: [],
    activeGroupID: null,
    leftPanelWidth: DEFAULT_LEFT_WIDTH,
    credentials: [],
    savedJumpHosts: [],
    forwards: {},
    sftpTransfers: {},
    monitor: {},
};

type Listener = (state: StoreState) => void;

let tempCounter = 0;
let groupCounter = 0;

/** Ephemeral group id ("grp-<counter36>"); never persisted (A3). */
function newGroupID(): string {
    return `grp-${(groupCounter++).toString(36)}`;
}

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
                groups: [],
                activeGroupID: null,
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
     * the secret-free snapshot, optimistically create a "connecting" tab in
     * the focused group (creating the first group when none exists), then
     * drive TerminalService.Connect; a returned Connect error marks the tab
     * "error". `terminal:status` events keep the tab's state current.
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
        const tab: Tab = { id: tempId, session: dto, state: "connecting" };
        const { groups, activeGroupID } = this.state;
        const targetIndex = groups.findIndex((g) => g.id === activeGroupID);
        let nextGroups: TabGroup[];
        if (targetIndex === -1) {
            // No focused group (no groups at all, or a stale id): create one.
            const group: TabGroup = { id: newGroupID(), tabs: [tab], activeTabID: tempId };
            nextGroups = [...groups, group];
            this.set({ groups: nextGroups, activeGroupID: group.id });
        } else {
            nextGroups = groups.map((g, i) =>
                i === targetIndex ? { ...g, tabs: [...g.tabs, tab], activeTabID: tempId } : g,
            );
            this.set({ groups: nextGroups, activeGroupID: groups[targetIndex].id });
        }
        try {
            const tabID = await TerminalService.Connect(sessionID);
            // The user may have closed the optimistic tab while Connect was
            // still in flight (the temp id is clickable immediately). Tear the
            // now-orphaned backend session down instead of leaking it.
            if (!findTab(this.state, tempId)) {
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

    /**
     * Focus a group (its active tab becomes the globally active tab). No-op
     * when it is already focused; the terminal view reconciles focus/fit.
     */
    activateGroup(groupID: string): void {
        if (this.state.activeGroupID === groupID) {
            return;
        }
        if (!this.state.groups.some((g) => g.id === groupID)) {
            return;
        }
        this.set({ activeGroupID: groupID });
    }

    /** Activate (focus) a tab by ID (and focus its group). */
    activateTab(tabID: string): void {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const { group, groupIndex } = loc;
        const tab = group.tabs[loc.tabIndex];
        const groups = this.state.groups.map((g, i) =>
            i === groupIndex ? { ...g, activeTabID: tabID } : g,
        );
        const patch: Partial<StoreState> = { groups, activeGroupID: group.id };
        // Auto-open the SFTP panel when activating a ready tab (both dock
        // sides — plan ui-ux-refinements §B). The tree/panel swap on the left
        // is visible immediately; the [Sessions] button returns to the tree.
        if (tab.state === "ready" && this.state.settings.sftpBrowserEnabled) {
            patch.sftpPanelOpen = true;
        }
        this.set(patch);
    }

    /**
     * Close a tab: disconnect it first (unless already closed), remove it
     * and activate a neighbour. No confirmation (master plan A3).
     */
    async closeTab(tabID: string): Promise<void> {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const tab = loc.group.tabs[loc.tabIndex];
        if (tab.state !== "closed") {
            try {
                await TerminalService.Disconnect(tabID);
            } catch (err) {
                toast("error", String(err));
            }
        }
        const groups = this.state.groups.map((g) => ({ ...g, tabs: g.tabs.slice() }));
        const group = groups[loc.groupIndex];
        group.tabs.splice(loc.tabIndex, 1);
        if (group.activeTabID === tabID) {
            group.activeTabID =
                group.tabs[loc.tabIndex]?.id ?? group.tabs[loc.tabIndex - 1]?.id ?? null;
        }
        let activeGroupID = this.state.activeGroupID;
        if (group.tabs.length === 0) {
            groups.splice(loc.groupIndex, 1);
            if (activeGroupID === group.id) {
                activeGroupID = groups[loc.groupIndex]?.id ?? groups[loc.groupIndex - 1]?.id ?? null;
            }
        }
        const forwards = { ...this.state.forwards };
        delete forwards[tabID];
        const sftpTransfers = { ...this.state.sftpTransfers };
        delete sftpTransfers[tabID];
        const monitor = { ...this.state.monitor };
        delete monitor[tabID];
        this.set({ groups, activeGroupID, forwards, sftpTransfers, monitor });
    }

    /**
     * Close every tab except tabID in its group (tab context menu "Close
     * Others"). Keeps tabID as the group's active tab.
     */
    async closeOtherTabs(tabID: string): Promise<void> {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const toClose = loc.group.tabs.filter((t) => t.id !== tabID).map((t) => t.id);
        if (toClose.length === 0) {
            return;
        }
        await this.dropTabs(toClose, tabID, loc.group.id);
    }

    /** Close every tab in the clicked tab's group (tab context menu "Close All Tabs"). */
    async closeAllTabs(tabID: string): Promise<void> {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const ids = loc.group.tabs.map((t) => t.id);
        if (ids.length === 0) {
            return;
        }
        await this.dropTabs(ids, null, loc.group.id);
    }

    /**
     * Close every tab to the right of tabID within its group (tab context
     * menu "Close Tabs to the Right"). The anchor tab stays; if the
     * previously active tab was closed, tabID becomes active.
     */
    async closeTabsToRight(tabID: string): Promise<void> {
        const loc = findTab(this.state, tabID);
        if (!loc || loc.tabIndex === loc.group.tabs.length - 1) {
            return;
        }
        await this.dropTabs(
            loc.group.tabs.slice(loc.tabIndex + 1).map((t) => t.id),
            tabID,
            loc.group.id,
        );
    }

    /**
     * Shared batch close scoped to one group: disconnect each non-closed tab
     * (a failure never aborts the rest — per-tab error toast, mirroring
     * closeTab), drop the tabs plus their per-tab caches, then pick the next
     * active tab. `anchorID` is the tab that survives (close-others /
     * close-right) or null for close-all. An emptied group is removed and
     * `activeGroupID` falls back to a neighbouring group.
     */
    private async dropTabs(ids: string[], anchorID: string | null, groupID: string): Promise<void> {
        const loc = this.state.groups.findIndex((g) => g.id === groupID);
        if (loc === -1) {
            return;
        }
        const source = this.state.groups[loc];
        for (const id of ids) {
            const tab = source.tabs.find((t) => t.id === id);
            if (tab && tab.state !== "closed") {
                try {
                    await TerminalService.Disconnect(id);
                } catch (err) {
                    toast("error", String(err));
                }
            }
        }
        const closing = new Set(ids);
        const groups = this.state.groups.map((g) => ({ ...g, tabs: g.tabs.slice() }));
        const group = groups[loc];
        const oldActiveIdx = group.tabs.findIndex((t) => t.id === group.activeTabID);
        group.tabs = group.tabs.filter((t) => !closing.has(t.id));
        let nextActive: string | null = group.activeTabID;
        if (nextActive === null || closing.has(nextActive)) {
            nextActive = group.tabs[oldActiveIdx]?.id ?? group.tabs[oldActiveIdx - 1]?.id ?? null;
        }
        if (anchorID !== null && !group.tabs.some((t) => t.id === nextActive)) {
            nextActive = anchorID;
        }
        group.activeTabID = nextActive;
        let activeGroupID = this.state.activeGroupID;
        if (group.tabs.length === 0) {
            groups.splice(loc, 1);
            if (activeGroupID === group.id) {
                activeGroupID = groups[loc]?.id ?? groups[loc - 1]?.id ?? null;
            }
        }
        const forwards = { ...this.state.forwards };
        const sftpTransfers = { ...this.state.sftpTransfers };
        const monitor = { ...this.state.monitor };
        for (const id of ids) {
            delete forwards[id];
            delete sftpTransfers[id];
            delete monitor[id];
        }
        this.set({ groups, activeGroupID, forwards, sftpTransfers, monitor });
    }

    /**
     * Move a tab within or across groups (pure frontend state — tabs are
     * ephemeral and never persisted, master plan A3). `toIndex` is clamped to
     * the target group's range; a cross-group move focuses the target group
     * and makes the moved tab active, and removes an emptied source group.
     */
    moveTab(tabID: string, toGroupID: string, toIndex: number): void {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const tab = loc.group.tabs[loc.tabIndex];

        if (loc.group.id === toGroupID) {
            // Within-group reorder keeps the pre-split semantics (no focus
            // change; a no-op drop is a no-op).
            const from = loc.tabIndex;
            const clamped = Math.max(0, Math.min(loc.group.tabs.length - 1, toIndex));
            if (from === clamped) {
                return;
            }
            const groups = this.state.groups.map((g, i) =>
                i === loc.groupIndex ? { ...g, tabs: g.tabs.slice() } : g,
            );
            const tabs = groups[loc.groupIndex].tabs;
            const [moved] = tabs.splice(from, 1);
            tabs.splice(clamped, 0, moved);
            this.set({ groups });
            return;
        }

        const groups = this.state.groups.map((g) => ({ ...g, tabs: g.tabs.slice() }));
        const source = groups[loc.groupIndex];
        source.tabs.splice(loc.tabIndex, 1);
        if (source.activeTabID === tabID) {
            source.activeTabID =
                source.tabs[loc.tabIndex]?.id ?? source.tabs[loc.tabIndex - 1]?.id ?? null;
        }
        if (source.tabs.length === 0) {
            groups.splice(loc.groupIndex, 1);
        }
        const target = groups.find((g) => g.id === toGroupID);
        if (!target) {
            return;
        }
        const clamped = Math.max(0, Math.min(target.tabs.length, toIndex));
        target.tabs.splice(clamped, 0, tab);
        target.activeTabID = tab.id;
        this.set({ groups, activeGroupID: target.id });
    }

    /**
     * Split the clicked tab into a group on `direction` (tab context menu /
     * VS Code chords). The destination is the adjacent group when one exists
     * on that side, else a new group inserted there; the moved tab becomes the
     * destination's active tab and the destination is focused. An emptied
     * source group is removed. Outward splits of a solo tab in an edge group
     * are no-ops (they would recreate an identical layout).
     */
    splitTab(tabID: string, direction: "left" | "right"): void {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const { groups } = this.state;
        const { group, groupIndex } = loc;
        const isRight = direction === "right";
        if (isRight && groupIndex === groups.length - 1 && group.tabs.length === 1) {
            return;
        }
        if (!isRight && groupIndex === 0 && group.tabs.length === 1) {
            return;
        }
        const tab = group.tabs[loc.tabIndex];
        // Resolve the adjacent group by identity before any removal shifts the
        // indices.
        const neighbour = isRight ? groups[groupIndex + 1] : groups[groupIndex - 1];

        const next = groups.map((g) => ({ ...g, tabs: g.tabs.slice() }));
        const source = next[groupIndex];
        source.tabs.splice(loc.tabIndex, 1);
        if (source.activeTabID === tabID) {
            source.activeTabID =
                source.tabs[loc.tabIndex]?.id ?? source.tabs[loc.tabIndex - 1]?.id ?? null;
        }
        const sourceEmptied = source.tabs.length === 0;
        if (sourceEmptied) {
            next.splice(groupIndex, 1);
        }

        let targetID: string;
        if (neighbour) {
            targetID = neighbour.id;
        } else {
            targetID = newGroupID();
            const insertAt = isRight ? (sourceEmptied ? groupIndex : groupIndex + 1) : groupIndex;
            next.splice(insertAt, 0, { id: targetID, tabs: [], activeTabID: null });
        }
        const target = next.find((g) => g.id === targetID);
        if (!target) {
            return;
        }
        target.tabs.push(tab);
        target.activeTabID = tab.id;
        this.set({ groups: next, activeGroupID: targetID });
    }

    /** Split the focused group's active tab (global chord handler). */
    splitActiveTab(direction: "left" | "right"): void {
        const group = activeGroup(this.state);
        if (!group || !group.activeTabID) {
            return;
        }
        this.splitTab(group.activeTabID, direction);
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
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const groups = this.state.groups.map((g, i) =>
            i === loc.groupIndex
                ? {
                      ...g,
                      tabs: g.tabs.map((t) =>
                          t.id === tabID ? { ...t, state: "closed" as TabState, exitStatus } : t,
                      ),
                  }
                : g,
        );
        this.set({ groups });
    }

    /** Swap an optimistic temp tabID for the real tabID Connect returned. */
    replaceTab(oldID: string, newID: string): void {
        const loc = findTab(this.state, oldID);
        if (!loc) {
            return;
        }
        const groups = this.state.groups.map((g, i) => {
            if (i !== loc.groupIndex) {
                return g;
            }
            const tabs = g.tabs.map((t) => (t.id === oldID ? { ...t, id: newID } : t));
            return { ...g, tabs, activeTabID: g.activeTabID === oldID ? newID : g.activeTabID };
        });
        this.set({ groups });
    }

    /**
     * Update one tab's status (from a status event or a Connect error).
     * Status events never create tabs: the optimistic tab created by
     * connectSession is the only source, so an event for an unknown id is a
     * late event for a tab the user already closed and must be ignored (the
     * bridge writes responses and events from separate goroutines, so the
     * Disconnect response can overtake its own terminal:status "closed"
     * event). Auto-opens the SFTP panel when the active tab turns ready and
     * the setting is on — both dock sides (plan ui-ux-refinements §B).
     */
    setTabState(tabID: string, state: TabState, message?: string): void {
        const loc = findTab(this.state, tabID);
        if (!loc) {
            return;
        }
        const { settings } = this.state;
        const patch: Partial<StoreState> = {
            groups: this.state.groups.map((g, i) =>
                i === loc.groupIndex
                    ? {
                          ...g,
                          tabs: g.tabs.map((t) =>
                              t.id === tabID
                                  ? { ...t, state, errorMessage: message || undefined }
                                  : t,
                          ),
                      }
                    : g,
            ),
        };
        if (
            state === "ready" &&
            tabID === activeTabID(this.state) &&
            settings.sftpBrowserEnabled
        ) {
            patch.sftpPanelOpen = true;
        }
        this.set(patch);
    }
}

// ----------------------------------------------------------- selectors ---
//
// Pure read-only helpers over StoreState. Every consumer (shell, monitor bar,
// SFTP panel, shortcuts, terminal view) uses these instead of walking
// `groups` by hand, so the "globally active tab = focused group's active tab"
// rule lives in exactly one place.

/** Flatten every tab in group order (left group → right group). */
export function allTabs(state: StoreState): Tab[] {
    const out: Tab[] = [];
    for (const g of state.groups) {
        for (const t of g.tabs) {
            out.push(t);
        }
    }
    return out;
}

/** Locate a tab across all groups. */
export function findTab(
    state: StoreState,
    id: string,
): { group: TabGroup; groupIndex: number; tabIndex: number } | undefined {
    for (let gi = 0; gi < state.groups.length; gi++) {
        const group = state.groups[gi];
        const tabIndex = group.tabs.findIndex((t) => t.id === id);
        if (tabIndex !== -1) {
            return { group, groupIndex: gi, tabIndex };
        }
    }
    return undefined;
}

/** The focused group, if any. */
export function activeGroup(state: StoreState): TabGroup | undefined {
    return state.groups.find((g) => g.id === state.activeGroupID);
}

/** The globally active tab (the focused group's active tab). */
export function activeTab(state: StoreState): Tab | undefined {
    const group = activeGroup(state);
    if (!group || !group.activeTabID) {
        return undefined;
    }
    return group.tabs.find((t) => t.id === group.activeTabID);
}

/** The globally active tab's id (or null). */
export function activeTabID(state: StoreState): string | null {
    return activeGroup(state)?.activeTabID ?? null;
}

/** True when the active tab exists and is "ready" (reused by shell + store). */
export function hasReadyActiveTab(state: StoreState): boolean {
    const tab = activeTab(state);
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

/**
 * True when the toolbar `[SFTP]` button should be visible: the browser setting
 * is on, the active tab is ready and the panel is currently closed. Shared by
 * both dock-side layout syncs (shell.ts) so their visibility rules cannot
 * drift apart.
 */
export function sftpReopenVisible(state: StoreState): boolean {
    return state.settings.sftpBrowserEnabled && hasReadyActiveTab(state) && !state.sftpPanelOpen;
}

export const store = new Store();