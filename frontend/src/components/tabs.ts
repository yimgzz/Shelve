// components/tabs.ts — tab strip (Phase 4b task 4 + Phase 4c; master
// plan §6 Tabs). Renders store.tabs: session name, status dot (connecting
// amber / ready green / error red / closed gray), × close on hover,
// middle-click close, click activate, horizontal scroll. The right pane is
// owned by components/terminal-view.ts (Phase 4c).

import { store } from "../store";

let stripHost: HTMLElement | null = null;
let stripUnsub: (() => void) | null = null;

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