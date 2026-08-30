// components/tabs.ts — tab strip + right-pane placeholder (Phase 4b
// task 4; master plan §6 Tabs/Terminal). The strip renders store.tabs:
// session name, status dot (connecting amber / ready green / error red /
// closed gray), × close on hover, middle-click close, click activate,
// horizontal scroll. The right pane (4b version) is a STABLE #terminal-pane
// wrapper holding a placeholder card; 4c mounts xterm.js inside it.

import { store } from "../store";

let stripHost: HTMLElement | null = null;
let stripUnsub: (() => void) | null = null;

let paneHost: HTMLElement | null = null;
let paneUnsub: (() => void) | null = null;

/** Render the tab strip into host (subscribes to store.tabs). */
export function renderTabStrip(host: HTMLElement): void {
    stripHost = host;
    if (stripUnsub) {
        stripUnsub();
    }
    stripUnsub = store.subscribe(() => renderStrip());
    renderStrip();
}

function renderStrip(): void {
    if (!stripHost) {
        return;
    }
    const { tabs, activeTabID } = store.getState();
    stripHost.textContent = "";

    const strip = document.createElement("div");
    strip.className = "tab-strip";

    for (const tab of tabs) {
        const el = document.createElement("div");
        el.className = `tab${tab.id === activeTabID ? " active" : ""}`;
        el.dataset.tabId = tab.id;

        const dot = document.createElement("span");
        dot.className = `dot ${tab.state}`;
        const label = document.createElement("span");
        label.className = "tab-label";
        label.textContent = tab.session.name;
        label.title = tab.session.name;

        const close = document.createElement("button");
        close.type = "button";
        close.className = "tab-close";
        close.textContent = "×";
        close.title = "Close";
        close.addEventListener("click", (e) => {
            e.stopPropagation();
            void store.closeTab(tab.id);
        });

        el.addEventListener("click", () => store.activateTab(tab.id));
        el.addEventListener("auxclick", (e) => {
            if (e.button === 1) {
                e.preventDefault();
                void store.closeTab(tab.id);
            }
        });

        el.append(dot, label, close);
        strip.appendChild(el);

        if (tab.id === activeTabID) {
            el.scrollIntoView({ block: "nearest", inline: "nearest" });
        }
    }

    stripHost.appendChild(strip);
}

/** Render the right-pane placeholder into host (subscribes to tabs). */
export function renderTerminalPane(host: HTMLElement): void {
    paneHost = host;
    if (!paneHost.querySelector("#terminal-pane")) {
        const pane = document.createElement("div");
        pane.id = "terminal-pane";
        paneHost.appendChild(pane);
    }
    if (paneUnsub) {
        paneUnsub();
    }
    paneUnsub = store.subscribe(() => renderPane());
    renderPane();
}

function renderPane(): void {
    if (!paneHost) {
        return;
    }
    const pane = paneHost.querySelector<HTMLElement>("#terminal-pane")!;
    pane.textContent = "";

    const { tabs, activeTabID } = store.getState();
    const tab = tabs.find((t) => t.id === activeTabID) || null;

    if (!tab) {
        const empty = document.createElement("div");
        empty.className = "empty-state";
        const t = document.createElement("div");
        t.textContent = "No open sessions";
        const hint = document.createElement("div");
        hint.className = "hint";
        hint.textContent = "Double-click a session to open a terminal here.";
        empty.append(t, hint);
        pane.appendChild(empty);
        return;
    }

    const card = document.createElement("div");
    card.className = "terminal-placeholder";

    const label = document.createElement("div");
    label.className = "tp-label";
    const dot = document.createElement("span");
    dot.className = `dot ${tab.state}`;
    const name = document.createElement("span");
    name.textContent = tab.session.name;
    name.title = `${tab.session.user}@${tab.session.host}:${tab.session.port}`;
    label.append(dot, name);

    const stateLine = document.createElement("div");
    stateLine.className = "tp-state";
    stateLine.textContent =
        tab.state === "error" ? tab.errorMessage || "Connection error" : `State: ${tab.state}`;
    if (tab.state === "error") {
        stateLine.classList.add("error");
    }

    const closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "btn small";
    closeBtn.textContent = "Close";
    closeBtn.addEventListener("click", () => void store.closeTab(tab.id));

    card.append(label, stateLine, closeBtn);
    pane.appendChild(card);
}