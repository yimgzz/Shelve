// components/tabs.ts — per-group tab strip (Phase 4b task 4 + Phase 4c;
// master plan §6 Tabs, terminal split groups). One strip is mounted per
// terminal group: session name, status dot (connecting amber / ready green /
// error red / closed gray), × close on hover, middle-click close, click
// activate, horizontal scroll. Right-click opens a menu (Split to Right /
// Split to Left / Close Others / Close All Tabs / Close Tabs to the Right);
// left-drag reorders within the group or moves a tab onto another group's
// strip. The terminal area (groups, splitters, panes) is owned by
// components/terminal-view.ts.

import { store, findTab } from "../store";
import { openContextMenu, type MenuItem } from "./context-menu";

/** Handle returned by mountTabStrip; destroy() releases the subscription. */
export interface TabStripHandle {
    destroy(): void;
}

// ------------------------------------------------------------ tab menu ---

/** Right-click menu on a tab. Close items are scoped to the tab's group. */
function openTabContextMenu(x: number, y: number, tabID: string): void {
    const st = store.getState();
    const loc = findTab(st, tabID);
    if (!loc) {
        return;
    }
    const { group, groupIndex } = loc;
    const solo = group.tabs.length === 1;
    const items: MenuItem[] = [
        {
            label: "Split to Right",
            // A solo tab in the last group would move into a brand-new group
            // and recreate the identical layout (decision 8).
            disabled: solo && groupIndex === st.groups.length - 1,
            action: () => store.splitTab(tabID, "right"),
        },
        {
            label: "Split to Left",
            disabled: solo && groupIndex === 0,
            action: () => store.splitTab(tabID, "left"),
        },
        {
            label: "Close Others",
            separatorBefore: true,
            disabled: group.tabs.length <= 1,
            action: () => void store.closeOtherTabs(tabID),
        },
        {
            label: "Close All Tabs",
            disabled: group.tabs.length === 0,
            action: () => void store.closeAllTabs(tabID),
        },
        {
            label: "Close Tabs to the Right",
            disabled: loc.tabIndex === group.tabs.length - 1,
            action: () => void store.closeTabsToRight(tabID),
        },
    ];
    openContextMenu(x, y, items);
}

// -------------------------------------------------------- tab dragging ---

interface TabDrag {
    tabID: string;
    fromGroupID: string;
    pointerId: number;
    startX: number;
    startY: number;
    started: boolean;
}

/** Active tab drag (pointer-based; one strip at a time). */
let tabDrag: TabDrag | null = null;

const DRAG_THRESHOLD_PX = 6;

function dragTabEl(tabID: string): HTMLElement | null {
    return document.querySelector<HTMLElement>(`.tab[data-tab-id="${tabID}"]`);
}

function clearTabDropIndicators(): void {
    for (const el of document.querySelectorAll<HTMLElement>(".tab.drop-before, .tab.drop-after")) {
        el.classList.remove("drop-before", "drop-after");
    }
}

/**
 * Map a drop onto `tabEl` (cursor at clientX, left half = before) to an
 * insertion index in the target group's order, accounting for the source tab
 * being removed first when it lives in the same group. Moving across groups
 * needs no correction (the source is not in the target's order).
 */
function dropIndexFor(tabEl: HTMLElement, dragTabID: string, clientX: number): number {
    const strip = tabEl.closest<HTMLElement>(".tab-strip");
    if (!strip) {
        return 0;
    }
    const tabs = Array.from(strip.querySelectorAll<HTMLElement>(".tab"));
    const sourceIdx = tabs.findIndex((t) => t.dataset.tabId === dragTabID);
    const hoverIdx = tabs.indexOf(tabEl);
    const rect = tabEl.getBoundingClientRect();
    const before = clientX < rect.left + rect.width / 2;
    let target = hoverIdx + (before ? 0 : 1);
    if (sourceIdx >= 0 && sourceIdx < target) {
        target--; // removing the source shifts later indices left
    }
    return target;
}

/**
 * Mount a tab strip for one group into `host`. Subscribes to the store and
 * rebuilds only when this group's drawn state changes; `destroy()` releases
 * the subscription and listeners (listener-audit rule).
 */
