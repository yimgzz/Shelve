// rpc/types.ts — the transport's typed contract (phase E3).
//
// Two kinds of mirrors live here, both derived from the Go side so the wire
// shape cannot drift silently:
//
//   * the JSON DTOs of `internal/api/dto.go` and the `internal/config`
//     settings schema (field names exactly as the Go json tags);
//   * the reviewed native surface `electron/preload.ts` exposes on
//     `window.shelve` (master plan §8.11: minimal, secret-free, no Node).
//
// This module is types only; it emits no runtime code beyond the ambient
// `Window.shelve` declaration.

// The IPC payloads are declared once for all three processes in ./ipc.
import type { BridgeEndpoint, DisplayChanged, WindowState } from "./ipc";
export type { BridgeEndpoint, DisplayChanged, WindowState };

// ---------------------------------------------------------------- DTOs ---

/** model.AuthType: AuthPassword = 0, AuthKey = 1. */
export type AuthType = number;

/** Session-tree node (NodeDTO). */
export interface NodeDTO {
    kind: "folder" | "session";
    id: string;
    name: string;
    children: NodeDTO[];
}

/** One flat live-search result (SearchResultDTO, master plan §2 A9). */
export interface SearchResultDTO {
    id: string;
    name: string;
    host: string;
    user: string;
    folderPath: string;
}

/** Jump-host read view (JumpHostDTO): never carries the password. */
export interface JumpHostDTO {
    host: string;
    port: number;
    user: string;
    authType: AuthType;
    hasPassword: boolean;
    keyPath?: string;
    /** Bastion-style hop (plan P009): last handshake, target-embedding. */
    bastion?: boolean;
}

/** Jump-host write view (JumpHostInput). */
export interface JumpHostInput {
    host: string;
    port: number;
    user: string;
    authType: AuthType;
    password?: string;
    keyPath?: string;
    bastion?: boolean;
}

/** Session read view (SessionDTO): secret-free. */
export interface SessionDTO {
    id: string;
    folderId: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: AuthType;
    hasPassword: boolean;
    keyPath?: string;
    jumpHosts: JumpHostDTO[];
    extraArgs: string;
    sftpInitialPath?: string;
    /** Reference to a named credential (plan P003); "" = none. */
    credentialId?: string;
    /** Reference to a saved jump host (plan P006); "" = none. */
    jumpHostRef?: string;
}

/** Session write draft (SessionInput): the password only flows INTO the vault. */
export interface SessionInput {
    id?: string;
    folderId: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: AuthType;
    password?: string;
    keyPath?: string;
    jumpHosts: JumpHostInput[];
    extraArgs: string;
    sftpInitialPath?: string;
    credentialId?: string;
    jumpHostRef?: string;
}

/** Credential read view (CredentialDTO): secret-free. */
export interface CredentialDTO {
    id: string;
    name: string;
    user: string;
    authType: AuthType;
    hasPassword: boolean;
    keyPath?: string;
}

/** Credential write draft (CredentialInput). */
export interface CredentialInput {
    id?: string;
    name: string;
    user: string;
    authType: AuthType;
    password?: string;
    keyPath?: string;
}

/** Saved-jump-host read view (SavedJumpHostDTO): secret-free. */
export interface SavedJumpHostDTO {
    id: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: AuthType;
    hasPassword: boolean;
    keyPath?: string;
    bastion?: boolean;
}

/** Saved-jump-host write draft (SavedJumpHostInput). */
export interface SavedJumpHostInput {
    id?: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: AuthType;
    password?: string;
    keyPath?: string;
    bastion?: boolean;
}

/** One SFTP listing row (SftpEntryDTO; modTime is RFC3339). */
export interface SftpEntryDTO {
    name: string;
    isDir: boolean;
    size: number;
    modTime: string;
    textLike: boolean;
}

/** Vault state machine (VaultStatusDTO). */
export interface VaultStatus {
    state: "create" | "locked" | "unlocked";
    unlocked: boolean;
}

/** Result of a configuration import (ImportResultDTO, secret-free counts). */
export interface ImportResultDTO {
    mode: "merge" | "replace";
    folders: number;
    sessions: number;
    credentials: number;
    savedJumpHosts: number;
    remappedReferences: number;
}

// ------------------------------------------------------------ settings ---

/** Terminal appearance/behaviour (config.TerminalSettings). */
export interface TerminalSettings {
    fontFamily: string;
    fontSize: number;
    scrollback: number;
}

/** Remembered main-window geometry (config.WindowSettings, master plan A7). */
export interface WindowSettings {
    width: number;
    height: number;
    leftWidth: number;
    sftpWidth: number;
}

/**
 * Renderer-side UI preferences (config.UISettings, E4 T7). `zoomLevel` is the
 * VSCode zoom model — user zoom, separate from the OS device scale — applied
 * via `window.shelve.zoom.setLevel`. 0 = no zoom (the default).
 */
export interface UISettings {
    zoomLevel: number;
}

/** settings.json schema (config.Settings): never contains secrets. */
export interface Settings {
    theme: string;
    themeVariant: string;
    autoLockMinutes: number;
    sftpBrowserEnabled: boolean;
    /** Dock side of the SFTP browser (config.SftpPanelSide). */
    sftpPanelSide: SftpPanelSide;
    monitoringEnabled: boolean;
    terminal: TerminalSettings;
    textEditorCommand: string;
    sftpInitialPath: string;
    window: WindowSettings;
    ui: UISettings;
}

/** SFTP browser dock side (config.SftpPanelSide). */
export type SftpPanelSide = "left" | "right";

// ------------------------------------------------- native (preload) API ---

/**
 * The complete `window.shelve` surface (electron/preload.ts). Reviewed
 * against master plan §8.11: no secrets, no Node objects, no arbitrary IPC.
 */
export interface ShelveNative {
    /** Resolves the backend `{addr, token}`; rejects before the handshake. */
    bridgeEndpoint(): Promise<BridgeEndpoint>;
    clipboard: {
        readText(): Promise<string>;
        writeText(text: string): Promise<void>;
    };
    /** Native single-file picker; resolves "" when cancelled. */
    pickFile(): Promise<string>;
    /** Native save dialog for configuration export; resolves "" when cancelled. */
    pickSaveFile(defaultName: string): Promise<string>;
    /** Native open dialog for configuration import; resolves "" when cancelled. */
    pickOpenFile(): Promise<string>;
    /** Window geometry changes (debounced in main); returns an unsubscribe. */
    windowState: {
        onChange(cb: (state: WindowState) => void): () => void;
    };
    /** Display/DPI changes (debounced in main); returns an unsubscribe. */
    display: {
        onChange(cb: (state: DisplayChanged) => void): () => void;
    };
    /** User zoom, separate from OS DPI (E4 T7: 1.2 ** level). */
    zoom: {
        setLevel(level: number): void;
        getLevel(): number;
    };
    /**
     * Custom (frameless) title bar controls. `frameless` is false when the app
     * runs with SHELVE_TITLEBAR=native (the OS frame is drawn instead).
     */
    titleBar: {
        frameless: boolean;
        minimize(): void;
        toggleMaximize(): void;
        close(): void;
    };
}

declare global {
    interface Window {
        shelve: ShelveNative;
    }
}
