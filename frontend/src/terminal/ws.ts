// terminal/ws.ts — plan P005: a WebSocket transport for terminal I/O,
// bypassing the Wails event bridge (which starves keyboard input via its
// promise-chain delivery and churns goroutines/threads under output
// floods). Browser WebSocket message handlers are ordinary macrotasks, so
// keydown stays responsive even mid-flood; input rides the same socket in
// the opposite direction.
//
// The webview page loads from the wails:// custom URI scheme, which cannot
// carry WebSockets, so the socket lives on a dedicated loopback listener
// started by the Go side (internal/termws). This module discovers its
// address with a plain GET /termws-port on the wails:// asset handler and
// connects to ws://127.0.0.1:<port>/terminal.
//
// Frame format (shared with internal/termws/frame.go):
//
//	[u8 tabIDLen][tabID ASCII][u32 payloadLen BE][payload bytes]
//
// Payloads above MAX_PAYLOAD are split into several frames by the sender.

const MAX_TABID_LEN = 64;
const MAX_PAYLOAD = 256 * 1024;

let ws: WebSocket | null = null;
let wsURL: string | null = null;
let retryTimer: number | null = null;
let resolving = false;
/** Installed by main.ts: routes decoded output frames to the term pool. */
let outputHandler: ((tabID: string, bytes: Uint8Array) => void) | null = null;

/** Set the decoded-output callback (once at boot, before connecting). */
export function setTerminalOutputHandler(fn: (tabID: string, bytes: Uint8Array) => void): void {
    outputHandler = fn;
}

function encodeFrame(tabID: string, payload: Uint8Array): Uint8Array<ArrayBuffer> {
    const id = new TextEncoder().encode(tabID);
    const out = new Uint8Array(1 + id.length + 4 + payload.length);
    out[0] = id.length;
    out.set(id, 1);
    new DataView(out.buffer).setUint32(1 + id.length, payload.length, false);
    out.set(payload, 1 + id.length + 4);
    return out;
}

function splitIntoFrames(tabID: string, payload: Uint8Array): Uint8Array<ArrayBuffer>[] {
    const frames: Uint8Array<ArrayBuffer>[] = [];
    for (let off = 0; off < payload.length; off += MAX_PAYLOAD) {
        frames.push(encodeFrame(tabID, payload.subarray(off, off + MAX_PAYLOAD)));
    }
    return frames;
}

function handleMessage(ev: MessageEvent): void {
    if (!outputHandler) {
        return;
    }
    const buf = ev.data as ArrayBuffer;
    const view = new Uint8Array(buf);
    if (view.length < 5) {
        return;
    }
    const tabLen = view[0];
    if (tabLen === 0 || tabLen > MAX_TABID_LEN) {
        return;
    }
    const id = new TextDecoder().decode(view.subarray(1, 1 + tabLen));
    const size = new DataView(buf).getUint32(1 + tabLen, false);
    const off = 1 + tabLen + 4;
    if (view.length < off + size) {
        return;
    }
    outputHandler(id, view.subarray(off, off + size));
}

/** Discover the loopback terminal WebSocket address via GET /termws-port. */
async function resolveTerminalWsUrl(): Promise<string> {
    const ctl = new AbortController();
    const timer = window.setTimeout(() => ctl.abort(), 5000);
    try {
        const resp = await fetch("/termws-port", { signal: ctl.signal });
        if (!resp.ok) {
            throw new Error(`termws-port: HTTP ${resp.status}`);
        }
        const addr = (await resp.text()).trim();
        if (!addr) {
            throw new Error("termws-port: empty address");
        }
        return `ws://${addr}/terminal`;
    } finally {
        window.clearTimeout(timer);
    }
}

function connect(): void {
    const socket = new WebSocket(wsURL!);
    socket.binaryType = "arraybuffer";
    socket.onmessage = handleMessage;
    socket.onopen = () => {
        // no-op: sendInput checks readyState; the open handoff is implicit
    };
    socket.onclose = () => {
        if (ws === socket) {
            ws = null;
        }
        scheduleRetry();
    };
    socket.onerror = () => {
        try {
            socket.close();
        } catch {
            /* already closing */
        }
    };
    ws = socket;
}

function scheduleRetry(): void {
    if (retryTimer !== null) {
        return;
    }
    retryTimer = window.setTimeout(() => {
        retryTimer = null;
        void ensureConnected();
    }, 1000);
}

/** Resolve the address if needed, then connect. Never throws. */
async function ensureConnected(): Promise<void> {
    if (resolving) {
        return;
    }
    resolving = true;
    try {
        if (wsURL === null) {
            wsURL = await resolveTerminalWsUrl();
        }
        if (isTerminalWsActive()) {
            return;
        }
        connect();
    } catch {
        wsURL = null; // re-resolve on the next retry
        scheduleRetry();
    } finally {
        resolving = false;
    }
}

/** True while the terminal socket is open. */
export function isTerminalWsActive(): boolean {
    return ws !== null && ws.readyState === WebSocket.OPEN;
}

/**
 * Send raw terminal input over the socket. Returns false when the socket is
 * not open — callers fall back to the Wails service call.
 */
export function sendInput(tabID: string, bytes: Uint8Array): boolean {
    if (!isTerminalWsActive()) {
        return false;
    }
    for (const frame of splitIntoFrames(tabID, bytes)) {
        ws!.send(frame);
    }
    return true;
}

/** Start the terminal transport (call once at boot). */
export function initTerminalWs(): void {
    void ensureConnected();
}