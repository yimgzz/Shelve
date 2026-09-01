// components/shell.ts — the app shell (master plan §6 Layout).
// Left panel (search header + toolbar + tree/search body), splitter
// (drag, clamp 240–480, persist window.leftWidth), right pane (tab strip
// + terminal panes) and the status-band row. The toolbar hosts the gear
// menu (Phase 4d) that opens Settings / Lock vault / About.

import { AppService } from "../../bindings/shelve/internal/wailsvc";
import {
    store,
    sftpPanelVisible,
    hasReadyActiveTab,
    DEFAULT_LEFT_WIDTH,
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
const SAVE_DEBOUNCE_MS = 300;

declare global {
    interface Window {
        /**
         * Dev-only flag kept for dev tooling (search timing in tree.ts,
         * dev notes). The 4a/4b QA hook UI was removed in 4d.
         */
        __dsmDev?: boolean;
    }
}

/** Persist the current left-panel width (debounced, partial update). */
let saveTimer: number | null = null;
function persistLeftWidth(): void {
    const { settings } = store.getState();
    if (saveTimer !== null) {
        window.clearTimeout(saveTimer);
    }
    saveTimer = window.setTimeout(() => {
        const next: Settings = { ...settings, window: { ...settings.window } };
        void AppService.SaveSettings(next).catch((err) => toast("error", String(err)));
    }, SAVE_DEBOUNCE_MS);
}

/** Render the app shell into the given root element. */
export function renderShell(root: HTMLElement): void {
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

    // [SFTP] (phase 5d D5d-1): switches the left panel back to the SFTP
    // browser when it is hidden behind the session tree. Visible only when
    // the setting is on, a ready tab exists, and the panel isn't shown.
    const btnSftp = document.createElement("button");
    btnSftp.type = "button";
    btnSftp.className = "btn small";
    btnSftp.textContent = "SFTP";
    btnSftp.title = "Open the SFTP browser for the active session";
    btnSftp.addEventListener("click", () => store.set({ leftMode: "sftp" }));

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

    // Tree / search body (scrollable) + a hint shown when the SFTP browser
    // is enabled but no active ready tab exists (Phase 5c task 1).
    const treeHost = document.createElement("div");
    treeHost.className = "tree-body";
    left.appendChild(treeHost);
    renderTreeBody(treeHost);

    const sftpHint = document.createElement("div");
    sftpHint.className = "sftp-hint";
    sftpHint.textContent = "Connect to a session to open the SFTP browser";

    // SFTP panel host (always mounted; display toggled by the visibility rule).
    const sftpHost = document.createElement("div");
    sftpHost.className = "sftp-host";
    left.append(sftpHint, sftpHost);
    renderSftpPanel(sftpHost);

    // Single source of truth (store.sftpPanelVisible): the panel replaces
    // the tree only when the setting is on AND leftMode is "sftp" AND the
    // active tab is ready (phase 5d D5d-3). The hint shows when the setting
    // is on but no ready tab exists; the toolbar [SFTP] button shows when a
    // ready tab exists but the panel is hidden behind the tree.
    const applyLeftMode = () => {
        const st = store.getState();
        const panel = sftpPanelVisible(st);
        const readyTab = hasReadyActiveTab(st);
        treeHost.style.display = panel ? "none" : "";
        sftpHost.style.display = panel ? "" : "none";
        sftpHint.style.display = st.settings.sftpBrowserEnabled && !readyTab ? "" : "none";
        btnSftp.style.display =
            st.settings.sftpBrowserEnabled && readyTab && !panel ? "" : "none";
    };
    const leftModeUnsub = store.subscribe(applyLeftMode);
    applyLeftMode();
    void leftModeUnsub; // kept alive for the shell's lifetime

    root.appendChild(left);

    // ---- Splitter ----
    const splitter = document.createElement("div");
    splitter.className = "splitter";
    splitter.setAttribute("role", "separator");
    splitter.setAttribute("aria-orientation", "vertical");
    root.appendChild(splitter);

    const applyWidth = (w: number) => {
        const clamped = Math.min(MAX_LEFT, Math.max(MIN_LEFT, w));
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
            persistLeftWidth();
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

    // ---- Bottom monitor bar (plan P004): hostname/CPU/RAM/net/uptime/disk
    // for the active ready tab; replaced the Phase 4c user@host status bar.
    const status = document.createElement("footer");
    status.className = "status-band";
    root.appendChild(status);
    renderMonitorBar(status);
}