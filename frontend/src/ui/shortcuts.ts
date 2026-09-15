// ui/shortcuts.ts — global keyboard shortcut router (Phase 4d task 5;
// master plan §6 Shortcuts). One document-level keydown listener installed
// at boot. Registers the FULL master-plan table.
//
// All chords are matched on KeyboardEvent.code (the physical US position),
// which is stable under any keyboard layout — `key`/`keyCode` are
// layout-mapped and break off English layouts (plan P008, D1/D5).
//
// Typing rule: while any form field (input/textarea/select/contenteditable)
// has focus, ALL shortcuts are suppressed (the router returns early). Esc is
// intentionally NOT handled here — the modal (dialog.ts), search box and
// context menu already close on Esc, so handling it again would double-fire.
//
// The router only acts while the vault is unlocked (no tree/tabs when locked).

import { AppService } from "../rpc";
import { store } from "../store";
import { focusSearch } from "../components/search";
import {
    connectSession,
    openNewSession,
    findTreeNode,
    renameSelectedNode,
    deleteSelectedNode,
} from "../components/tree";
import { openSettingsDialog } from "../components/settings-dialog";
import { toast } from "../components/toasts";

let initialized = false;

/** True when the focus target is an editable form field (typing rule). */
function isTypingTarget(t: EventTarget | null): boolean {
    if (!(t instanceof HTMLElement)) {
        return false;
    }
    const tag = t.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || t.isContentEditable;
}

/** Cycle active tab by `dir` (±1), wrapping around the strip order. */
function cycleTab(dir: number): void {
    const { tabs, activeTabID } = store.getState();
    if (tabs.length === 0) {
        return;
    }
    const idx = tabs.findIndex((t) => t.id === activeTabID);
    const next = (idx + dir + tabs.length) % tabs.length;
    store.activateTab(tabs[next].id);
}

/** Activate the nth tab (1-based). */
function activateNth(n: number): void {
    const { tabs } = store.getState();
    const tab = tabs[n - 1];
    if (tab) {
        store.activateTab(tab.id);
    }
}

/** Ctrl+W: close the active tab with no confirmation (master A3). */
function closeActiveTab(): void {
    const { activeTabID } = store.getState();
    if (activeTabID) {
        void store.closeTab(activeTabID);
    }
}

/** Ctrl+T: connect the selected session, or open a new-session draft. */
function connectOrCreate(): void {
    const { tree, selectedID } = store.getState();
    const node = selectedID ? findTreeNode(tree, selectedID) : null;
    if (node && node.kind === "session") {
        connectSession(node.id);
    } else {
        openNewSession("");
    }
}

/** Ctrl+Shift+E: toggle the SFTP browser setting (mirrors the dialog box). */
async function toggleSftp(): Promise<void> {
    const s = store.getState().settings;
    const next = { ...s, sftpBrowserEnabled: !s.sftpBrowserEnabled };
    store.set({ settings: next });
    try {
        await AppService.SaveSettings(next);
        toast("info", next.sftpBrowserEnabled ? "SFTP browser enabled" : "SFTP browser disabled");
    } catch (err) {
        toast("error", String(err));
    }
}

/** Install the global shortcut router. Call once at boot. */
export function initShortcuts(): void {
    if (initialized) {
        return;
    }
    initialized = true;

    document.addEventListener("keydown", (e) => {
        if (store.getState().vaultState !== "unlocked") {
            return;
        }
        // Typing rule: while a form field has focus, suppress everything
        // (Esc is handled by the components themselves, not here).
        if (isTypingTarget(e.target)) {
            return;
        }

        const ctrl = e.ctrlKey || e.metaKey;
        const shift = e.shiftKey;
        const alt = e.altKey;
        const noMods = !ctrl && !shift && !alt;
        // Physical US position — layout-stable (plan P008).
        const code = e.code;

        // Ctrl+K / Ctrl+L → focus search.
        if (ctrl && !shift && !alt && (code === "KeyK" || code === "KeyL")) {
            e.preventDefault();
            focusSearch();
            return;
        }

        // Ctrl+T → connect selected / new-session draft.
        if (ctrl && !shift && !alt && code === "KeyT") {
            e.preventDefault();
            connectOrCreate();
            return;
        }

        // Ctrl+W → close active tab.
        if (ctrl && !shift && !alt && code === "KeyW") {
            e.preventDefault();
            closeActiveTab();
            return;
        }

        // Ctrl+, → settings dialog.
        if (ctrl && !shift && !alt && code === "Comma") {
            e.preventDefault();
            openSettingsDialog();
            return;
        }

        // Ctrl+Tab / Ctrl+Shift+Tab → cycle tabs.
        if (ctrl && code === "Tab") {
            e.preventDefault();
            cycleTab(shift ? -1 : 1);
            return;
        }

        // Ctrl+1…9 → activate nth tab (main row or numpad, as before).
        const digit = /^Digit([1-9])$/.exec(code) ?? /^Numpad([1-9])$/.exec(code);
        if (ctrl && !shift && !alt && digit) {
            e.preventDefault();
            activateNth(Number(digit[1]));
            return;
        }

        // F2 → rename selected tree node.
        if (noMods && code === "F2") {
            e.preventDefault();
            renameSelectedNode();
            return;
        }

        // Delete → delete selected tree node (A8 confirm inside).
        if (noMods && code === "Delete") {
            e.preventDefault();
            deleteSelectedNode();
            return;
        }

        // Ctrl+Shift+E → toggle SFTP browser.
        if (ctrl && shift && !alt && code === "KeyE") {
            e.preventDefault();
            void toggleSftp();
        }
    });
}