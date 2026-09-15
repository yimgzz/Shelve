// components/transfer-dialog.ts — configuration export & import modals
// (plan config-export-import §6).
//
// Both flows are path-only: the native dialog runs in the Electron main
// process (`window.shelve.pickSaveFile` / `pickOpenFile`) and only the chosen
// path crosses the loopback bridge (`TransferService.Export`/`Import`). The
// passphrase protects the file and is never persisted, logged or echoed back.
// Import defaults to Merge (fresh IDs under a new `Imported <date>` folder);
// Replace swaps the whole tree + settings and therefore confirms first with the
// A8-style destructive-op dialog.

import { TransferService } from "../rpc";
import type { NodeDTO } from "../rpc/types";
import { openDialog, type DialogHandle } from "../ui/dialog";
import { store } from "../store";
import { confirmDialog } from "./confirm";
import { toast } from "./toasts";

const MIN_PASSPHRASE_LENGTH = 8;

/** A labelled field wrapper with an inline error slot (mirrors the editor). */
function field(labelText: string, input: HTMLElement, hint?: string): {
    wrap: HTMLElement;
    err: HTMLElement;
} {
    const wrap = document.createElement("div");
    wrap.className = "field";
    const lbl = document.createElement("label");
    lbl.textContent = labelText;
    const err = document.createElement("div");
    err.className = "field-error";
    err.style.display = "none";
    wrap.append(lbl, input);
    if (hint) {
        const h = document.createElement("div");
        h.className = "hint";
        h.textContent = hint;
        wrap.appendChild(h);
    }
    wrap.appendChild(err);
    return { wrap, err };
}

/** A simple radio button label (input + text). */
function radioBtn(name: string, checked: boolean, labelText: string): { rb: HTMLInputElement; el: HTMLLabelElement } {
    const rb = document.createElement("input");
    rb.type = "radio";
    rb.name = name;
    rb.checked = checked;
    const el = document.createElement("label");
    el.className = "radio";
    const span = document.createElement("span");
    span.textContent = labelText;
    el.append(rb, span);
    return { rb, el };
}

function passphraseInput(): HTMLInputElement {
    const input = document.createElement("input");
    input.type = "password";
    input.className = "input";
    input.autocomplete = "new-password";
    input.spellcheck = false;
    return input;
}

/** Default export filename: shelve-config-YYYY-MM-DD.shelve. */
function defaultExportName(): string {
    const d = new Date();
    const pad = (n: number): string => String(n).padStart(2, "0");
    return `shelve-config-${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}.shelve`;
}

/** Count folders/sessions in the current tree (for the Replace confirm). */
function countTree(nodes: NodeDTO[]): { folders: number; sessions: number } {
    let folders = 0;
    let sessions = 0;
    const walk = (ns: NodeDTO[]): void => {
        for (const n of ns) {
            if (n.kind === "folder") {
                folders++;
            } else {
                sessions++;
            }
            walk(n.children);
        }
    };
    walk(nodes);
    return { folders, sessions };
}

/**
 * Open the export modal. Resolves true once a file was written. The flow is:
 * validate the passphrase, pick a save path, encrypt and write.
 */
export function openExportDialog(): Promise<boolean> {
    const body = document.createElement("div");
    body.className = "editor-body";

    const hint = document.createElement("p");
    hint.className = "hint";
    hint.textContent =
        "The exported file is encrypted with its own passphrase, independent of the vault master password. Keep it safe — it cannot be recovered.";
    body.appendChild(hint);

    const pw = passphraseInput();
    const pwF = field("Passphrase *", pw, `At least ${MIN_PASSPHRASE_LENGTH} characters`);

    const confirm = passphraseInput();
    const confirmF = field("Confirm passphrase *", confirm);

    body.append(pwF.wrap, confirmF.wrap);

    const footer = document.createElement("div");
    const status = document.createElement("span");
    status.className = "editor-status";
    const cancelBtn = document.createElement("button");
    cancelBtn.type = "button";
    cancelBtn.className = "btn";
    cancelBtn.textContent = "Cancel";
    const saveBtn = document.createElement("button");
    saveBtn.type = "button";
    saveBtn.className = "btn primary";
    saveBtn.textContent = "Export…";
    footer.append(status, cancelBtn, saveBtn);

    const dialog: DialogHandle<boolean> = openDialog<boolean>({
        title: "Export configuration",
        body,
        footer,
        backdropClose: true,
        width: 460,
    });

    function clearErrors(): void {
        for (const el of [pwF.err, confirmF.err]) {
            el.style.display = "none";
            el.textContent = "";
        }
    }

    saveBtn.addEventListener("click", () => {
        void (async () => {
            clearErrors();
            let bad = false;
            if (pw.value.length < MIN_PASSPHRASE_LENGTH) {
                pwF.err.style.display = "";
                pwF.err.textContent = `must be at least ${MIN_PASSPHRASE_LENGTH} characters`;
                bad = true;
            }
            if (confirm.value !== pw.value) {
                confirmF.err.style.display = "";
                confirmF.err.textContent = "passphrases do not match";
                bad = true;
            }
            if (bad) {
                return;
            }

            let path = "";
            saveBtn.disabled = true;
            try {
                path = await window.shelve.pickSaveFile(defaultExportName());
            } catch (err) {
                toast("error", String(err));
                saveBtn.disabled = false;
                return;
            }
            if (!path) {
                saveBtn.disabled = false; // cancelled: no-op
                return;
            }
            try {
                await TransferService.Export(path, pw.value);
                dialog.close(true);
                toast("info", "Configuration exported");
            } catch (err) {
                toast("error", String(err));
                saveBtn.disabled = false;
            }
        })();
    });

    cancelBtn.addEventListener("click", () => dialog.close(false));
    return dialog.done;
}

