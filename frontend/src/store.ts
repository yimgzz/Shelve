// store.ts — tiny typed pub/sub store (master plan §5).
//
// RULE: components subscribe to the store; they never call
// `Events.On` directly. main.ts is the single owner of the Wails event
// bus and routes every event here via set(). This keeps one source of
// truth and prevents listener growth across lock/unlock cycles.

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
    theme: string; // "system" | "light" | "dark"
    autoLockMinutes: number;
    sftpBrowserEnabled: boolean;
    terminal: TerminalSettings;
    textEditorCommand: string;
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
}

export interface Tab {
    id: string;
    session: SessionDTO;
    state: TabState;
    errorMessage?: string;
}

export interface StoreState {
    settings: Settings;
    vaultState: VaultState;
    tree: NodeDTO[];
    selectedID: string | null;
    searchQ: string;
    tabs: Tab[];
    activeTabID: string | null;
    leftPanelWidth: number;
}

export const DEFAULT_LEFT_WIDTH = 320;

export const initialState: StoreState = {
    settings: {
        theme: "system",
        autoLockMinutes: 0,
        sftpBrowserEnabled: false,
        terminal: { fontFamily: "monospace", fontSize: 13, scrollback: 10000 },
        textEditorCommand: "xdg-open",
        window: { width: 1280, height: 800, leftWidth: DEFAULT_LEFT_WIDTH },
    },
    vaultState: "locked",
    tree: [],
    selectedID: null,
    searchQ: "",
    tabs: [],
    activeTabID: null,
    leftPanelWidth: DEFAULT_LEFT_WIDTH,
};

type Listener = (state: StoreState) => void;

class Store {
    private state: StoreState = initialState;
    private listeners = new Set<Listener>();

    getState(): StoreState {
        return this.state;
    }

    /** Merge a partial update and notify subscribers (new object identity). */
    set(partial: Partial<StoreState>): void {
        this.state = { ...this.state, ...partial };
        for (const fn of this.listeners) {
            fn(this.state);
        }
    }

    subscribe(fn: Listener): () => void {
        this.listeners.add(fn);
        return () => {
            this.listeners.delete(fn);
        };
    }
}

export const store = new Store();