// rpc/index.ts — the drop-in transport surface (phase E3).
//
// Exports one typed proxy per backend service plus the event bus. The
// proxies are intentionally shaped exactly like the old generated bindings:
// `await SessionService.Tree()` still type-checks and behaves the same, so
// every call site changed only its import path (master plan §5 service
// surface; the RPC dispatcher reflects the same methods).
//
// `service<T>()` returns a Proxy whose every property is a function that
// forwards `(name, property, args)` to the RPC client; the `T` type gives
// the call sites their real signatures (Promises, DTO results).
import { call, off, on, ready } from "./client";
import type {
    CredentialDTO,
    CredentialInput,
    ImportResultDTO,
    NodeDTO,
    SavedJumpHostDTO,
    SavedJumpHostInput,
    SearchResultDTO,
    SessionDTO,
    SessionInput,
    Settings,
    SftpEntryDTO,
    VaultStatus,
} from "./types";

export interface AppServiceApi {
    GetVersion(): Promise<string>;
    GetSettings(): Promise<Settings>;
    SaveSettings(settings: Settings): Promise<void>;
}

export interface VaultServiceApi {
    Status(): Promise<VaultStatus>;
    CreateVault(password: string): Promise<void>;
    Unlock(password: string): Promise<void>;
    Lock(): Promise<void>;
    ApproveHostKey(connID: string): Promise<void>;
    RejectHostKey(connID: string): Promise<void>;
    SubmitKeyPassphrase(connID: string, passphrase: string): Promise<void>;
    SubmitKbdintResponse(connID: string, answers: string[]): Promise<void>;
    CancelKbdint(connID: string): Promise<void>;
}

export interface SessionServiceApi {
    Tree(): Promise<NodeDTO[]>;
    Search(q: string): Promise<SearchResultDTO[]>;
    CreateFolder(parentID: string, name: string): Promise<string>;
    RenameFolder(id: string, name: string): Promise<void>;
    MoveNode(id: string, newParentID: string, index: number): Promise<void>;
    DeleteNode(id: string): Promise<number>;
    CreateSession(input: SessionInput): Promise<string>;
    Session(id: string): Promise<SessionDTO>;
    UpdateSession(input: SessionInput): Promise<void>;
    DuplicateSession(id: string): Promise<string>;
    ValidateExtraArgs(extraArgs: string): Promise<void>;
    TestConnection(input: SessionInput): Promise<void>;
}

export interface CredentialServiceApi {
    List(): Promise<CredentialDTO[]>;
    Create(input: CredentialInput): Promise<string>;
    Update(input: CredentialInput): Promise<void>;
    Delete(id: string): Promise<number>;
    Get(id: string): Promise<CredentialDTO>;
    Usage(id: string): Promise<number>;
}

export interface JumpHostServiceApi {
    List(): Promise<SavedJumpHostDTO[]>;
    Create(input: SavedJumpHostInput): Promise<string>;
    Update(input: SavedJumpHostInput): Promise<void>;
    Delete(id: string): Promise<number>;
    Get(id: string): Promise<SavedJumpHostDTO>;
    Usage(id: string): Promise<number>;
}

export interface TerminalServiceApi {
    Connect(sessionID: string): Promise<string>;
    Disconnect(tabID: string): Promise<void>;
    Write(tabID: string, dataB64: string): Promise<void>;
    Resize(tabID: string, cols: number, rows: number): Promise<void>;
    Reconnect(tabID: string): Promise<void>;
}

export interface SftpServiceApi {
    IsActive(tabID: string): Promise<boolean>;
    List(tabID: string, path: string): Promise<SftpEntryDTO[]>;
    Mkdir(tabID: string, path: string): Promise<void>;
    Rename(tabID: string, from: string, to: string): Promise<void>;
    Remove(tabID: string, path: string): Promise<void>;
    PickLocalFiles(tabID: string, multi: boolean): Promise<string[]>;
    Upload(tabID: string, localPaths: string[], remoteDir: string): Promise<void>;
    Download(tabID: string, remotePath: string): Promise<string>;
    DownloadThenSave(tabID: string, remotePath: string): Promise<string>;
    EditRemoteText(tabID: string, remotePath: string): Promise<void>;
    CancelEdit(tabID: string): Promise<void>;
}

export interface MonitorServiceApi {
    Start(tabID: string): Promise<void>;
    Stop(tabID: string): Promise<void>;
}

export interface TransferServiceApi {
    Export(path: string, password: string): Promise<void>;
    Import(path: string, password: string, mode: "merge" | "replace"): Promise<ImportResultDTO>;
}

/**
 * Build a service proxy. The target is empty; every property read yields a
 * forwarding function, so `Service.Method(...)` maps to
 * `call("Service", "Method", [...])`. `then`/`constructor`/`__esModule`
 * stay undefined so the proxy is never mistaken for a thenable.
 */
function service<T extends object>(name: string): T {
    return new Proxy({} as T, {
        get(_target, prop) {
            if (typeof prop !== "string" || prop === "then" || prop === "constructor" || prop === "__esModule") {
                return undefined;
            }
            return (...args: unknown[]): Promise<unknown> => call(name, prop, args);
        },
    });
}

export const AppService = service<AppServiceApi>("AppService");
export const VaultService = service<VaultServiceApi>("VaultService");
export const SessionService = service<SessionServiceApi>("SessionService");
export const CredentialService = service<CredentialServiceApi>("CredentialService");
export const JumpHostService = service<JumpHostServiceApi>("JumpHostService");
export const TerminalService = service<TerminalServiceApi>("TerminalService");
export const SftpService = service<SftpServiceApi>("SftpService");
export const MonitorService = service<MonitorServiceApi>("MonitorService");
export const TransferService = service<TransferServiceApi>("TransferService");

/** The renderer's sole event bus (main.ts is the single subscriber owner). */
export const events = { on, off, ready };
