// components/shell.ts — the app shell (master plan §6 Layout).
// Left panel (search header + toolbar + tree/search body), splitter
// (drag, clamp 240–480, persist window.leftWidth), right pane (tab strip
// + terminal panes) and the status-band row. The toolbar hosts the gear
// menu (Phase 4d) that opens Settings / Lock vault / About.
//
// SFTP dock side (settings.sftpPanelSide, plan sftp-panel-side): docked right
// the browser is its own column beside the terminal (today's layout); docked
// left (the default) it takes the tree's place inside the left column, with a
// slim top-of-column toggle switching between the tree and the browser. The
// store subscription installed at the end of renderShell re-renders the shell
// when the side changes in Settings.

import { AppService } from "../rpc";
import {
    store,
    sftpPanelVisible,
    hasReadyActiveTab,
    normalizeSftpPanelSide,
    DEFAULT_LEFT_WIDTH,
    DEFAULT_SFTP_WIDTH,
    type Settings,
    type SftpPanelSide,
} from "../store";
import { toast } from "./toasts";
import { renderSearch } from "./search";
import { renderTreeBody, openNewSession, openNewFolderAt } from "./tree";
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
 * In right mode this is the always-visible left panel; in left mode it is the
 * `.left-tree-view` wrapper that swaps with the SFTP view. The toolbar
 * `[SFTP]` reopen button only exists in right mode — left mode's
 * top-of-column toggle replaces it.
 *
 * Returns the hint element (whose display the layout keeps in sync) and the
 * optional `[SFTP]` button.
 */
function buildTreeView(
    container: HTMLElement,
    withSftpButton: boolean,
): { hint: HTMLElement; sftpBtn: HTMLButtonElement | null } {
    // Search header.
    const searchHost = document.createElement("div");
    searchHost.className = "left-header";
    container.appendChild(searchHost);
    renderSearch(searchHost);

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

    // [SFTP]: reopen button for right docking, where the browser has its own
    // column. Visible only when the setting is on, a ready tab exists, and the
    // panel is currently hidden (the session tree is always visible on the
    // left). Left mode omits it; the column toggle handles the swap.
    let sftpBtn: HTMLButtonElement | null = null;
    if (withSftpButton) {
        sftpBtn = document.createElement("button");
        sftpBtn.type = "button";
        sftpBtn.className = "btn small";
        sftpBtn.textContent = "SFTP";
        sftpBtn.title = "Open the SFTP browser for the active session";
        sftpBtn.addEventListener("click", () => store.set({ sftpPanelOpen: true }));
    }

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
    toolbar.append(btnNewSession, btnNewFolder);
    if (sftpBtn) {
        toolbar.appendChild(sftpBtn);
    }
    toolbar.append(spacer, gear);
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
    let switchRowEl: HTMLElement | null = null;
    let toggleBtnEl: HTMLButtonElement | null = null;
    let treeViewEl: HTMLElement | null = null;
    let sftpViewEl: HTMLElement | null = null;

    if (side === "right") {
        // Right docking keeps today's layout: the session tree owns the left
        // panel and the browser has its own right-hand column (built below).
        const parts = buildTreeView(left, true);
        hintEl = parts.hint;
        sftpBtnEl = parts.sftpBtn;
    } else {
        // Left docking: a slim header row holds the single toggle that swaps
        // the tree and the browser inside this column.
        const switcher = document.createElement("div");
        switcher.className = "left-view-switch";
        const toggle = document.createElement("button");
        toggle.type = "button";
        toggle.className = "btn small icon-btn";
        toggle.addEventListener("click", () => {
            store.set({ sftpPanelOpen: !store.getState().sftpPanelOpen });
        });
        switcher.appendChild(toggle);
        left.appendChild(switcher);
        switchRowEl = switcher;
        toggleBtnEl = toggle;

        treeViewEl = document.createElement("div");
        treeViewEl.className = "left-tree-view";
        left.appendChild(treeViewEl);
        hintEl = buildTreeView(treeViewEl, false).hint;

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
                sftpBtnEl.style.display =
                    st.settings.sftpBrowserEnabled && readyTab && !panel ? "" : "none";
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
            // The switcher row is fixed (it must not appear/disappear as tabs
            // connect); it is hidden only when the browser setting is off.
            if (switchRowEl) {
                switchRowEl.style.display = enabled ? "" : "none";
            }
            if (toggleBtnEl) {
                const showSftp = !panel;
                toggleBtnEl.textContent = showSftp ? "📁" : "☰";
                toggleBtnEl.disabled = !readyTab;
                const label = !readyTab
                    ? "Connect to a session…"
                    : showSftp
                      ? "Show SFTP browser"
                      : "Show sessions";
                toggleBtnEl.title = label;
                toggleBtnEl.setAttribute("aria-label", label);
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