export function mountTabStrip(host: HTMLElement, groupID: string): TabStripHandle {
    const stripHost = host;
    let lastKey = "";

    /**
     * Snapshot identity of everything renderStrip draws (this group's tab
     * id/state/name and the group's active tab). The strip is rebuilt ONLY
     * when this changes: unrelated store churn — most notably the every-2 s
     * monitor:metrics updates and group-focus changes (the focus indicator is
     * applied by terminal-view on `.term-group`) — must never replace the .tab
     * nodes, or a click split across the rebuild would be swallowed (mousedown
     * and mouseup land on different elements, so no click event fires and the
     * tab appears non-clickable).
     */
    const snapshotKey = (): string => {
        const st = store.getState();
        const group = st.groups.find((g) => g.id === groupID);
        if (!group) {
            return "";
        }
        let key = `${group.activeTabID ?? ""}|`;
        for (const t of group.tabs) {
            key += `${t.id}:${t.state}:${t.session.name};`;
        }
        return key;
    };

    const renderStrip = (): void => {
        const st = store.getState();
        const group = st.groups.find((g) => g.id === groupID);
        if (!group) {
            return;
        }
        const key = snapshotKey();
        if (key === lastKey) {
            return;
        }
        lastKey = key;
        stripHost.textContent = "";

        const strip = document.createElement("div");
        strip.className = "tab-strip";

        for (const tab of group.tabs) {
            const el = document.createElement("div");
            el.className = `tab${tab.id === group.activeTabID ? " active" : ""}`;
            el.dataset.tabId = tab.id;
            el.dataset.groupId = groupID;

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
        const activeId = group.activeTabID;
        if (activeId) {
            const activeEl = strip.querySelector<HTMLElement>(`.tab[data-tab-id="${activeId}"]`);
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
    };

    // Activate on pointerdown (delegated): if a .tab node is replaced between
    // mousedown and mouseup by any store-driven rebuild, the browser never
    // fires `click` on the tab; activating at press time keeps switching
    // deterministic. The × button keeps its own click handler.
    const onActivatePointerDown = (e: PointerEvent): void => {
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
    };

    // Pointer-drag reorder / cross-group move: record a candidate on left-press
    // over a tab, promote it once the pointer passes the threshold, then commit
    // on release. Handlers stay on the persistent strip host (never rebuilt);
    // the strip itself is replaced on store changes. Pointer capture keeps
    // move/up targeted here while elementFromPoint resolves the real hover
    // target across strips.
    const onDragPointerDown = (e: PointerEvent): void => {
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
            fromGroupID: groupID,
            pointerId: e.pointerId,
            startX: e.clientX,
            startY: e.clientY,
            started: false,
        };
    };

    const onDragPointerMove = (e: PointerEvent): void => {
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
                stripHost.setPointerCapture(tabDrag.pointerId);
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
    };

    const endTabDrag = (e: PointerEvent): void => {
        if (!tabDrag || tabDrag.pointerId !== e.pointerId) {
            return;
        }
        const drag = tabDrag;
        tabDrag = null;
        dragTabEl(drag.tabID)?.classList.remove("dragging");
        if (!drag.started) {
            clearTabDropIndicators();
            return;
        }
        const hovered = document.elementFromPoint(e.clientX, e.clientY)?.closest<HTMLElement>(".tab");
        clearTabDropIndicators();
        if (!hovered) {
            return;
        }
        const targetGroupID = hovered.dataset.groupId || drag.fromGroupID;
        store.moveTab(drag.tabID, targetGroupID, dropIndexFor(hovered, drag.tabID, e.clientX));
    };

    stripHost.addEventListener("pointerdown", onActivatePointerDown);
    stripHost.addEventListener("pointerdown", onDragPointerDown);
    stripHost.addEventListener("pointermove", onDragPointerMove);
    stripHost.addEventListener("pointerup", endTabDrag);
    stripHost.addEventListener("pointercancel", endTabDrag);

    const unsub = store.subscribe(() => renderStrip());
    lastKey = "";
    renderStrip();

    return {
        destroy(): void {
            unsub();
            stripHost.removeEventListener("pointerdown", onActivatePointerDown);
            stripHost.removeEventListener("pointerdown", onDragPointerDown);
            stripHost.removeEventListener("pointermove", onDragPointerMove);
            stripHost.removeEventListener("pointerup", endTabDrag);
            stripHost.removeEventListener("pointercancel", endTabDrag);
            if (tabDrag?.fromGroupID === groupID) {
                tabDrag = null;
            }
        },
    };
}
