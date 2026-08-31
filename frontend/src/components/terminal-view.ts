// components/terminal-view.ts — real per-tab xterm panes (Phase 4c task 3;
// master plan §6 Terminal). Replaces the 4b placeholder card INSIDE
// #terminal-pane: one .term-pane per tab (`display:none` unless active);
// switching tabs shows + fits + focuses the active pane. State overlays
// (connecting / error / exit) are centered cards over the pane with real
// buttons ([Retry] → Reconnect, [Close tab]).

import { store } from "../store";
import type { Tab } from "../store";
import { TermPool } from "../terminal/xterm";
import { TerminalService } from "../../bindings/dummy-ssh-manager/internal/wailsvc";

let paneHost: HTMLElement | null = null;
let paneEl: HTMLElement | null = null;
let unsub: (() => void) | null = null;
let lastActive: string | null = null;

interface Pane {
    el: HTMLElement;
    xtermEl: HTMLElement;
    overlay: HTMLElement;
}

/** tabID → pane element. Pool entries are tracked separately in TermPool. */
const panes = new Map<string, Pane>();

function buildPane(tab: Tab): Pane {
    const el = document.createElement("div");
    el.className = "term-pane";
    el.dataset.tabId = tab.id;

    const xtermEl = document.createElement("div");
    xtermEl.className = "term-xterm";

    const overlay = document.createElement("div");
    overlay.className = "term-overlay";
    overlay.style.display = "none";

    el.append(xtermEl, overlay);
    return { el, xtermEl, overlay };
}

function button(label: string, className: string, onClick: () => void): HTMLButtonElement {
    const b = document.createElement("button");
    b.type = "button";
    b.className = `btn small ${className}`.trim();
    b.textContent = label;
    b.addEventListener("click", onClick);
    return b;
}

/** [Retry] re-dials under the same tabID and clears the terminal buffer. */
async function retryTab(tabID: string): Promise<void> {
    store.setTabState(tabID, "connecting");
    TermPool.clear(tabID);
    try {
        await TerminalService.Reconnect(tabID);
    } catch (err) {
        store.setTabState(tabID, "error", String(err));
    }
}

function renderOverlay(overlay: HTMLElement, tab: Tab): void {
    overlay.textContent = "";
    if (tab.state === "ready") {
        overlay.style.display = "none";
        return;
    }
    overlay.style.display = "flex";

    const card = document.createElement("div");
    card.className = "term-overlay-card";

    if (tab.state === "connecting") {
        const spinner = document.createElement("span");
        spinner.className = "spinner";
        const msg = document.createElement("div");
        msg.textContent = "Connecting to host…";
        card.append(spinner, msg);
    } else if (tab.state === "error") {
        const title = document.createElement("div");
        title.className = "to-title";
        title.textContent = "Connection error";
        const msg = document.createElement("div");
        msg.className = "to-msg";
        msg.textContent = tab.errorMessage || "Failed to connect to the remote host.";
        const actions = document.createElement("div");
        actions.className = "to-actions";
        actions.append(
            button("Retry", "primary", () => void retryTab(tab.id)),
            button("Close tab", "", () => void store.closeTab(tab.id)),
        );
        card.append(title, msg, actions);
    } else if (tab.state === "closed") {
        const title = document.createElement("div");
        title.className = "to-title";
        title.textContent = "Connection closed";
        const msg = document.createElement("div");
        msg.className = "to-msg";
        msg.textContent =
            tab.exitStatus !== undefined && tab.exitStatus !== null
                ? `The remote session ended (exit code ${tab.exitStatus}).`
                : "The remote session ended.";
        const actions = document.createElement("div");
        actions.className = "to-actions";
        actions.append(
            button("Retry", "primary", () => void retryTab(tab.id)),
            button("Close tab", "", () => void store.closeTab(tab.id)),
        );
        card.append(title, msg, actions);
    }

    overlay.appendChild(card);
}

function showEmptyState(): void {
    if (!paneEl) {
        return;
    }
    const empty = document.createElement("div");
    empty.className = "empty-state";
    const t = document.createElement("div");
    t.textContent = "No open sessions";
    const hint = document.createElement("div");
    hint.className = "hint";
    hint.textContent = "Double-click a session to open a terminal here.";
    empty.append(t, hint);
    paneEl.appendChild(empty);
}

function reconcile(): void {
    if (!paneEl) {
        return;
    }
    const { tabs, activeTabID, settings } = store.getState();

    // Drop any "No open sessions" placeholder; re-added below when needed.
    paneEl.querySelector(".empty-state")?.remove();

    // Remove panes (and tear down their pooled terminals) only for tabs that
    // no longer exist. Existing panes stay attached in the DOM — they must
    // NOT be wiped here, or the persisted xterm viewport vanishes.
    const ids = new Set(tabs.map((t) => t.id));
    for (const id of Array.from(panes.keys())) {
        if (!ids.has(id)) {
            TermPool.destroy(id);
            panes.get(id)?.el.remove();
            panes.delete(id);
        }
    }

    if (tabs.length === 0) {
        lastActive = null;
        showEmptyState();
        return;
    }

    // Create panes + pooled terminals for NEW tabs. Existing panes are
    // already in the DOM and are skipped here.
    for (const tab of tabs) {
        if (panes.has(tab.id)) {
            continue;
        }
        const p = buildPane(tab);
        paneEl.appendChild(p.el);
        panes.set(tab.id, p);
        TermPool.create(tab.id, p.xtermEl, { settings: settings.terminal });
    }

    // Visibility + overlays.
    for (const tab of tabs) {
        const p = panes.get(tab.id);
        if (!p) {
            continue;
        }
        p.el.style.display = tab.id === activeTabID ? "flex" : "none";
        renderOverlay(p.overlay, tab);
    }

    // Focus/fit the newly-activated tab (only when the activation changed).
    const active = tabs.find((t) => t.id === activeTabID);
    if (active && active.id !== lastActive && active.state !== "error" && active.state !== "closed") {
        TermPool.activate(active.id);
    }
    lastActive = active ? active.id : null;
}

/**
 * Mount the terminal view into `host` (the .terminal-pane-host wrapper).
 * Subscribes to the store; panes and pooled terminals persist across
 * renders and are torn down only when a tab closes or the vault locks.
 */
export function renderTerminalView(host: HTMLElement): void {
    paneHost = host;
    if (!paneHost.querySelector("#terminal-pane")) {
        const pane = document.createElement("div");
        pane.id = "terminal-pane";
        paneHost.appendChild(pane);
    }
    paneEl = paneHost.querySelector<HTMLElement>("#terminal-pane")!;
    if (unsub) {
        unsub();
    }
    unsub = store.subscribe(reconcile);
    reconcile();
}