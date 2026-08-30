// components/shell.ts — the app shell (master plan §6 Layout).
// Left panel (search placeholder + toolbar), splitter (drag, clamp
// 240–480, persist window.leftWidth), right pane (empty state) and the
// status-band row. A temporary dev hook (window.__dsmDev) exposes a Lock
// button and QA helpers; it is removed in 4d.

import { AppService, VaultService } from "../../bindings/dummy-ssh-manager/internal/wailsvc";
import { store, DEFAULT_LEFT_WIDTH, type Settings } from "../store";
import { openContextMenu, type MenuItem } from "./context-menu";
import { showHostKeyPrompt, showKeyPrompt } from "./prompts";
import { toast } from "./toasts";

const MIN_LEFT = 240;
const MAX_LEFT = 480;
const SAVE_DEBOUNCE_MS = 300;

declare global {
    interface Window {
        /** Dev-only flag; when truthy the shell shows the dev hook. */
        __dsmDev?: boolean;
    }
}

/** Store intent no-ops until the tree/editor land in 4b. */
export function requestNewSession(_parentID: string | null): void {
    console.debug("[shell] requestNewSession intent (implemented in 4b)", _parentID);
}
export function requestNewFolder(_parentID: string | null): void {
    console.debug("[shell] requestNewFolder intent (implemented in 4b)", _parentID);
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

    // Search placeholder (functional in 4b).
    const search = document.createElement("input");
    search.type = "text";
    search.className = "input";
    search.placeholder = "Search sessions…  Ctrl K";
    search.disabled = true;
    search.style.margin = "8px";
    left.appendChild(search);

    // Toolbar.
    const toolbar = document.createElement("div");
    toolbar.className = "toolbar";
    const btnNewSession = document.createElement("button");
    btnNewSession.type = "button";
    btnNewSession.className = "btn small";
    btnNewSession.textContent = "+ Session";
    btnNewSession.addEventListener("click", () => requestNewSession(null));
    const btnNewFolder = document.createElement("button");
    btnNewFolder.type = "button";
    btnNewFolder.className = "btn small";
    btnNewFolder.textContent = "+ Folder";
    btnNewFolder.addEventListener("click", () => requestNewFolder(null));
    toolbar.append(btnNewSession, btnNewFolder);
    left.appendChild(toolbar);

    // Empty state.
    const empty = document.createElement("div");
    empty.className = "empty-state";
    const emptyTitle = document.createElement("div");
    emptyTitle.textContent = "No sessions yet";
    const emptyHint = document.createElement("div");
    emptyHint.className = "hint";
    emptyHint.textContent = "Create one from the left panel to get started.";
    empty.append(emptyTitle, emptyHint);
    left.appendChild(empty);

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
    const rightEmpty = document.createElement("div");
    rightEmpty.className = "empty-state";
    const rightTitle = document.createElement("div");
    rightTitle.textContent = "No open sessions";
    const rightHint = document.createElement("div");
    rightHint.className = "hint";
    rightHint.textContent = "Create a session from the left panel to open a terminal here.";
    rightEmpty.append(rightTitle, rightHint);
    right.appendChild(rightEmpty);
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
        dev.title = "Phase 4a temporary dev tooling — removed in 4d";
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
                { label: "New Session", action: () => requestNewSession(null) },
                { label: "New Folder", action: () => requestNewFolder(null) },
                { label: "Danger action", danger: true, action: () => toast("info", "danger action") },
                { label: "Disabled", disabled: true, action: () => undefined },
            ];
            openContextMenu(e.clientX, e.clientY, items);
        });

        devBar.append(lockBtn, toastBtn, hostkeyBtn, keyBtn, ctxBtn);
        root.appendChild(devBar);
    }
}