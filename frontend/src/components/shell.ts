// components/shell.ts — the app shell (master plan §6 Layout).
// Left panel (search header + toolbar + tree/search body), splitter
// (drag, clamp 240–480, persist window.leftWidth), right pane (tab strip
// + terminal panes) and the status-band row. The toolbar hosts the gear
// menu (Phase 4d) that opens Settings / Lock vault / About.
//
// SFTP dock side (settings.sftpPanelSide, plan sftp-panel-side): docked right
// the browser is its own column beside the terminal; docked left (the default)
// it takes the tree's place inside the left column. In both modes the toolbar
// [SFTP] button opens the browser and, when docked left, the panel-header
// [Sessions] button returns to the tree (plan ui-ux-refinements §B). The store
// subscription installed at the end of renderShell re-renders the shell when
// the side changes in Settings.

import { AppService } from "../rpc";
import {
    store,
    sftpPanelVisible,
    sftpReopenVisible,
    hasReadyActiveTab,
    normalizeSftpPanelSide,
    DEFAULT_LEFT_WIDTH,
    DEFAULT_SFTP_WIDTH,
    type Settings,
    type SftpPanelSide,
} from "../store";
import { toast } from "./toasts";
import { renderSearch } from "./search";
import { renderTreeBody, openNewSession, openNewFolderAt, collapseAllTree, expandAllTree } from "./tree";
import { renderSftpPanel } from "./sftp-panel";
import { openGearMenu } from "./gear";
import { renderTerminalArea } from "./terminal-view";
import { renderMonitorBar } from "./monitor-bar";

const MIN_LEFT = 240;
const MAX_LEFT = 480;
// SFTP right-panel drag range (master plan §6) and the floor that keeps the
// terminal column usable once the left panel + both splitters are subtracted.
const MIN_SFTP = 200;
const MAX_SFTP = 640;
const SFTP_TERM_MIN = 240;
const SPLITTER_TOTAL = 8; // two 4 px splitters
const SAVE_DEBOUNCE_MS = 300;
const RESIZE_DEBOUNCE_MS = 150;

declare global {
    interface Window {
        /**
         * Dev-only flag kept for dev tooling (search timing in tree.ts,
         * dev notes). The 4a/4b QA hook UI was removed in 4d.
         */
        __dsmDev?: boolean;
    }
}

let saveTimer: number | null = null;
/**
 * SFTP-layout subscriber of the current shell mount. renderShell re-runs on
 * every lock/unlock cycle; the previous subscription must be released or
 * every unlock permanently adds one more listener (listener-audit rule).
 */
let sftpLayoutUnsub: (() => void) | null = null;
/**
 * Debounced window-resize handler of the current shell mount (re-clamps the
 * SFTP width so the terminal keeps ≥ 240 px after shrinking the window);
 * released together with sftpLayoutUnsub on every re-render.
 */
let sftpResizeUnsub: (() => void) | null = null;
/**
 * Dock side the currently-mounted shell was rendered for. The store
 * subscription installed at the end of renderShell compares it against
 * `settings.sftpPanelSide` and re-renders the shell on a change (the Settings
 * dialog is the only place the side changes).
 */
let mountedSide: SftpPanelSide | null = null;
/** Side-change watcher of the current shell mount (released on re-render). */
let sideWatchUnsub: (() => void) | null = null;
/** Collapses a burst of side changes into one scheduled re-render. */
let rerenderScheduled = false;
/**
 * Persist the current window geometry (left + SFTP widths) debounced. Called
 * at the end of either splitter drag; the store already carries the new
 * width by the time this runs, so a single full-settings write suffices.
 */
function persistWindow(): void {
    if (saveTimer !== null) {
        window.clearTimeout(saveTimer);
    }
    saveTimer = window.setTimeout(() => {
        // Read the store inside the timeout so a concurrent window:state
        // merge (main-process geometry) can never be clobbered by a stale
        // snapshot captured before the merge.
        const { settings } = store.getState();
        const next: Settings = { ...settings, window: { ...settings.window } };
        void AppService.SaveSettings(next).catch((err) => toast("error", String(err)));
    }, SAVE_DEBOUNCE_MS);
}

