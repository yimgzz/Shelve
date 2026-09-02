// components/tabs.ts — tab strip (Phase 4b task 4 + Phase 4c; master
// plan §6 Tabs). Renders store.tabs: session name, status dot (connecting
// amber / ready green / error red / closed gray), × close on hover,
// middle-click close, click activate, horizontal scroll. Right-click opens
// a batch-close context menu (Close Others / Close All Tabs / Close Tabs
// to the Right); left-drag reorders the strip (pointer-based; HTML5 DnD is
// unreliable in WebKitGTK). The right pane is owned by
// components/terminal-view.ts (Phase 4c).

import { store } from "../store";
import { openContextMenu, type MenuItem } from "./context-menu";

let stripHost: HTMLElement | null = null;
let stripUnsub: (() => void) | null = null;
/**
 * Snapshot identity of everything renderStrip draws (tab id/state/name and
 * the active id). The strip is rebuilt ONLY when this changes: unrelated
 * store churn — most notably the every-2 s monitor:metrics updates — must
 * never replace the .tab nodes, or a click split across the rebuild would be
 * swallowed (mousedown and mouseup land on different elements, so no click
 * event fires and the tab appears non-clickable).
 */
let lastKey = "";

function snapshotKey(): string {
    const { tabs, activeTabID } = store.getState();
    let key = `${activeTabID ?? ""}|`;
    for (const t of tabs) {
        key += `${t.id}:${t.state}:${t.session.name};`;
    }
    return key;
}

// ------------------------------------------------------------ tab menu ---

/** Right-click menu on a tab: batch close actions (English labels). */
function openTabContextMenu(x: number, y: number, tabID: string): void {
    const { tabs } = store.getState();
    const idx = tabs.findIndex((t) => t.id === tabID);
    if (idx === -1) {
        return;
    }
    const items: MenuItem[] = [
        {
            label: "Close Others",
            disabled: tabs.length <= 1,
            action: () => void store.closeOtherTabs(tabID),
        },
        {
            label: "Close All Tabs",
            disabled: tabs.length === 0,
            action: () => void store.closeAllTabs(),
        },
        {
            label: "Close Tabs to the Right",
            disabled: idx === tabs.length - 1,
            action: () => void store.closeTabsToRight(tabID),
        },
    ];
    openContextMenu(x, y, items);
}

// -------------------------------------------------------- tab dragging ---

interface TabDrag {
    tabID: string;
    pointerId: number;
    startX: number;
    startY: number;
    started: boolean;
}

/** Active tab drag (pointer-based reorder; see header comment). */
let tabDrag: TabDrag | null = null;

const DRAG_THRESHOLD_PX = 6;

function dragTabEl(tabID: string): HTMLElement | null {
    if (!stripHost) {
        return null;
    }
    return stripHost.querySelector<HTMLElement>(`.tab[data-tab-id="${tabID}"]`);
}

function clearTabDropIndicators(): void {
    if (!stripHost) {
        return;
    }
    for (const el of stripHost.querySelectorAll<HTMLElement>(".tab")) {
        el.classList.remove("drop-before", "drop-after");
    }
}

/**
 * Map a drop onto `tabEl` (cursor at clientX, left half = before) to an
 * insertion index in the current store.tabs order, accounting for the
 * source tab being removed first (matches store.moveTab's remove-then-splice).
 */
function dropIndexFor(tabEl: HTMLElement, dragTabID: string, clientX: number): number {
    if (!stripHost) {
        return 0;
    }
    const tabs = Array.from(stripHost.querySelectorAll<HTMLElement>(".tab"));
    const sourceIdx = tabs.findIndex((t) => t.dataset.tabId === dragTabID);
    const hoverIdx = tabs.indexOf(tabEl);
    const rect = tabEl.getBoundingClientRect();
    const before = clientX < rect.left + rect.width / 2;
    let target = hoverIdx + (before ? 0 : 1);
    if (sourceIdx < target) {
        target--; // removing the source shifts later indices left
    }
    return target;
}

