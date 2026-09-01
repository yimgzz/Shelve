// terminal/ws.ts — plan P005: same-origin WebSocket transport for terminal
// I/O, bypassing the Wails event bridge (which starves keyboard input via
// its promise-chain delivery and churns goroutines/threads under output
// floods). Browser WebSocket message handlers are ordinary macrotasks, so
// keydown stays responsive even mid-flood; input rides the same socket in
// the opposite direction.
//
// Frame format (shared with internal/termws/frame.go):
//
//	[u8 tabIDLen][tabID ASCII][u32 payloadLen BE][payload bytes]
//
// The socket lives on the app's own HTTP transport at /terminal, so the URL
// is derived from window.location — no port discovery, no mixed content.

const MAX_TABID_LEN = 64;

let ws: WebSocket | null = null;
let retryTimer: number | null = null;
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

function connect(): void {
    const proto = window.location.protocol === "https:" ? "wss://" : "ws://";
    const socket = new WebSocket(`${proto}${window.location.host}/terminal`);
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
        connect();
    }, 1000);
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
    if (isTerminalWsActive()) {
        ws!.send(encodeFrame(tabID, bytes));
        return true;
    }
    return false;
}

/** Start the terminal transport (call once at boot). */
export function initTerminalWs(): void {
    connect();
}