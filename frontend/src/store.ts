// store.ts — tiny typed pub/sub store (master plan §5).
//
// RULE: components subscribe to the store; they never call
// `Events.On` directly. main.ts is the single owner of the Wails event
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
// closeTab): those bridge the Wails service bindings and the reactive
// state in one place so components stay presentation-only.

import { CredentialService, SessionService, TerminalService } from "../bindings/shelve/internal/wailsvc";
import { toast } from "./components/toasts";

export type VaultState = "create" | "locked" | "unlocked";
export type TabState = "connecting" | "ready" | "error" | "closed";

export interface WindowSettings {
    width: number;
    height: number;
    leftWidth: number;
}

export interface TerminalSettings {
    fontFamily: string;
    fontSize: number;
    scrollback: number;
}

export interface Settings {
   theme: string; // "system" | "light" | "dark" — the mode
   /** Concrete palette id ("" = family default). Plan P001. */
   themeVariant: string;
   autoLockMinutes: number;
    sftpBrowserEnabled: boolean;
    terminal: TerminalSettings;
    textEditorCommand: string;
    /** Global SFTP browser start path ("~" default). Plan P002. */
    sftpInitialPath: string;
    /** Local handler command for the SFTP "Open" action (xdg-open default). Plan P002. */
    sftpOpenCommand: string;
    window: WindowSettings;
}

/** Session-tree node (secret-free read view, master plan §5). */
export interface NodeDTO {
    kind: "folder" | "session";
    id: string;
    name: string;
    children: NodeDTO[];
}

/** Jump host read view. */
export interface JumpHostDTO {
    host: string;
    port: number;
    user: string;
    authType: number;
    hasPassword: boolean;
    keyPath?: string;
}

/** Session read view snapshot (stored per tab). */
export interface SessionDTO {
    id: string;
    folderId: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: number;
    hasPassword: boolean;
    keyPath?: string;
    jumpHosts: JumpHostDTO[];
    extraArgs: string;
    /** Per-session SFTP browser start path ("" = global default). Plan P002. */
    sftpInitialPath?: string;
    /**
     * Optional reference to a named credential (plan P003). While set,
     * the backend resolves User+Auth from the credential at connect/test
     * time; the inline fields remain the fallback snapshot.
     */
    credentialId?: string;
}

/** Saved-credential read view (secret-free, plan P003 §4.3). */
export interface CredentialDTO {
    id: string;
    name: string;
    user: string;
    authType: number;
    hasPassword: boolean;
    keyPath?: string;
}

/** Saved-credential write draft (password only flows INTO the vault). */
export interface CredentialInput {
    id?: string;
    name: string;
    user: string;
    authType: number;
    password?: string;
    keyPath?: string;
}

/** One flat live-search result (master plan §2 A9). */
export interface SearchResultDTO {
    id: string;
    name: string;
    host: string;
    user: string;
    folderPath: string;
}

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

/** One SFTP listing row (mirrors wailsvc.SftpEntryDTO; master plan §5). */
export interface SftpEntryDTO {
    name: string;
    isDir: boolean;
    size: number;
    /** RFC3339 string (Go time.Time over the wire). */
    modTime: string;
    textLike: boolean;
}

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

export interface StoreState {
    settings: Settings;
    vaultState: VaultState;
    /**
     * Left-panel mode (phase 5d D5d-3): "tree" shows the session list,
     * "sftp" the SFTP browser. Defaults to "tree"; the store auto-switches
     * to "sftp" when a ready tab becomes active (D5d-1).
     */
    leftMode: "tree" | "sftp";
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
     * tabID → ordered list of transfer snapshots (sftp:progress events,
     * Phase 5c). The SFTP panel footer derives its progress line from this
     * cache. Never persisted.
     */
    sftpTransfers: Record<string, SftpTransfer[]>;
    /**
     * tabID → session snapshot for tabs whose `terminal:status` event may
     * arrive before the optimistic tab is reconciled (Phase 4b task 4).
     * Never persisted.
     */
    pendingSessions: Record<string, SessionDTO>;
    /**
     * tabID → port-forward lifecycle cache (ssh:forward events, Phase 4c),
     * used by the status bar. One entry per unique spec (latest state wins).
     * Never persisted.
     */
    forwards: Record<string, ForwardDTO[]>;
}

