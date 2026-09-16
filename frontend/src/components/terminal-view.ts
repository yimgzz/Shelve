// components/terminal-view.ts — the terminal group area (Phase 4c task 3;
// master plan §6 Terminal, terminal split groups). Renders the store's
// `groups` as a horizontal row of VS Code-style editor groups: each group is
// one `.term-group` holding a tab strip (mountTabStrip) plus one visible
// terminal pane. Panes stay pooled per tab (`TermPool`) and are re-parented
// between groups without being recreated, so splitting/moving is cheap and
// the xterm viewport/scrollback survives. Draggable `.group-splitter`s
// rebalance ephemeral widths (module-local, never persisted — A3). State
// overlays (connecting / error / exit) are centered cards with real buttons
// ([Retry] → Reconnect, [Close tab]).

import { store, activeGroup, activeTab } from "../store";
import type { Tab, TabGroup, TabState, TerminalSettings } from "../store";
import { TermPool } from "../terminal/xterm";
import { TerminalService } from "../rpc";
import { mountTabStrip, type TabStripHandle } from "./tabs";

let areaHost: HTMLElement | null = null;
let areaEl: HTMLElement | null = null;
let unsub: (() => void) | null = null;
let lastActive: string | null = null;
let lastActiveState: TabState | null = null;
/**
 * Focused group of the last reconcile. Tracked alongside `lastActive` so a
 * split/move that re-parents the already-active tab into a different group
 * still re-fits and re-focuses it (the tab id alone would look unchanged).
 */
let lastActiveGroup: string | null = null;
/**
 * Reference identity of the last reconcile inputs (groups / activeGroupID /
 * settings.terminal). Guards reconcile against unrelated store churn
 * (monitor metrics, sftp progress, search, tree) that would otherwise
 * re-loop every group/pane on every store set.
 */
let lastReconcileGroups: TabGroup[] | null = null;
let lastReconcileActiveGroup: string | null = null;
let lastReconcileTerm: TerminalSettings | null = null;

interface Pane {
    el: HTMLElement;
    xtermEl: HTMLElement;
    overlay: HTMLElement;
}

/** tabID → pane element. Pool entries are tracked separately in TermPool. */
const panes = new Map<string, Pane>();

interface GroupView {
    container: HTMLElement;
    paneHost: HTMLElement;
    strip: TabStripHandle;
}

/** groupID → group container + strip. */
const groupViews = new Map<string, GroupView>();
/** left group id → the splitter element to the right of that group. */
const splitterViews = new Map<string, HTMLElement>();
/** Ephemeral per-group widths set by divider drags (never persisted, A3). */
const widths = new Map<string, number>();

/** Minimum usable group width (dividers clamp against this). */
const MIN_GROUP_W = 240;

interface SplitterDrag {
    pointerId: number;
    leftGroupID: string;
}

let splitterDrag: SplitterDrag | null = null;

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
    if (!areaEl) {
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
    areaEl.appendChild(empty);
}

/** Apply the ephemeral divider widths (absent → equal/flexible width). */
function applyWidths(): void {
    for (const [gid, view] of groupViews) {
        const w = widths.get(gid);
        view.container.style.flex = w === undefined ? "1 1 0" : `0 0 ${Math.round(w)}px`;
    }
}

function createSplitter(leftGroupID: string): HTMLElement {
    const sp = document.createElement("div");
    sp.className = "group-splitter";
    sp.setAttribute("role", "separator");
    sp.setAttribute("aria-orientation", "vertical");

    sp.addEventListener("pointerdown", (e) => {
        e.preventDefault();
        sp.setPointerCapture(e.pointerId);
        sp.classList.add("dragging");
        splitterDrag = { pointerId: e.pointerId, leftGroupID };
    });
    sp.addEventListener("pointermove", (e) => {
        const drag = splitterDrag;
        if (!drag || drag.pointerId !== e.pointerId) {
            return;
        }
        const st = store.getState();
        const gi = st.groups.findIndex((g) => g.id === drag.leftGroupID);
        if (gi === -1) {
            return;
        }
        const left = groupViews.get(st.groups[gi].id);
        const right = groupViews.get(st.groups[gi + 1]?.id ?? "");
        if (!left || !right) {
            return;
        }
        const lr = left.container.getBoundingClientRect();
        const rr = right.container.getBoundingClientRect();
        const total = lr.width + rr.width;
        if (total < MIN_GROUP_W * 2) {
            return;
        }
        const w = Math.max(MIN_GROUP_W, Math.min(total - MIN_GROUP_W, e.clientX - lr.left));
        // The left group takes the dragged px width; the right one flexes to
        // fill the remaining space (no explicit width entry).
        widths.set(st.groups[gi].id, w);
        widths.delete(st.groups[gi + 1].id);
        applyWidths();
    });
    const end = (e: PointerEvent): void => {
        const drag = splitterDrag;
        if (!drag || drag.pointerId !== e.pointerId) {
            return;
        }
        splitterDrag = null;
        sp.classList.remove("dragging");
    };
    sp.addEventListener("pointerup", end);
    sp.addEventListener("pointercancel", end);
    return sp;
}

