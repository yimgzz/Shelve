// components/statusbar.ts — bottom status bar (Phase 4c task 4; master
// plan §6 Layout). For the active tab it renders `user@host:port`,
// `, via <jump1>[, <jump2>]` from the tab's session snapshot, and a port
// forward summary built from cached ssh:forward events. A failed forward
// renders dim-red with its spec. No active tab → empty.

import { store } from "../store";
import type { ForwardDTO } from "../store";

let host: HTMLElement | null = null;
let unsub: (() => void) | null = null;

/**
 * Render a forward spec to a compact label. The engine's spec is
 * "bind:localPort" for -D and "bind:localPort:dstHost:dstPort" for -L
 * (see sshengine/forwardSpec). We turn those into e.g. "D 1080" and
 * "L 8080→db:5432".
 */
function formatForward(f: ForwardDTO): string {
    const m = /^(.+?):(\d+)(?::([^:]+):(\d+))?$/.exec(f.spec);
    if (!m) {
        return f.spec;
    }
    const localPort = m[2];
    if (m[3] !== undefined && m[4] !== undefined) {
        return `L ${localPort}→${m[3]}:${m[4]}`;
    }
    return `D ${localPort}`;
}

function render(): void {
    const el = host;
    if (!el) {
        return;
    }
    el.textContent = "";
    const { tabs, activeTabID, forwards } = store.getState();
    const tab = tabs.find((t) => t.id === activeTabID);
    if (!tab) {
        return;
    }

    const conn = `${tab.session.user}@${tab.session.host}:${tab.session.port}`;
    const connText = document.createElement("span");
    connText.textContent =
        tab.session.jumpHosts.length > 0
            ? `${conn}, via ${tab.session.jumpHosts.map((j) => `${j.user}@${j.host}`).join(", ")}`
            : conn;
    el.appendChild(connText);

    const liveForwards = (forwards[tab.id] || []).filter((f) => f.state !== "closed");
    if (liveForwards.length === 0) {
        return;
    }

    const firstSep = document.createElement("span");
    firstSep.textContent = " · ";
    el.appendChild(firstSep);

    liveForwards.forEach((f, i) => {
        if (i > 0) {
            const sep = document.createElement("span");
            sep.textContent = " · ";
            el.appendChild(sep);
        }
        const span = document.createElement("span");
        span.textContent = formatForward(f);
        if (f.state === "failed") {
            span.className = "fwd failed";
            span.title = f.error || f.spec;
        } else {
            span.className = "fwd";
        }
        el.appendChild(span);
    });
}

/** Mount the status bar into `el` (the .status-band footer) and subscribe. */
export function renderStatusBar(el: HTMLElement): void {
    host = el;
    if (unsub) {
        unsub();
    }
    unsub = store.subscribe(render);
    render();
}