/** Render the tab strip into host (subscribes to store.tabs). */
export function renderTabStrip(host: HTMLElement): void {
    stripHost = host;
    tabDrag = null;
    if (stripUnsub) {
        stripUnsub();
    }
    // Activate on pointerdown (delegated): if a .tab node is replaced between
    // mousedown and mouseup by any store-driven rebuild, the browser never
    // fires `click` on the tab; activating at press time keeps switching
    // deterministic. The × button keeps its own click handler.
    host.addEventListener("pointerdown", (e) => {
        if (e.button !== 0) {
            return;
        }
        const target = e.target as HTMLElement | null;
        if (!target || typeof target.closest !== "function") {
            return;
        }
        if (target.closest(".tab-close")) {
            return;
        }
        const tabEl = target.closest<HTMLElement>(".tab");
        if (tabEl?.dataset.tabId) {
            store.activateTab(tabEl.dataset.tabId);
        }
    });

    // Pointer-drag reorder: record a candidate on left-press over a tab,
    // promote it to a real drag once the pointer passes the threshold, then
    // commit the new order on release. Handlers stay on the persistent host
    // (never rebuilt); the strip itself is replaced on store changes.
    host.addEventListener("pointerdown", (e) => {
        if (e.button !== 0) {
            return;
        }
        const target = e.target as HTMLElement | null;
        if (!target || typeof target.closest !== "function") {
            return;
        }
        if (target.closest(".tab-close")) {
            return;
        }
        const tabId = target.closest<HTMLElement>(".tab")?.dataset.tabId;
        if (!tabId) {
            return;
        }
        tabDrag = {
            tabID: tabId,
            pointerId: e.pointerId,
            startX: e.clientX,
            startY: e.clientY,
            started: false,
        };
    });
    host.addEventListener("pointermove", (e) => {
        if (!tabDrag || tabDrag.pointerId !== e.pointerId) {
            return;
        }
        if (!tabDrag.started) {
            const dx = e.clientX - tabDrag.startX;
            const dy = e.clientY - tabDrag.startY;
            if (dx * dx + dy * dy < DRAG_THRESHOLD_PX * DRAG_THRESHOLD_PX) {
                return;
            }
            tabDrag.started = true;
            try {
                host.setPointerCapture(tabDrag.pointerId);
            } catch {
                /* pointer already released; harmless */
            }
            dragTabEl(tabDrag.tabID)?.classList.add("dragging");
            clearTabDropIndicators();
        }
        const hovered = document.elementFromPoint(e.clientX, e.clientY)?.closest<HTMLElement>(".tab");
        if (!hovered || hovered.dataset.tabId === tabDrag.tabID) {
            clearTabDropIndicators();
            return;
        }
        const rect = hovered.getBoundingClientRect();
        hovered.classList.toggle("drop-before", e.clientX < rect.left + rect.width / 2);
        hovered.classList.toggle("drop-after", e.clientX >= rect.left + rect.width / 2);
    });
    const endTabDrag = (e: PointerEvent) => {
        if (!tabDrag || tabDrag.pointerId !== e.pointerId) {
            return;
        }
        const drag = tabDrag;
        tabDrag = null;
        dragTabEl(drag.tabID)?.classList.remove("dragging");
        if (!drag.started || !stripHost) {
            clearTabDropIndicators();
            return;
        }
        const hovered = document.elementFromPoint(e.clientX, e.clientY)?.closest<HTMLElement>(".tab");
        clearTabDropIndicators();
        if (!hovered) {
            return;
        }
        store.moveTab(drag.tabID, dropIndexFor(hovered, drag.tabID, e.clientX));
    };
    host.addEventListener("pointerup", endTabDrag);
    host.addEventListener("pointercancel", endTabDrag);

    stripUnsub = store.subscribe(() => renderStrip());
    lastKey = "";
    renderStrip();
}

function renderStrip(): void {
    if (!stripHost) {
        return;
    }
    const key = snapshotKey();
    if (key === lastKey) {
        return;
    }
    lastKey = key;
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
        el.addEventListener("contextmenu", (e) => {
            e.preventDefault();
            openTabContextMenu(e.clientX, e.clientY, tab.id);
        });

        el.append(dot, label, close);
        strip.appendChild(el);
    }

    stripHost.appendChild(strip);

    // Reveal the active tab by scrolling ONLY the tab strip (never ancestor
    // containers): mirrors scrollIntoView({inline:"nearest"}) via viewport
    // rects, so unrelated scroll containers (the right pane, the page) are
    // never scrolled as a side effect of a tab activation/rebuild.
    if (activeTabID) {
        const activeEl = strip.querySelector<HTMLElement>(`.tab[data-tab-id="${activeTabID}"]`);
        if (activeEl) {
            const sr = strip.getBoundingClientRect();
            const er = activeEl.getBoundingClientRect();
            if (er.left < sr.left) {
                strip.scrollLeft += er.left - sr.left;
            } else if (er.right > sr.right) {
                strip.scrollLeft += er.right - sr.right;
            }
        }
    }
}