function createGroupView(groupID: string): GroupView {
    const container = document.createElement("div");
    container.className = "term-group";

    const stripHost = document.createElement("div");
    stripHost.className = "tab-strip-host";

    const paneHost = document.createElement("div");
    paneHost.className = "terminal-pane-host";

    container.append(stripHost, paneHost);
    // Focus the group on any press inside it. Capture phase (no
    // preventDefault) so xterm still receives the event for text
    // selection/focus; activateGroup is a no-op when already focused. The ×
    // close button is exempt: focusing the group would rebuild its strip and
    // swallow the close click on an unfocused group.
    container.addEventListener(
        "pointerdown",
        (e) => {
            if (e.button !== 0) {
                return; // middle/right presses keep their own behavior
            }
            const target = e.target as HTMLElement | null;
            if (target && typeof target.closest === "function" && target.closest(".tab-close")) {
                return;
            }
            store.activateGroup(groupID);
        },
        true,
    );

    const strip = mountTabStrip(stripHost, groupID);
    return { container, paneHost, strip };
}

function reconcile(): void {
    if (!areaEl) {
        return;
    }
    const state = store.getState();
    const { groups, activeGroupID, settings } = state;

    // Skip all DOM work when nothing this view renders changed (reference
    // identity comparison). The store mutates on many things the terminal
    // view does not draw — monitor metrics, sftp progress, search queries,
    // tree refreshes — so this guard removes the per-set group/pane loops.
    if (
        lastReconcileGroups === groups &&
        lastReconcileActiveGroup === activeGroupID &&
        lastReconcileTerm === settings.terminal
    ) {
        return;
    }
    lastReconcileGroups = groups;
    lastReconcileActiveGroup = activeGroupID;
    lastReconcileTerm = settings.terminal;

    // Drop any "No open sessions" placeholder; re-added below when needed.
    areaEl.querySelector(".empty-state")?.remove();

    // Remove panes (and tear down their pooled terminals) only for tabs that
    // no longer exist anywhere. Existing panes stay attached — they are
    // re-parented between groups below, never wiped (the xterm viewport must
    // survive a split/move).
    const liveTabs = new Set<string>();
    for (const g of groups) {
        for (const t of g.tabs) {
            liveTabs.add(t.id);
        }
    }
    for (const id of Array.from(panes.keys())) {
        if (!liveTabs.has(id)) {
            TermPool.destroy(id);
            panes.get(id)?.el.remove();
            panes.delete(id);
        }
    }

    // Remove group containers (and their strip subscriptions) for groups that
    // no longer exist; drop the group's ephemeral width with it.
    for (const [gid, view] of Array.from(groupViews)) {
        if (!groups.some((g) => g.id === gid)) {
            view.strip.destroy();
            view.container.remove();
            groupViews.delete(gid);
            widths.delete(gid);
        }
    }

    // A splitter is valid only between two groups, i.e. keyed by a group that
    // still has a right neighbour. This drops splitters for a removed left
    // group AND for a surviving left group whose right neighbour went away
    // (which would otherwise linger as a dead divider), including the
    // groups.length === 0 case (empty needed set).
    const neededSplitters = new Set(groups.slice(0, -1).map((g) => g.id));
    for (const [gid, el] of Array.from(splitterViews)) {
        if (!neededSplitters.has(gid)) {
            el.remove();
            splitterViews.delete(gid);
        }
    }

    if (groups.length === 0) {
        lastActive = null;
        lastActiveState = null;
        lastActiveGroup = null;
        showEmptyState();
        return;
    }

    // Create a view (container + strip) for every new group.
    for (const g of groups) {
        if (!groupViews.has(g.id)) {
            groupViews.set(g.id, createGroupView(g.id));
        }
    }

    // Order group containers and their splitters to match `groups`. Only move
    // an element when it is not already at its target index: re-appending an
    // in-place node detaches (and re-attaches) it, which would blur the pooled
    // xterm and needlessly churn layout on every unrelated store update.
    const order: HTMLElement[] = [];
    groups.forEach((g, i) => {
        order.push(groupViews.get(g.id)!.container);
        if (i < groups.length - 1) {
            let sp = splitterViews.get(g.id);
            if (!sp) {
                sp = createSplitter(g.id);
                splitterViews.set(g.id, sp);
            }
            order.push(sp);
        }
    });
    for (let i = 0; i < order.length; i++) {
        const el = order[i];
        if (areaEl.children[i] !== el) {
            areaEl.insertBefore(el, areaEl.children[i] ?? null);
        }
    }

    // Create panes + pooled terminals for NEW tabs, and re-parent every pane
    // into its group's pane host (a split/move changes the parent while the
    // pooled xterm — living in the pane's xtermEl — is preserved).
    for (const g of groups) {
        const view = groupViews.get(g.id)!;
        for (const tab of g.tabs) {
            let p = panes.get(tab.id);
            if (!p) {
                p = buildPane(tab);
                panes.set(tab.id, p);
                TermPool.create(tab.id, p.xtermEl, { settings: settings.terminal });
            }
            if (p.el.parentElement !== view.paneHost) {
                view.paneHost.appendChild(p.el);
            }
        }
    }

    // Visibility + overlays + focus indicator.
    for (const g of groups) {
        const view = groupViews.get(g.id)!;
        view.container.classList.toggle("active", g.id === activeGroupID);
        for (const tab of g.tabs) {
            const p = panes.get(tab.id);
            if (!p) {
                continue;
            }
            p.el.style.display = tab.id === g.activeTabID ? "flex" : "none";
            renderOverlay(p.overlay, tab);
        }
    }

    applyWidths();

    // Focus/fit the focused group's active tab — and re-focus when it becomes
    // "ready". The session-open path activates while the tab is still
    // "connecting"; host-key / key-passphrase prompts (vault:hostkey-prompt,
    // vault:key-prompt) steal focus during that window and only restore it to
    // their own previously-focused element, never the terminal. Re-activating
    // on the connecting→ready transition repairs it so the user can type
    // immediately.
    const focused = activeGroup(state);
    const active = activeTab(state);
    const becameReady = active !== undefined && active.state === "ready" && lastActiveState !== "ready";
    if (
        active &&
        (active.id !== lastActive || becameReady || focused?.id !== lastActiveGroup) &&
        active.state !== "error" &&
        active.state !== "closed"
    ) {
        TermPool.activate(active.id);
    }
    lastActive = active ? active.id : null;
    lastActiveState = active ? active.state : null;
    lastActiveGroup = focused?.id ?? null;
}

