// components/shell.ts — the app shell (master plan §6 Layout).
// Left panel (search header + toolbar + tree/search body), splitter
// (drag, clamp 240–480, persist window.leftWidth), right pane (tab strip
// + #terminal-pane placeholder) and the status-band row. A temporary dev
// hook (window.__dsmDev) exposes a Lock button and QA helpers; removed in 4d.

import { AppService, VaultService } from "../../bindings/dummy-ssh-manager/internal/wailsvc";
import { store, DEFAULT_LEFT_WIDTH, type Settings } from "../store";
import { openContextMenu, type MenuItem } from "./context-menu";
import { showHostKeyPrompt, showKeyPrompt } from "./prompts";
import { toast } from "./toasts";
import { renderSearch } from "./search";
import { renderTreeBody, openNewSession, openNewFolderAt } from "./tree";
import { renderTabStrip, renderTerminalPane } from "./tabs";

const MIN_LEFT = 240;
const MAX_LEFT = 480;
const SAVE_DEBOUNCE_MS = 300;

declare global {
    interface Window {
        /** Dev-only flag; when truthy the shell shows the dev hook. */
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
    toolbar.append(btnNewSession, btnNewFolder);
    left.appendChild(toolbar);

    // Tree / search body (scrollable).
    const treeHost = document.createElement("div");
    treeHost.className = "tree-body";
    left.appendChild(treeHost);
    renderTreeBody(treeHost);

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
    renderTerminalPane(paneHost);

    root.appendChild(right);

    // ---- Status band (placeholder; filled by 4c) ----
    const status = document.createElement("footer");
    status.className = "status-band";
    const statusText = document.createElement("span");
    statusText.textContent = "Ready";
    status.appendChild(statusText);
    root.appendChild(status);

    // ---- Dev hook (remove in 4d) ----
    if (window.__dsmDev) {
        const dev = document.createElement("div");
        dev.className = "dev-banner";
        dev.textContent = "DEV HOOK";
        dev.title = "Phase 4a/4b temporary dev tooling — removed in 4d";
        root.appendChild(dev);

        const devBar = document.createElement("div");
        devBar.className = "toolbar";

        const lockBtn = document.createElement("button");
        lockBtn.type = "button";
        lockBtn.className = "btn small";
        lockBtn.textContent = "🔒 Lock";
        lockBtn.addEventListener("click", () => {
            void VaultService.Lock().catch((err) => toast("error", String(err)));
        });

        const toastBtn = document.createElement("button");
        toastBtn.type = "button";
        toastBtn.className = "btn small";
        toastBtn.textContent = "Toast info";
        toastBtn.addEventListener("click", () => toast("info", "Sample info toast"));

        const hostkeyBtn = document.createElement("button");
        hostkeyBtn.type = "button";
        hostkeyBtn.className = "btn small";
        hostkeyBtn.textContent = "Hostkey prompt";
        hostkeyBtn.addEventListener("click", () =>
            showHostKeyPrompt({
                connID: "dev",
                host: "dev.example.com",
                port: 22,
                keyType: "ssh-ed25519",
                keyB64: "AAAA…",
                fingerprint: "SHA256:FXL4PxPmC2Re1hE1yGm2iG0+3fZ9bqN4wT0xZy7uRlM",
            }),
        );

        const keyBtn = document.createElement("button");
        keyBtn.type = "button";
        keyBtn.className = "btn small";
        keyBtn.textContent = "Key prompt";
        keyBtn.addEventListener("click", () =>
            showKeyPrompt({ connID: "dev", keyPath: "/home/user/.ssh/id_ed25519" }),
        );

        const ctxBtn = document.createElement("button");
        ctxBtn.type = "button";
        ctxBtn.className = "btn small";
        ctxBtn.textContent = "Context menu";
        ctxBtn.addEventListener("click", (e) => {
            const items: MenuItem[] = [
                { label: "New Session", action: () => openNewSession("") },
                { label: "New Folder", action: () => openNewFolderAt("") },
                { label: "Danger action", danger: true, action: () => toast("info", "danger action") },
                { label: "Disabled", disabled: true, action: () => undefined },
            ];
            openContextMenu(e.clientX, e.clientY, items);
        });

        devBar.append(lockBtn, toastBtn, hostkeyBtn, keyBtn, ctxBtn);
        root.appendChild(devBar);
    }
}