// rpc/client.ts — the `/rpc` WebSocket client (phase E3; master plan §5).
//
// One text-frame WebSocket to the backend's token-gated loopback listener
// carries both directions:
//
//   * requests  {"id":n,"svc":"SessionService","method":"Tree","args":[…]}
//   * responses {"id":n,"result":…} | {"id":n,"error":"…"}
//   * events    {"event":"terminal:status","data":{…}}
//
// Requests get monotonic ids and are matched by id in a pending table. A
// dropped socket rejects every in-flight request ("bridge disconnected")
// and reconnects with backoff against the SAME endpoint (the bridge binds
// once per process, so nothing is re-provisioned). The renderer's only
// fallback byte path — terminal:data — also rides this socket as an event.
//
// Callers that arrive while the socket is down wait on a single shared
// "open gate": one connection attempt per backoff interval, one promise for
// every waiter, and a rejection (never an unbounded queue) if that attempt
// fails.
import { resolveEndpoint } from "./endpoint";
import type { BridgeEndpoint } from "./types";

const MIN_BACKOFF_MS = 250;
const MAX_BACKOFF_MS = 5000;

type Pending = {
    resolve: (value: unknown) => void;
    reject: (error: Error) => void;
};

/** The single shared wait-for-open gate (at most one per connection cycle). */
interface OpenGate {
    promise: Promise<void>;
    resolve: () => void;
    reject: (error: Error) => void;
}

/** One server→client event handler. */
export type EventHandler = (data: unknown) => void;

interface ResponseFrame {
    id?: unknown;
    result?: unknown;
    error?: unknown;
    event?: unknown;
    data?: unknown;
}

let socket: WebSocket | null = null;
let connecting = false;
let reconnectTimer: number | null = null;
let backoffMs = MIN_BACKOFF_MS;
let nextID = 1;

let gate: OpenGate | null = null;

const pending = new Map<number, Pending>();
const handlers = new Map<string, Set<EventHandler>>();

function isOpen(): boolean {
    return socket !== null && socket.readyState === WebSocket.OPEN;
}

/** Resolve or reject the shared gate (idempotent: the gate is consumed once). */
function settleGate(ok: boolean): void {
    const current = gate;
    if (!current) {
        return;
    }
    gate = null;
    if (ok) {
        current.resolve();
    } else {
        current.reject(new Error("bridge disconnected"));
    }
}

/**
 * Wait until the socket is open, driving (at most) one connection attempt.
 * Every concurrent caller shares the same gate, so a failing connect rejects
 * them together instead of accumulating waiters.
 */
function openGate(): Promise<void> {
    if (isOpen()) {
        return Promise.resolve();
    }
    if (gate) {
        return gate.promise;
    }
    let resolve!: () => void;
    let reject!: (error: Error) => void;
    const promise = new Promise<void>((res, rej) => {
        resolve = res;
        reject = rej;
    });
    gate = { promise, resolve, reject };
    connect();
    return promise;
}

/** Reject every in-flight request after a disconnect (documented behavior). */
function rejectPending(): void {
    if (pending.size === 0) {
        return;
    }
    const error = new Error("bridge disconnected");
    const inflight = Array.from(pending.values());
    pending.clear();
    for (const entry of inflight) {
        entry.reject(error);
    }
}

function dispatchEvent(name: string, data: unknown): void {
    const set = handlers.get(name);
    if (!set) {
        return;
    }
    // forEach avoids Set iteration (tsconfig has no --downlevelIteration).
    set.forEach((handler) => {
        try {
            handler(data);
        } catch (err) {
            console.error(`[rpc] handler for ${name} failed:`, err);
        }
    });
}

function handleFrame(ev: MessageEvent): void {
    if (typeof ev.data !== "string") {
        return; // /rpc is text-only
    }
    let frame: ResponseFrame;
    try {
        frame = JSON.parse(ev.data) as ResponseFrame;
    } catch {
        return; // malformed frame: no id to answer
    }
    if (typeof frame.event === "string") {
        dispatchEvent(frame.event, frame.data);
        return;
    }
    if (typeof frame.id !== "number") {
        return;
    }
    const entry = pending.get(frame.id);
    if (!entry) {
        return;
    }
    pending.delete(frame.id);
    if (typeof frame.error === "string" && frame.error.length > 0) {
        entry.reject(new Error(frame.error));
    } else {
        entry.resolve(frame.result);
    }
}

