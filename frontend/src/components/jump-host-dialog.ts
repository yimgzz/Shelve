// components/jump-host-dialog.ts — saved jump host manager + editor
// (plan P006).
//
// The manager is a modal listing saved jump hosts (name, host:port, user,
// type, masked) with [New] / [Edit] / [Delete] and, in pick mode (opened
// from the session editor's "Manage…" button), a [Use] action per row that
// resolves the promise with the picked saved jump host. Delete confirms via
// the existing `confirm` primitive and shows the number of referencing
// sessions (A8-style destructive-op UX). Passwords never appear in the
// list or read DTOs (master plan §8).

import { AppService, JumpHostService } from "../../bindings/shelve/internal/wailsvc";
import { openDialog, type DialogHandle } from "../ui/dialog";
import { store, type SavedJumpHostDTO } from "../store";
import { confirmDialog } from "./confirm";
import { toast } from "./toasts";

// model.AuthType: AuthPassword=0, AuthKey=1.
const AUTH_PASSWORD = 0;
const AUTH_KEY = 1;

/** A labelled field wrapper with an inline error slot (mirrors the session editor). */
function field(labelText: string, input: HTMLElement, opts?: { hint?: string }): {
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
    if (opts?.hint) {
        const hint = document.createElement("div");
        hint.className = "hint";
        hint.textContent = opts.hint;
        wrap.appendChild(hint);
    }
    wrap.appendChild(err);
    return { wrap, err };
}