/**
 * Mount the terminal group area into `host` (the .terminal-area-host wrapper).
 * Subscribes to the store; panes and pooled terminals persist across renders
 * and are torn down only when a tab closes or the vault locks.
 */
export function renderTerminalArea(host: HTMLElement): void {
    areaHost = host;
    if (!areaHost.querySelector(".terminal-area")) {
        const area = document.createElement("div");
        area.className = "terminal-area";
        areaHost.appendChild(area);
    }
    areaEl = areaHost.querySelector<HTMLElement>(".terminal-area")!;
    if (unsub) {
        unsub();
    }
    // A fresh mount must always run the first reconcile, even when the store
    // contents are unchanged since the previous mount (same references). The
    // shell root wipe detached every group container, so tear down the old
    // strips (their subscriptions) and splitters before rebuilding; pooled
    // terminals survive because they live in `panes`, not in the DOM.
    lastReconcileGroups = null;
    lastReconcileActiveGroup = null;
    lastReconcileTerm = null;
    lastActive = null;
    lastActiveState = null;
    lastActiveGroup = null;
    for (const view of groupViews.values()) {
        view.strip.destroy();
    }
    groupViews.clear();
    for (const el of splitterViews.values()) {
        el.remove();
    }
    splitterViews.clear();
    unsub = store.subscribe(reconcile);
    reconcile();
}
