// components/shell.ts — the app shell (master plan §6 Layout).
// Left panel (search header + toolbar + tree/search body), splitter
// (drag, clamp 240–480, persist window.leftWidth), right pane (tab strip
// + terminal panes) and the status-band row. The toolbar hosts the gear
// menu (Phase 4d) that opens Settings / Lock vault / About.

import { AppService } from "../rpc";
import {
    store,
    sftpPanelVisible,
    hasReadyActiveTab,
    DEFAULT_LEFT_WIDTH,
    DEFAULT_SFTP_WIDTH,
    type Settings,
} from "../store";
import { toast } from "./toasts";
import { renderSearch } from "./search";
import { renderTreeBody, openNewSession, openNewFolderAt } from "./tree";
import { renderSftpPanel } from "./sftp-panel";
import { openGearMenu } from "./gear";
import { renderTabStrip } from "./tabs";
import { renderTerminalView } from "./terminal-view";
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
    root.textContent = "";

    // ---- Left panel ----
    const left = document.createElement("aside");
    left.className = "left-panel";
    left.setAttribute("aria-label", "Session list");

    // Search header.
    const searchHost = document.createElement("div");
    searchHost.className = "left-header";
    left.appendChild(searchHost);
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

    // [SFTP]: reopens the SFTP right panel after it has been closed. Visible
    // only when the setting is on, a ready tab exists, and the panel is
    // currently hidden (the session tree is always visible on the left).
    const btnSftp = document.createElement("button");
    btnSftp.type = "button";
    btnSftp.className = "btn small";
    btnSftp.textContent = "SFTP";
    btnSftp.title = "Open the SFTP browser for the active session";
    btnSftp.addEventListener("click", () => store.set({ sftpPanelOpen: true }));

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
    toolbar.append(btnNewSession, btnNewFolder, btnSftp, spacer, gear);
    left.appendChild(toolbar);

    // Tree / search body (scrollable). The tree is always shown in the left
    // panel; the SFTP browser lives in the right-hand column now.
    const treeHost = document.createElement("div");
    treeHost.className = "tree-body";
    left.appendChild(treeHost);
    renderTreeBody(treeHost);

    // Hint shown when the SFTP browser is enabled but no active ready tab
    // exists (Phase 5c task 1). Its rule is unchanged by the layout move;
    // applySftpLayout (below) keeps its display in sync.
    const sftpHint = document.createElement("div");
    sftpHint.className = "sftp-hint";
    sftpHint.textContent = "Connect to a session to open the SFTP browser";
    left.appendChild(sftpHint);

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

    const tabHost = document.createElement("div");
    tabHost.className = "tab-strip-host";
    right.appendChild(tabHost);
    renderTabStrip(tabHost);

    const paneHost = document.createElement("div");
    paneHost.className = "terminal-pane-host";
    right.appendChild(paneHost);
    renderTerminalView(paneHost);

    root.appendChild(right);

    // ---- SFTP right column (right of the terminal) ----
    // The SFTP browser is its own grid column; the session tree on the left
    // is never covered. The column collapses fully (0 px track + display:none)
    // when the panel is not visible.
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

    // Single source of truth (store.sftpPanelVisible): the right column shows
    // only when the setting is on AND the panel is open AND the active tab is
    // ready. It sizes both grid tracks, toggles the column, syncs the
    // left-panel hint, and reveals the toolbar [SFTP] reopen button.
    const applySftpLayout = () => {
        const st = store.getState();
        const panel = sftpPanelVisible(st);
        const readyTab = hasReadyActiveTab(st);
        const w = clampSftpWidth(st.sftpPanelWidth);
        document.documentElement.style.setProperty("--sftp-w", panel ? `${w}px` : "0px");
        document.documentElement.style.setProperty("--sftp-gap", panel ? "4px" : "0px");
        sftpSide.style.display = panel ? "flex" : "none";
        sftpSide.setAttribute("aria-hidden", panel ? "false" : "true");
        sftpHint.style.display = st.settings.sftpBrowserEnabled && !readyTab ? "" : "none";
        btnSftp.style.display =
            st.settings.sftpBrowserEnabled && readyTab && !panel ? "" : "none";
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

    // ---- Bottom monitor bar (plan P004): hostname/CPU/RAM/net/uptime/disk
    // for the active ready tab; replaced the Phase 4c user@host status bar.
    const status = document.createElement("footer");
    status.className = "status-band";
    root.appendChild(status);
    renderMonitorBar(status);
}