/** Build the search header, toolbar, tree body and SFTP hint into `container`.
 *
 * Used by both dock sides. The toolbar `[SFTP]` reopen/open button is always
 * created; the layout sync decides when it is visible (the setting is on, a
 * ready tab exists and the browser is closed).
 *
 * Returns the hint element (whose display the layout keeps in sync) and the
 * `[SFTP]` button.
 */
function buildTreeView(container: HTMLElement): { hint: HTMLElement; sftpBtn: HTMLButtonElement } {
    // Search header.
    const searchHost = document.createElement("div");
    searchHost.className = "left-header";
    container.appendChild(searchHost);
    renderSearch(searchHost);

    // Collapse all / Expand all (plan §D): kept in the search row so the
    // 240 px toolbar never clips the gear.
    const treeActions = document.createElement("div");
    treeActions.className = "tree-actions";
    const collapseBtn = document.createElement("button");
    collapseBtn.type = "button";
    collapseBtn.className = "btn small icon-btn";
    collapseBtn.textContent = "⊟";
    collapseBtn.title = "Collapse all";
    collapseBtn.setAttribute("aria-label", "Collapse all");
    collapseBtn.addEventListener("click", collapseAllTree);
    const expandBtn = document.createElement("button");
    expandBtn.type = "button";
    expandBtn.className = "btn small icon-btn";
    expandBtn.textContent = "⊞";
    expandBtn.title = "Expand all";
    expandBtn.setAttribute("aria-label", "Expand all");
    expandBtn.addEventListener("click", expandAllTree);
    treeActions.append(collapseBtn, expandBtn);
    searchHost.appendChild(treeActions);

    // Toolbar (+ Session / + Folder) — now active (Phase 4b).
    const toolbar = document.createElement("div");
    toolbar.className = "toolbar";
    const btnNewSession = document.createElement("button");
    btnNewSession.type = "button";
    btnNewSession.className = "btn small";
    btnNewSession.textContent = "+ Session";
    btnNewSession.addEventListener("click", () => openNewSession(""));
    const btnNewFolder = document.createElement("button");
    btnNewFolder.type = "button";
    btnNewFolder.className = "btn small";
    btnNewFolder.textContent = "+ Folder";
    btnNewFolder.addEventListener("click", () => openNewFolderAt(""));

    // [SFTP]: open the browser for the active ready tab. Shown only when the
    // setting is on, a ready tab exists and the panel is currently hidden (the
    // layout sync sets its display); works on both dock sides.
    const sftpBtn = document.createElement("button");
    sftpBtn.type = "button";
    sftpBtn.className = "btn small";
    sftpBtn.textContent = "SFTP";
    sftpBtn.title = "Open the SFTP browser for the active session";
    sftpBtn.addEventListener("click", () => store.set({ sftpPanelOpen: true }));

    // Gear menu (Phase 4d): [Settings…] / [Lock vault…] / [About], anchored
    // to the button at the right edge of the toolbar (master plan §6).
    const spacer = document.createElement("div");
    spacer.className = "spacer";
    const gear = document.createElement("button");
    gear.type = "button";
    gear.className = "btn small icon-btn";
    gear.textContent = "⚙";
    gear.title = "Menu";
    gear.setAttribute("aria-label", "Menu");
    gear.addEventListener("click", (e) => {
        e.stopPropagation();
        const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
        openGearMenu(r.right - 8, r.bottom + 4);
    });
    toolbar.append(btnNewSession, btnNewFolder, sftpBtn, spacer, gear);
    container.appendChild(toolbar);

    // Tree / search body (scrollable).
    const treeHost = document.createElement("div");
    treeHost.className = "tree-body";
    container.appendChild(treeHost);
    renderTreeBody(treeHost);

    // Hint shown when the SFTP browser is enabled but no active ready tab
    // exists (Phase 5c task 1). The layout syncs its display.
    const sftpHint = document.createElement("div");
    sftpHint.className = "sftp-hint";
    sftpHint.textContent = "Connect to a session to open the SFTP browser";
    container.appendChild(sftpHint);

    return { hint: sftpHint, sftpBtn };
}