function scheduleReconnect(): void {
    if (reconnectTimer !== null) {
        return;
    }
    reconnectTimer = window.setTimeout(() => {
        reconnectTimer = null;
        connect();
    }, backoffMs);
    backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
}

/** Open one socket; resolves on open or on the first failure. */
function openSocket(ep: BridgeEndpoint): Promise<void> {
    return new Promise<void>((resolve) => {
        const url = `ws://${ep.addr}/rpc?token=${encodeURIComponent(ep.token)}`;
        let settled = false;
        const done = (): void => {
            if (!settled) {
                settled = true;
                resolve();
            }
        };
        let ws: WebSocket;
        try {
            ws = new WebSocket(url);
        } catch {
            done();
            return;
        }
        ws.onopen = () => {
            socket = ws;
            backoffMs = MIN_BACKOFF_MS;
            done();
            settleGate(true);
        };
        ws.onmessage = (ev) => handleFrame(ev);
        ws.onclose = () => {
            // A superseded socket (a newer attempt already owns `socket`) must
            // not reject requests pending on the live socket or schedule a
            // duplicate reconnect; the live socket owns that state.
            if (socket !== ws) {
                done();
                return;
            }
            socket = null;
            rejectPending();
            done();
            scheduleReconnect();
        };
        ws.onerror = () => {
            try {
                ws.close();
            } catch {
                /* already closing */
            }
            done();
        };
    });
}

/** Start (or resume) the connection. Idempotent. */
function connect(): void {
    // `reconnectTimer` also parks attempts: callers waiting during an outage
    // share the one scheduled retry instead of opening a socket per call.
    if (connecting || isOpen() || reconnectTimer !== null) {
        return;
    }
    connecting = true;
    void (async () => {
        try {
            const ep = await resolveEndpoint();
            await openSocket(ep);
        } catch {
            // resolveEndpoint never rejects; guard against unexpected throws.
        } finally {
            connecting = false;
            if (!isOpen()) {
                settleGate(false);
                scheduleReconnect();
            }
        }
    })();
}

/**
 * Resolves once the socket is open (the first time, or after a reconnect when
 * already open). Never rejects: a failed attempt simply schedules another.
 */
export function ready(): Promise<void> {
    if (isOpen()) {
        return Promise.resolve();
    }
    return openGate().then(
        () => undefined,
        () => ready(),
    );
}

/**
 * Invoke one registered service method. Resolves with the decoded result;
 * rejects with `new Error(response.error)` for a service error and
 * `new Error("bridge disconnected")` for a request whose socket (or wait for
 * a socket) failed.
 */
export function call(svc: string, method: string, args: unknown[]): Promise<unknown> {
    return openGate().then(
        () =>
            new Promise<unknown>((resolve, reject) => {
                if (!isOpen() || socket === null) {
                    reject(new Error("bridge disconnected"));
                    return;
                }
                const id = nextID++;
                pending.set(id, { resolve, reject });
                const frame = JSON.stringify({ id, svc, method, args });
                try {
                    socket.send(frame);
                } catch (err) {
                    pending.delete(id);
                    reject(err instanceof Error ? err : new Error(String(err)));
                }
            }),
    );
}

/** Subscribe to a server→client event; returns the unsubscribe function. */
export function on(event: string, handler: EventHandler): () => void {
    let set = handlers.get(event);
    if (!set) {
        set = new Set<EventHandler>();
        handlers.set(event, set);
    }
    set.add(handler);
    return () => off(event, handler);
}

/** Remove an event subscription. */
export function off(event: string, handler: EventHandler): void {
    const set = handlers.get(event);
    if (!set) {
        return;
    }
    set.delete(handler);
    if (set.size === 0) {
        handlers.delete(event);
    }
}
