// components/gear.ts — the left-panel toolbar gear menu (Phase 4d task 2;
// master plan §6 Layout "…(gear)"). Items: [Settings…], [Lock vault…],
// [About]. Lock is the real vault lock: confirm only when tabs are `ready`
// (their connections would be dropped); otherwise it locks immediately.

import { AppService, VaultService } from "../rpc";
import { store } from "../store";
import { openContextMenu } from "./context-menu";
import { openSettingsDialog } from "./settings-dialog";
import { openCredentialManager } from "./credential-dialog";
import { openJumpHostManager } from "./jump-host-dialog";
import { openExportDialog, openImportDialog } from "./transfer-dialog";
import { confirmDialog } from "./confirm";
import { toast } from "./toasts";
import { openDialog } from "../ui/dialog";

/** Open the gear menu anchored near the toolbar gear button. */
export function openGearMenu(x: number, y: number): void {
    openContextMenu(x, y, [
        { label: "Settings…", action: () => openSettingsDialog() },
        { label: "Credentials…", action: () => void openCredentialManager() },
        { label: "Jump hosts…", action: () => void openJumpHostManager() },
        { label: "Export configuration…", action: () => void openExportDialog() },
        { label: "Import configuration…", action: () => void openImportDialog() },
        { label: "Lock vault…", action: () => void lockVault() },
        { label: "About", action: () => void showAbout() },
    ]);
}

/**
 * Lock the vault. If any tab is in `ready` state, confirm first (those
 * connections will be closed). Otherwise lock immediately with no dialog.
 * The actual teardown (terminals destroyed, store reset, unlock gate) runs
 * in main.ts on the resulting `vault:state-changed` event (the 4a switch).
 */
async function lockVault(): Promise<void> {
    const ready = store.getState().tabs.filter((t) => t.state === "ready").length;
    if (ready > 0) {
        const ok = await confirmDialog({
            title: "Lock the vault?",
            message: `${ready} active connection${ready === 1 ? "" : "s"} will be closed. Lock the vault?`,
            confirmLabel: "Lock",
            danger: true,
        });
        if (!ok) {
            return;
        }
    }
    try {
        await VaultService.Lock();
    } catch (err) {
        toast("error", String(err));
    }
}

/** Small About dialog: app name + version (AppService.GetVersion). */
async function showAbout(): Promise<void> {
    let version = "";
    try {
        version = await AppService.GetVersion();
    } catch {
        version = "";
    }

    const body = document.createElement("div");
    body.className = "about-body";
    const name = document.createElement("div");
    name.className = "about-name";
    name.textContent = "Shelve";
    const ver = document.createElement("div");
    ver.className = "hint";
    ver.textContent = version ? `Version ${version}` : "";
    const desc = document.createElement("div");
    desc.className = "hint";
    desc.textContent = "A lightweight, fully local SSH session manager.";
    body.append(name, ver, desc);

    const footer = document.createElement("div");
    const closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "btn primary";
    closeBtn.textContent = "Close";
    footer.appendChild(closeBtn);

    const dlg = openDialog<void>({ title: "About", body, footer, width: 360, showClose: false });
    closeBtn.addEventListener("click", () => dlg.close());
    void dlg.done;
}