/** Render the app shell into the given root element. */
export function renderShell(root: HTMLElement): void {
    if (sftpLayoutUnsub) {
        sftpLayoutUnsub();
        sftpLayoutUnsub = null;
    }
    if (sftpResizeUnsub) {
        sftpResizeUnsub();
        sftpResizeUnsub = null;
    }
    if (sideWatchUnsub) {
        sideWatchUnsub();
        sideWatchUnsub = null;
    }
    root.textContent = "";

    const side: SftpPanelSide = normalizeSftpPanelSide(
        store.getState().settings.sftpPanelSide,
    );
    mountedSide = side;
    // Left docking replaces the tree inside the left column; right docking is
    // the 5-column layout. Toggle (not add/remove) so a stale class from a
    // previous render can never survive.
    document.body.classList.toggle("sftp-left", side === "left");

    // ---- Left column ----
    const left = document.createElement("aside");
    left.className = "left-panel";
    left.setAttribute("aria-label", "Session list");

    let hintEl: HTMLElement;
    let sftpBtnEl: HTMLButtonElement | null = null;
    let treeViewEl: HTMLElement | null = null;
    let sftpViewEl: HTMLElement | null = null;

    if (side === "right") {
        // Right docking keeps today's layout: the session tree owns the left
        // panel and the browser has its own right-hand column (built below).
        const parts = buildTreeView(left);
        hintEl = parts.hint;
        sftpBtnEl = parts.sftpBtn;
    } else {
        // Left docking: the browser takes the tree's place inside this column.
        // The toolbar [SFTP] and the panel-header [Sessions] buttons swap the
        // two views (there is no separate top-of-column toggle any more).
        treeViewEl = document.createElement("div");
        treeViewEl.className = "left-tree-view";
        left.appendChild(treeViewEl);
        const parts = buildTreeView(treeViewEl);
        hintEl = parts.hint;
        sftpBtnEl = parts.sftpBtn;

        sftpViewEl = document.createElement("div");
        sftpViewEl.className = "left-sftp-view";
        sftpViewEl.setAttribute("aria-label", "SFTP browser");
        const leftSftpHost = document.createElement("div");
        leftSftpHost.className = "sftp-host";
        sftpViewEl.appendChild(leftSftpHost);
        left.appendChild(sftpViewEl);
        renderSftpPanel(leftSftpHost);
    }

    root.appendChild(left);

    // ---- Splitter ----
    const splitter = document.createElement("div");
    splitter.className = "splitter";
    splitter.setAttribute("role", "separator");
    splitter.setAttribute("aria-orientation", "vertical");
    root.appendChild(splitter);

    const applyWidth = (w: number) => {
        // clientX is a double (fractional under HiDPI/fractional scaling or
        // non-integer zoom); round before it reaches settings.window.leftWidth,
        // which the Go backend decodes as an int.
        const clamped = Math.round(Math.min(MAX_LEFT, Math.max(MIN_LEFT, w)));
        document.documentElement.style.setProperty("--left-w", `${clamped}px`);
        store.set({ leftPanelWidth: clamped });
    };

    // Apply the persisted/default width.
    const initial = store.getState().settings.window.leftWidth || DEFAULT_LEFT_WIDTH;
    applyWidth(initial);

    splitter.addEventListener("pointerdown", (e) => {
        e.preventDefault();
        splitter.setPointerCapture(e.pointerId);
        splitter.classList.add("dragging");
    });
    splitter.addEventListener("pointermove", (e) => {
        if (!splitter.hasPointerCapture(e.pointerId)) {
            return;
        }
        applyWidth(e.clientX);
    });
    const endDrag = () => {
        if (splitter.classList.contains("dragging")) {
            splitter.classList.remove("dragging");
            const w = store.getState().leftPanelWidth;
            store.set({
                settings: {
                    ...store.getState().settings,
                    window: { ...store.getState().settings.window, leftWidth: w },
                },
            });
            persistWindow();
        }
    };
    splitter.addEventListener("pointerup", endDrag);
    splitter.addEventListener("pointercancel", endDrag);

    // ---- Right pane ----
    const right = document.createElement("main");
    right.className = "right-pane";
    right.setAttribute("aria-label", "Terminal area");

    const areaHost = document.createElement("div");
    areaHost.className = "terminal-area-host";
    right.appendChild(areaHost);
    renderTerminalArea(areaHost);

    root.appendChild(right);

    if (side === "right") {
        // ---- SFTP right column (right of the terminal) ----
        // The SFTP browser is its own grid column; the session tree on the left
        // is never covered. The column collapses fully (0 px track +
        // display:none) when the panel is not visible.
        const sftpSplitter = document.createElement("div");
        sftpSplitter.className = "splitter sftp-splitter";
        sftpSplitter.setAttribute("role", "separator");
        sftpSplitter.setAttribute("aria-orientation", "vertical");

        const sftpSide = document.createElement("aside");
        sftpSide.className = "sftp-side";
        sftpSide.setAttribute("aria-label", "SFTP browser");

        const sftpHost = document.createElement("div");
        sftpHost.className = "sftp-host";
        sftpSide.appendChild(sftpHost);
        renderSftpPanel(sftpHost);

        // Clamp SFTP width: 200–640 px, capped so the terminal column keeps
        // ≥ 240 px given the current left-panel width and both splitters.
        const clampSftpWidth = (w: number): number => {
            const lo = MIN_SFTP;
            const hi = Math.max(
                lo,
                Math.min(MAX_SFTP, window.innerWidth - store.getState().leftPanelWidth - SPLITTER_TOTAL - SFTP_TERM_MIN),
            );
            // Round: window.innerWidth - clientX can be fractional, and the Go
            // backend decodes settings.window.sftpWidth as an int.
            return Math.round(Math.min(hi, Math.max(lo, w)));
        };
        const applySftpWidth = (w: number): void => {
            store.set({ sftpPanelWidth: clampSftpWidth(w) });
        };

        // Single source of truth (store.sftpPanelVisible): the right column
        // shows only when the setting is on AND the panel is open AND the
        // active tab is ready. It sizes both grid tracks, toggles the column,
        // syncs the left-panel hint, and reveals the toolbar [SFTP] button.
        const applySftpLayout = () => {
            const st = store.getState();
            const panel = sftpPanelVisible(st);
            const readyTab = hasReadyActiveTab(st);
            const w = clampSftpWidth(st.sftpPanelWidth);
            document.documentElement.style.setProperty("--sftp-w", panel ? `${w}px` : "0px");
            document.documentElement.style.setProperty("--sftp-gap", panel ? "4px" : "0px");
            sftpSide.style.display = panel ? "flex" : "none";
            sftpSide.setAttribute("aria-hidden", panel ? "false" : "true");
            hintEl.style.display = st.settings.sftpBrowserEnabled && !readyTab ? "" : "none";
            if (sftpBtnEl) {
                sftpBtnEl.style.display = sftpReopenVisible(st) ? "" : "none";
            }
        };
        sftpLayoutUnsub = store.subscribe(applySftpLayout);

        // Apply the persisted/default width, then the initial layout.
        const initialSftp = store.getState().settings.window.sftpWidth || DEFAULT_SFTP_WIDTH;
        applySftpWidth(initialSftp);
        applySftpLayout();

        // The SFTP track is a fixed px width sized against window.innerWidth; a
        // plain window resize fires no store change, so re-clamp (debounced) to
        // keep the terminal column at ≥ SFTP_TERM_MIN after the window shrinks.
        let sftpResizeTimer: number | null = null;
        const onSftpResize = () => {
            if (sftpResizeTimer !== null) {
                window.clearTimeout(sftpResizeTimer);
            }
            sftpResizeTimer = window.setTimeout(() => {
                sftpResizeTimer = null;
                applySftpWidth(store.getState().sftpPanelWidth);
            }, RESIZE_DEBOUNCE_MS);
        };
        window.addEventListener("resize", onSftpResize);
        sftpResizeUnsub = () => {
            window.removeEventListener("resize", onSftpResize);
            if (sftpResizeTimer !== null) {
                window.clearTimeout(sftpResizeTimer);
                sftpResizeTimer = null;
            }
        };

        sftpSplitter.addEventListener("pointerdown", (e) => {
            e.preventDefault();
            sftpSplitter.setPointerCapture(e.pointerId);
            sftpSplitter.classList.add("dragging");
        });
        sftpSplitter.addEventListener("pointermove", (e) => {
            if (!sftpSplitter.hasPointerCapture(e.pointerId)) {
                return;
            }
            // The column sits at the right edge: width is the distance from the
            // cursor to the window's right edge.
            applySftpWidth(window.innerWidth - e.clientX);
        });
        const endSftpDrag = () => {
            if (sftpSplitter.classList.contains("dragging")) {
                sftpSplitter.classList.remove("dragging");
                const w = store.getState().sftpPanelWidth;
                store.set({
                    settings: {
                        ...store.getState().settings,
                        window: { ...store.getState().settings.window, sftpWidth: w },
                    },
                });
                persistWindow();
            }
        };
        sftpSplitter.addEventListener("pointerup", endSftpDrag);
        sftpSplitter.addEventListener("pointercancel", endSftpDrag);

        root.appendChild(sftpSplitter);
        root.appendChild(sftpSide);
    } else {
        // ---- Left-docked layout ----
        // One view at a time inside the left column. No right SFTP column or
        // splitter is created; the left splitter keeps owning --left-w.
        const applySftpLayout = (): void => {
            const st = store.getState();
            const panel = sftpPanelVisible(st);
            const readyTab = hasReadyActiveTab(st);
            const enabled = st.settings.sftpBrowserEnabled;
            if (treeViewEl) {
                treeViewEl.style.display = panel ? "none" : "flex";
            }
            if (sftpViewEl) {
                sftpViewEl.style.display = panel ? "flex" : "none";
            }
            hintEl.style.display = enabled && !readyTab ? "" : "none";
            // Toolbar [SFTP]: shared predicate with right docking.
            if (sftpBtnEl) {
                sftpBtnEl.style.display = sftpReopenVisible(st) ? "" : "none";
            }
        };
        sftpLayoutUnsub = store.subscribe(applySftpLayout);
        applySftpLayout();
    }

    // ---- Bottom monitor bar (plan P004): hostname/CPU/RAM/net/uptime/disk
    // for the active ready tab; replaced the Phase 4c user@host status bar.
    const status = document.createElement("footer");
    status.className = "status-band";
    root.appendChild(status);
    renderMonitorBar(status);

    // Re-render the shell when the dock side changes (Settings is the only
    // place it changes). Scheduled as a microtask so it never re-enters this
    // store notification; the guard collapses a burst into one render.
    sideWatchUnsub = store.subscribe((st) => {
        const next = normalizeSftpPanelSide(st.settings.sftpPanelSide);
        if (next === mountedSide || rerenderScheduled) {
            return;
        }
        rerenderScheduled = true;
        queueMicrotask(() => {
            rerenderScheduled = false;
            renderShell(root);
        });
    });
}