/** A native file picker + Browse button that fills a text path input. */
function pathRow(value: string): { input: HTMLInputElement; row: HTMLElement; err: HTMLElement } {
    const row = document.createElement("div");
    row.className = "path-row";

    const input = document.createElement("input");
    input.type = "text";
    input.className = "input";
    input.value = value;
    input.placeholder = "/path/to/id_ed25519";
    input.autocomplete = "off";
    input.spellcheck = false;

    // Legacy fallback: WebKitGTK file inputs expose only the basename
    // (no File.path), so this is used solely when the native binding is
    // unavailable. AppService.PickFile returns the full path.
    const file = document.createElement("input");
    file.type = "file";
    file.style.display = "none";
    const browse = document.createElement("button");
    browse.type = "button";
    browse.className = "btn small";
    browse.textContent = "Browse…";
    browse.addEventListener("click", async () => {
        try {
            const picked = await AppService.PickFile();
            if (picked) {
                input.value = picked;
            }
        } catch {
            file.click(); // binding missing/unavailable: legacy picker
        }
    });
    file.addEventListener("change", () => {
        const f = file.files && file.files[0];
        if (f) {
            input.value = (f as unknown as { path?: string }).path ?? f.name;
        }
    });

    const err = document.createElement("div");
    err.className = "field-error";
    err.style.display = "none";

    row.append(input, browse, err);
    return { input, row, err };
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

export interface JumpHostManagerOptions {
    /** Show a [Use] action per row; the promise resolves with the picked host. */
    pick?: boolean;
}

/**
 * Open the saved jump host manager. In pick mode, resolving with a non-null
 * host means the caller should apply it; otherwise null.
 */
export async function openJumpHostManager(opts: JumpHostManagerOptions = {}): Promise<SavedJumpHostDTO | null> {
    await store.refreshSavedJumpHosts();
    const pick = !!opts.pick;

    const body = document.createElement("div");
    body.className = "cred-manager-body";

    const list = document.createElement("div");
    list.className = "cred-list";

    const empty = document.createElement("div");
    empty.className = "hint";
    empty.textContent = "No saved jump hosts yet — press [+ New] to create one.";

    const footer = document.createElement("div");
    const newBtn = document.createElement("button");
    newBtn.type = "button";
    newBtn.className = "btn";
    newBtn.textContent = "+ New";
    const closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "btn primary";
    closeBtn.textContent = "Close";
    footer.append(newBtn, closeBtn);

    const dialog: DialogHandle<SavedJumpHostDTO | null> = openDialog<SavedJumpHostDTO | null>({
        title: "Jump hosts",
        body,
        footer,
        width: 560,
    });

    let picked: SavedJumpHostDTO | null = null;

    function render(): void {
        list.replaceChildren();
        const hosts = store.getState().savedJumpHosts;
        empty.style.display = hosts.length === 0 ? "" : "none";
        for (const jh of hosts) {
            const row = document.createElement("div");
            row.className = "cred-row";

            const info = document.createElement("div");
            info.className = "cred-info";
            const nameEl = document.createElement("div");
            nameEl.className = "cred-name";
            nameEl.textContent = jh.name;
            const meta = document.createElement("div");
            meta.className = "hint";
            const kind = jh.authType === AUTH_KEY ? "key" : "password";
            const secret = jh.authType === AUTH_KEY ? (jh.keyPath ? jh.keyPath : "key") : "••••••••";
            meta.textContent = `${jh.user}@${jh.host}:${jh.port} — ${kind} — ${secret}`;
            info.append(nameEl, meta);

            const actions = document.createElement("div");
            actions.className = "cred-actions";
            if (pick) {
                const use = document.createElement("button");
                use.type = "button";
                use.className = "btn small primary";
                use.textContent = "Use";
                use.addEventListener("click", () => {
                    picked = jh;
                    dialog.close(jh);
                });
                actions.appendChild(use);
            }
            const edit = document.createElement("button");
            edit.type = "button";
            edit.className = "btn small";
            edit.textContent = "Edit";
            edit.addEventListener("click", () => void editJumpHost(jh));
            actions.appendChild(edit);
            const del = document.createElement("button");
            del.type = "button";
            del.className = "btn small danger";
            del.textContent = "Delete";
            del.addEventListener("click", () => void deleteJumpHost(jh));
            actions.appendChild(del);

            row.append(info, actions);
            list.appendChild(row);
        }
    }

    async function editJumpHost(jh: SavedJumpHostDTO): Promise<void> {
        const saved = await openJumpHostEditor(jh);
        if (saved) {
            await store.refreshSavedJumpHosts();
            render();
        }
    }

    async function deleteJumpHost(jh: SavedJumpHostDTO): Promise<void> {
        let usage = 0;
        try {
            usage = await JumpHostService.Usage(jh.id);
        } catch {
            // Fall back to a generic message when the count cannot be read.
        }
        const message =
            usage > 0
                ? `Delete jump host "${jh.name}"? ${usage} session${usage === 1 ? "" : "s"} reference${usage === 1 ? "s" : ""} it and will keep connecting with their inline jump hosts.`
                : `Delete jump host "${jh.name}"?`;
        const ok = await confirmDialog({ title: "Delete jump host?", message, confirmLabel: "Delete", danger: true });
        if (!ok) {
            return;
        }
        try {
            await JumpHostService.Delete(jh.id);
            await store.refreshSavedJumpHosts();
            render();
            toast("info", `Jump host "${jh.name}" deleted`);
        } catch (err) {
            toast("error", String(err));
        }
    }

    newBtn.addEventListener("click", () => {
        void (async () => {
            const saved = await openJumpHostEditor();
            if (saved) {
                await store.refreshSavedJumpHosts();
                render();
            }
        })();
    });
    closeBtn.addEventListener("click", () => dialog.close(picked));

    body.append(empty, list);
    render();
    return dialog.done;
}

/**
 * Open the create/edit form for one saved jump host. Resolves true when it
 * was saved. Passwords are never prefilled from the vault — editing a
 * password host with a blank password keeps the current one (backend merge
 * rule).
 */
export function openJumpHostEditor(existing?: SavedJumpHostDTO): Promise<boolean> {
    const editing = !!existing;

    const body = document.createElement("div");
    body.className = "editor-body";

    const name = document.createElement("input");
    name.type = "text";
    name.className = "input";
    name.autocomplete = "off";
    name.value = editing ? existing!.name : "";
    const nameF = field("Name *", name);

    const host = document.createElement("input");
    host.type = "text";
    host.className = "input mono";
    host.autocomplete = "off";
    host.spellcheck = false;
    host.value = editing ? existing!.host : "";
    const hostF = field("Host *", host);

    const port = document.createElement("input");
    port.type = "number";
    port.className = "input";
    port.min = "1";
    port.max = "65535";
    port.value = String(editing ? existing!.port : 22);
    const portF = field("Port", port);

    const user = document.createElement("input");
    user.type = "text";
    user.className = "input";
    user.autocomplete = "off";
    user.value = editing ? existing!.user : "";
    const userF = field("User *", user);

    const authName = `jh-auth-${Math.random().toString(36).slice(2)}`;
    const defaultAuth = editing ? existing!.authType : AUTH_PASSWORD;
    const pwRb = radioBtn(authName, defaultAuth === AUTH_PASSWORD, "Password");
    const keyRb = radioBtn(authName, defaultAuth === AUTH_KEY, "SSH key");

    const authRadios = document.createElement("div");
    authRadios.className = "auth-radios";
    authRadios.append(pwRb.el, keyRb.el);

    const pwInput = document.createElement("input");
    pwInput.type = "password";
    pwInput.className = "input";
    pwInput.autocomplete = "new-password";
    pwInput.placeholder = "Password";
    if (editing && defaultAuth === AUTH_PASSWORD && existing!.hasPassword) {
        pwInput.placeholder = "leave blank to keep the current password";
    }
    const pwF = field("Password", pwInput);

    const keyPath = pathRow(editing && existing!.keyPath ? existing!.keyPath : "");
    const keyPathInput = keyPath.input;
    const keyF = field("Key path", keyPath.row, {
        hint: "Key passphrase is asked on connect, never stored",
    });
    keyF.err.style.display = "none";

    const authErr = document.createElement("div");
    authErr.className = "field-error";
    authErr.style.display = "none";

    const authSection = document.createElement("div");
    authSection.className = "field";
    const authLabel = document.createElement("label");
    authLabel.textContent = "Authentication";
    authSection.append(authLabel, authRadios, pwF.wrap, keyF.wrap, authErr);

    const applyAuthMode = () => {
        const key = keyRb.rb.checked;
        pwF.wrap.style.display = key ? "none" : "";
        keyF.wrap.style.display = key ? "" : "none";
    };
    pwRb.rb.addEventListener("change", applyAuthMode);
    keyRb.rb.addEventListener("change", applyAuthMode);
    applyAuthMode();

    body.append(nameF.wrap, hostF.wrap, portF.wrap, userF.wrap, authSection);

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
    saveBtn.textContent = "Save";
    footer.append(status, cancelBtn, saveBtn);

    const dialog: DialogHandle<boolean> = openDialog<boolean>({
        title: editing ? "Edit jump host" : "New jump host",
        body,
        footer,
        backdropClose: true,
        width: 460,
    });

    function clearErrors(): void {
        body.querySelectorAll<HTMLElement>(".field-error").forEach((el) => {
            el.style.display = "none";
            el.textContent = "";
        });
    }

    function mapErrors(errText: string): void {
        clearErrors();
        for (const line of errText.split("\n")) {
            const m = /^savedJumpHost\.([^:]+): (.*)$/.exec(line.trim());
            if (!m) {
                continue;
            }
            const [, fieldPath, rule] = m;
            const show = (el: HTMLElement) => {
                el.style.display = "";
                el.textContent = rule;
            };
            switch (fieldPath) {
                case "name":
                    show(nameF.err);
                    break;
                case "host":
                    show(hostF.err);
                    break;
                case "port":
                    show(portF.err);
                    break;
                case "user":
                    show(userF.err);
                    break;
                case "auth":
                    show(authErr);
                    break;
                default:
                    show(authErr);
            }
        }
    }

    saveBtn.addEventListener("click", async () => {
        clearErrors();
        const authTypeValue = keyRb.rb.checked ? AUTH_KEY : AUTH_PASSWORD;
        const portValue = Number(port.value);
        const input = {
            id: editing ? existing!.id : undefined,
            name: name.value.trim(),
            host: host.value.trim(),
            port: portValue,
            user: user.value.trim(),
            authType: authTypeValue,
            password: authTypeValue === AUTH_PASSWORD ? pwInput.value : undefined,
            keyPath: authTypeValue === AUTH_KEY ? keyPathInput.value.trim() : undefined,
        };
        const localErr: Array<[HTMLElement, string]> = [];
        if (!input.name) {
            localErr.push([nameF.err, "must not be empty"]);
        }
        if (!input.host) {
            localErr.push([hostF.err, "must be a valid hostname, IPv4 or IPv6 address"]);
        }
        if (!Number.isInteger(portValue) || portValue < 1 || portValue > 65535) {
            localErr.push([portF.err, "must be in range 1-65535"]);
        }
        if (!input.user) {
            localErr.push([userF.err, "must not be empty"]);
        }
        if (authTypeValue === AUTH_KEY && !input.keyPath) {
            localErr.push([keyF.err, "keyPath is required when authType is key"]);
        }
        for (const [el, msg] of localErr) {
            el.style.display = "";
            el.textContent = msg;
        }
        if (localErr.length > 0) {
            return;
        }
        saveBtn.disabled = true;
        try {
            if (editing) {
                await JumpHostService.Update(input);
                toast("info", "Jump host updated");
            } else {
                await JumpHostService.Create(input);
                toast("info", "Jump host created");
            }
            dialog.close(true);
        } catch (err) {
            mapErrors(String(err));
            saveBtn.disabled = false;
        }
    });

    cancelBtn.addEventListener("click", () => dialog.close(false));

    return dialog.done;
}