export const DEFAULT_LEFT_WIDTH = 320;

export const initialState: StoreState = {
    settings: {
        theme: "system",
        themeVariant: "",
        autoLockMinutes: 0,
        // Phase 5d: the SFTP browser is on by default (mirrors the backend
        // default; AppService.GetSettings() at boot overrides with the
        // persisted value via toSettings).
        sftpBrowserEnabled: true,
        terminal: { fontFamily: "monospace", fontSize: 13, scrollback: 10000 },
        textEditorCommand: "xdg-open",
        sftpInitialPath: "~",
        sftpOpenCommand: "xdg-open",
        window: { width: 1280, height: 800, leftWidth: DEFAULT_LEFT_WIDTH },
    },
    vaultState: "locked",
    leftMode: "tree",
    tree: [],
    selectedID: null,
    searchQ: "",
    tabs: [],
    activeTabID: null,
    leftPanelWidth: DEFAULT_LEFT_WIDTH,
    credentials: [],
    pendingSessions: {},
    forwards: {},
    sftpTransfers: {},
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
            const tree = (await SessionService.Tree()) as unknown as NodeDTO[];
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
            const credentials = (await CredentialService.List()) as unknown as CredentialDTO[];
            this.set({ credentials });
        } catch (err) {
            toast("error", String(err));
        }
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
            dto = (await SessionService.Session(sessionID)) as unknown as SessionDTO;
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
            this.registerPendingSession(tabID, dto);
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
        // Auto-switch to the SFTP panel when activating a ready tab (D5d-1).
        if (tab && tab.state === "ready" && settings.sftpBrowserEnabled) {
            patch.leftMode = "sftp";
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
        this.set({ tabs: remaining, activeTabID: nextActive, forwards, sftpTransfers });
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

    /** Remember a real tabID→session so late status events can create it. */
    registerPendingSession(tabID: string, dto: SessionDTO): void {
        this.set({ pendingSessions: { ...this.state.pendingSessions, [tabID]: dto } });
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
     * Creates the tab on demand from the pending-session cache when a
     * `terminal:status` event beats the optimistic-tab reconciliation
     * (Phase 4b task 4). Auto-switches the left panel to the SFTP browser
     * when the active tab turns ready (phase 5d D5d-1).
     */
    setTabState(tabID: string, state: TabState, message?: string): void {
        const { tabs, activeTabID, settings, pendingSessions } = this.state;
        const patch: Partial<StoreState> = {};
        if (tabs.some((t) => t.id === tabID)) {
            patch.tabs = tabs.map((t) =>
                t.id === tabID ? { ...t, state, errorMessage: message || undefined } : t,
            );
        } else {
            const sess = pendingSessions[tabID];
            if (!sess) {
                return;
            }
            patch.tabs = [
                ...tabs,
                { id: tabID, session: sess, state, errorMessage: message || undefined },
            ];
        }
        if (state === "ready" && tabID === activeTabID && settings.sftpBrowserEnabled) {
            patch.leftMode = "sftp";
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
 * Single source of truth for whether the SFTP panel replaces the tree
 * (master plan §6, phases 5c/5d): the setting must be on AND leftMode must
 * be "sftp" AND the active tab must be ready. Otherwise the tree shows
 * (with a hint when the setting is on but no active ready tab exists).
 */
export function sftpPanelVisible(state: StoreState): boolean {
    if (!state.settings.sftpBrowserEnabled || state.leftMode !== "sftp") {
        return false;
    }
    return hasReadyActiveTab(state);
}

export const store = new Store();