/**
 * Open the import modal. Resolves true once the import was applied. Merge is
 * the default; Replace confirms with the current entity counts before running.
 */
export function openImportDialog(): Promise<boolean> {
    const body = document.createElement("div");
    body.className = "editor-body";

    const hint = document.createElement("p");
    hint.className = "hint";
    hint.textContent =
        "Import a .shelve configuration file. Merge adds the imported data under a new folder; Replace overwrites the entire tree and settings.";

    const pw = passphraseInput();
    const pwF = field("Passphrase *", pw);

    const modeName = `import-mode-${Math.random().toString(36).slice(2)}`;
    const mergeRb = radioBtn(modeName, true, "Merge");
    const replaceRb = radioBtn(modeName, false, "Replace");
    const modes = document.createElement("div");
    modes.className = "auth-radios";
    modes.append(mergeRb.el, replaceRb.el);

    const desc = document.createElement("div");
    desc.className = "hint";
    const updateDesc = (): void => {
        desc.textContent = mergeRb.rb.checked
            ? "Adds everything under a new “Imported” folder. Existing local data is kept."
            : "Replaces the whole tree and settings. All live connections are closed.";
    };
    mergeRb.rb.addEventListener("change", updateDesc);
    replaceRb.rb.addEventListener("change", updateDesc);
    updateDesc();

    const modeSection = document.createElement("div");
    modeSection.className = "field";
    const modeLabel = document.createElement("label");
    modeLabel.textContent = "Import mode";
    modeSection.append(modeLabel, modes, desc);

    body.append(hint, pwF.wrap, modeSection);

    const footer = document.createElement("div");
    const status = document.createElement("span");
    status.className = "editor-status";
    const cancelBtn = document.createElement("button");
    cancelBtn.type = "button";
    cancelBtn.className = "btn";
    cancelBtn.textContent = "Cancel";
    const importBtn = document.createElement("button");
    importBtn.type = "button";
    importBtn.className = "btn primary";
    importBtn.textContent = "Import…";
    footer.append(status, cancelBtn, importBtn);

    const dialog: DialogHandle<boolean> = openDialog<boolean>({
        title: "Import configuration",
        body,
        footer,
        backdropClose: true,
        width: 480,
    });

    importBtn.addEventListener("click", () => {
        void (async () => {
            pwF.err.style.display = "none";
            pwF.err.textContent = "";
            if (!pw.value) {
                pwF.err.style.display = "";
                pwF.err.textContent = "passphrase is required";
                return;
            }
            const mode: "merge" | "replace" = replaceRb.rb.checked ? "replace" : "merge";

            let path = "";
            importBtn.disabled = true;
            try {
                path = await window.shelve.pickOpenFile();
            } catch (err) {
                toast("error", String(err));
                importBtn.disabled = false;
                return;
            }
            if (!path) {
                importBtn.disabled = false; // cancelled: no-op
                return;
            }

            if (mode === "replace") {
                const { folders, sessions } = countTree(store.getState().tree);
                const creds = store.getState().credentials.length;
                const jumps = store.getState().savedJumpHosts.length;
                const ok = await confirmDialog({
                    title: "Replace the configuration?",
                    message:
                        `This replaces ${sessions} session${sessions === 1 ? "" : "s"}, ` +
                        `${folders} folder${folders === 1 ? "" : "s"}, ` +
                        `${creds} credential${creds === 1 ? "" : "s"} and ` +
                        `${jumps} saved jump host${jumps === 1 ? "" : "s"}, and closes all live connections.`,
                    confirmLabel: "Replace",
                    danger: true,
                });
                if (!ok) {
                    importBtn.disabled = false;
                    return;
                }
            }

            try {
                const result = await TransferService.Import(path, pw.value, mode);
                dialog.close(true);
                await store.afterImport(mode);
                toast(
                    "info",
                    `Imported ${result.sessions} session${result.sessions === 1 ? "" : "s"}, ` +
                        `${result.folders} folder${result.folders === 1 ? "" : "s"}, ` +
                        `${result.credentials} credential${result.credentials === 1 ? "" : "s"} and ` +
                        `${result.savedJumpHosts} saved jump host${result.savedJumpHosts === 1 ? "" : "s"}`,
                );
            } catch (err) {
                toast("error", String(err));
                importBtn.disabled = false;
            }
        })();
    });

    cancelBtn.addEventListener("click", () => dialog.close(false));
    return dialog.done;
}
