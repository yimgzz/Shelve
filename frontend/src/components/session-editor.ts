// components/session-editor.ts — the session create/edit modal
// (master plan §6 Session editor; Phase 4b task 3).
//
// Builds on the 4a dialog primitive. All fields live in the form DOM;
// Save builds a SessionInput and calls CreateSession/UpdateSession, then
// refreshes the tree. Server/model validation errors are mapped to
// inline field errors ("session.<field>: <rule>"). [Test connection]
// runs against the UNSAVED draft via SessionService.TestConnection
// (host-key/key-passphrase prompt modals may appear mid-test = correct).

import { SessionService } from "../../bindings/shelve/internal/wailsvc";
import { openDialog, type DialogHandle } from "../ui/dialog";
import { store, type SessionDTO, type JumpHostDTO } from "../store";
import { toast } from "./toasts";

// model.AuthType (internal/model/model.go): AuthPassword=0, AuthKey=1.
const AUTH_PASSWORD = 0;
const AUTH_KEY = 1;

export interface SessionEditorOptions {
    /** "create" opens a fresh draft; "edit" prefills from `initial`. */
    mode: "create" | "edit";
    /** Folder to create the session under ("" = root); ignored on edit. */
    parentID: string;
    /** Present in edit mode: the secret-free read view to prefill from. */
    initial?: SessionDTO;
}

export interface JumpHostInput {
    host: string;
    port: number;
    user: string;
    authType: number;
    password?: string;
    keyPath?: string;
}

export interface SessionInput {
    id?: string;
    folderId: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: number;
    password?: string;
    keyPath?: string;
    jumpHosts: JumpHostInput[];
    extraArgs: string;
    /** Per-session SFTP browser start path (blank = global default). Plan P002. */
    sftpInitialPath?: string;
}

const EXTRA_ARGS_HELP =
    "Supported: -L, -D, -o ServerAliveInterval|ServerAliveCountMax|ConnectTimeout|StrictHostKeyChecking, ProxyJump=user@host[:port]";

/** A labelled field wrapper with an inline error slot. */
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

/** A hidden file input + Browse button that fills a text path input. */
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

    const file = document.createElement("input");
    file.type = "file";
    file.style.display = "none";
    const browse = document.createElement("button");
    browse.type = "button";
    browse.className = "btn small";
    browse.textContent = "Browse…";
    browse.addEventListener("click", () => file.click());
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

/**
 * Open the session editor modal. Resolves true if a session was saved.
 */
export function openSessionEditor(opts: SessionEditorOptions): Promise<boolean> {
    const editing = opts.mode === "edit";
    const initial = opts.initial;

    const body = document.createElement("div");
    body.className = "editor-body";

    // ------------------------------------------------------------- fields ---
    const name = document.createElement("input");
    name.type = "text";
    name.className = "input";
    name.autocomplete = "off";
    name.value = editing ? initial!.name : "";
    const nameF = field("Name *", name);

    const host = document.createElement("input");
    host.type = "text";
    host.className = "input";
    host.autocomplete = "off";
    host.value = editing ? initial!.host : "";
    const hostF = field("Host *", host);

    const port = document.createElement("input");
    port.type = "number";
    port.className = "input";
    port.min = "1";
    port.max = "65535";
    port.value = String(editing ? initial!.port : 22);
    const portF = field("Port", port);

    const user = document.createElement("input");
    user.type = "text";
    user.className = "input";
    user.autocomplete = "off";
    user.value = editing ? initial!.user : "";
    const userF = field("User *", user);

    // ---- Auth ----
    const authName = `auth-${Math.random().toString(36).slice(2)}`;
    const defaultAuth = editing ? initial!.authType : AUTH_PASSWORD;
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
    if (editing && defaultAuth === AUTH_PASSWORD && initial!.hasPassword) {
        pwInput.placeholder = "leave blank to keep the current password";
    }
    const pwF = field("Password", pwInput);

    const keyPath = pathRow(editing && initial!.keyPath ? initial!.keyPath : "");
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

    // ---- Jump hosts ----
    const jumpSection = document.createElement("div");
    jumpSection.className = "jump-section";
    const jumpTitle = document.createElement("div");
    jumpTitle.className = "section-label";
    jumpTitle.textContent = "Jump hosts";
    const jumpRows = document.createElement("div");
    jumpRows.className = "jump-rows";
    const addJump = document.createElement("button");
    addJump.type = "button";
    addJump.className = "btn small";
    addJump.textContent = "+ Add jump host";
    jumpSection.append(jumpTitle, jumpRows, addJump);

    interface JumpHandles {
        el: HTMLElement;
        host: HTMLInputElement;
        port: HTMLInputElement;
        user: HTMLInputElement;
        pw: HTMLInputElement;
        keyRadio: HTMLInputElement;
        pwRadio: HTMLInputElement;
        keyPath: HTMLInputElement;
    }
    const jumpHandles: JumpHandles[] = [];

    function makeJumpRow(j?: JumpHostDTO): void {
        const jt = j ? j.authType : AUTH_PASSWORD;
        const rowName = `jump-auth-${Math.random().toString(36).slice(2)}`;

        const host = document.createElement("input");
        host.type = "text";
        host.className = "input";
        host.value = j ? j.host : "";
        const port = document.createElement("input");
        port.type = "number";
        port.className = "input";
        port.min = "1";
        port.max = "65535";
        port.value = String(j ? j.port : 22);
        const user = document.createElement("input");
        user.type = "text";
        user.className = "input";
        user.value = j ? j.user : "";

        const pwRb = radioBtn(rowName, jt === AUTH_PASSWORD, "Password");
        const keyRb = radioBtn(rowName, jt === AUTH_KEY, "Key");
        const pw = document.createElement("input");
        pw.type = "password";
        pw.className = "input";
        pw.autocomplete = "new-password";
        pw.placeholder = "Password";
        if (j && j.authType === AUTH_PASSWORD && j.hasPassword) {
            pw.placeholder = "leave blank to keep the current password";
        }
        const keyPath = pathRow(j && j.keyPath ? j.keyPath : "");
        const keyPathInput = keyPath.input;

        const authBox = document.createElement("div");
        authBox.className = "jump-auth";
        authBox.append(pwRb.el, keyRb.el, pw, keyPath.row);

        const row = document.createElement("div");
        row.className = "jump-row";
        const err = document.createElement("div");
        err.className = "field-error";
        err.style.display = "none";
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "btn small danger";
        remove.textContent = "×";
        remove.title = "Remove jump host";

        row.append(gridField("Host", host), gridField("Port", port), gridField("User", user), authBox, remove, err);
        jumpRows.appendChild(row);

        const sync = () => {
            const key = keyRb.rb.checked;
            keyPath.row.style.display = key ? "" : "none";
            pw.style.display = key ? "none" : "";
            keyRb.el.style.display = key ? "" : "none";
            pwRb.el.style.display = key ? "none" : "";
        };
        pwRb.rb.addEventListener("change", sync);
        keyRb.rb.addEventListener("change", sync);
        sync();

        const h: JumpHandles = { el: row, host, port, user, pw, keyRadio: keyRb.rb, pwRadio: pwRb.rb, keyPath: keyPathInput };
        remove.addEventListener("click", () => {
            row.remove();
            const i = jumpHandles.indexOf(h);
            if (i >= 0) {
                jumpHandles.splice(i, 1);
            }
        });
        jumpHandles.push(h);
    }

    for (const j of (editing ? initial!.jumpHosts : [])) {
        makeJumpRow(j);
    }
    addJump.addEventListener("click", () => makeJumpRow());

    // ---- Extra Args ----
    const extraArgs = document.createElement("input");
    extraArgs.type = "text";
    extraArgs.className = "input mono";
    extraArgs.autocomplete = "off";
    extraArgs.spellcheck = false;
    extraArgs.value = editing ? initial!.extraArgs : "";
    const extraF = field("Extra Args", extraArgs, { hint: EXTRA_ARGS_HELP });
    const extraErr = extraF.err;
    extraArgs.addEventListener("blur", () => {
        const v = extraArgs.value.trim();
        void SessionService.ValidateExtraArgs(v).then(
            () => {
                extraErr.style.display = "none";
                extraErr.textContent = "";
            },
            (err: unknown) => {
                extraErr.style.display = "";
                extraErr.textContent = String(err);
            },
        );
    });

    // ---- SFTP start path (plan P002) ----
    const sftpPath = document.createElement("input");
    sftpPath.type = "text";
    sftpPath.className = "input mono";
    sftpPath.autocomplete = "off";
    sftpPath.spellcheck = false;
    sftpPath.placeholder = "~/data (blank = global default)";
    sftpPath.value = editing && initial!.sftpInitialPath ? initial!.sftpInitialPath : "";
    const sftpPathF = field("SFTP start path", sftpPath, {
        hint: "Directory the SFTP browser opens in for this session. Absolute or ~-relative; blank uses the global default.",
    });

    body.append(nameF.wrap, hostF.wrap, portF.wrap, userF.wrap, authSection, jumpSection, extraF.wrap, sftpPathF.wrap);

    // ---- Footer ----
    const footer = document.createElement("div");
    const status = document.createElement("span");
    status.className = "editor-status";
    const testBtn = document.createElement("button");
    testBtn.type = "button";
    testBtn.className = "btn";
    testBtn.textContent = "Test connection";
    const cancelBtn = document.createElement("button");
    cancelBtn.type = "button";
    cancelBtn.className = "btn";
    cancelBtn.textContent = "Cancel";
    const saveBtn = document.createElement("button");
    saveBtn.type = "button";
    saveBtn.className = "btn primary";
    saveBtn.textContent = "Save";
    footer.append(status, testBtn, cancelBtn, saveBtn);

    const dialog: DialogHandle<boolean> = openDialog<boolean>({
        title: editing ? "Edit session" : "New session",
        body,
        footer,
        backdropClose: true,
        width: 560,
    });

    // ------------------------------------------------------------- build ---
    function clearErrors(): void {
        body.querySelectorAll<HTMLElement>(".field-error").forEach((el) => {
            el.style.display = "none";
            el.textContent = "";
        });
    }

    function collect(): { input: SessionInput; valid: boolean } {
        clearErrors();
        const authTypeValue = keyRb.rb.checked ? AUTH_KEY : AUTH_PASSWORD;
        const input: SessionInput = {
            id: editing ? initial!.id : undefined,
            folderId: editing ? initial!.folderId : opts.parentID,
            name: name.value.trim(),
            host: host.value.trim(),
            port: Number(port.value) || 0,
            user: user.value.trim(),
            authType: authTypeValue,
            password: authTypeValue === AUTH_PASSWORD ? pwInput.value : undefined,
            keyPath: authTypeValue === AUTH_KEY ? keyPathInput.value.trim() : undefined,
            jumpHosts: jumpHandles.map((h) => ({
                host: h.host.value.trim(),
                port: Number(h.port.value) || 0,
                user: h.user.value.trim(),
                authType: h.keyRadio.checked ? AUTH_KEY : AUTH_PASSWORD,
                password: h.keyRadio.checked ? undefined : h.pw.value,
                keyPath: h.keyRadio.checked ? h.keyPath.value.trim() : undefined,
            })),
            extraArgs: extraArgs.value.trim(),
            sftpInitialPath: sftpPath.value.trim(),
        };
        const localErr: Array<[HTMLElement, string]> = [];
        if (!input.name) {
            localErr.push([nameF.err, "must not be empty"]);
        }
        if (!input.host) {
            localErr.push([hostF.err, "must be a valid hostname, IPv4 or IPv6 address"]);
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
        return { input, valid: localErr.length === 0 };
    }

    /** Map backend ValidationError lines ("session.<field>: rule") to fields. */
    function mapErrors(errText: string): void {
        clearErrors();
        for (const line of errText.split("\n")) {
            const m = /^session\.([^:]+): (.*)$/.exec(line.trim());
            if (!m) {
                continue;
            }
            const [, fieldPath, rule] = m;
            const show = (el: HTMLElement) => {
                el.style.display = "";
                el.textContent = rule;
            };
            const jumpMatch = /^jumpHosts\[(\d+)\]\.(.*)$/.exec(fieldPath);
            if (jumpMatch) {
                const row = jumpHandles[Number(jumpMatch[1])];
                if (row) {
                    show(row.el.querySelector<HTMLElement>(".field-error")!);
                }
                continue;
            }
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
                case "extraArgs":
                    show(extraF.err);
                    break;
                default:
                    show(authErr);
            }
        }
    }

    const busy = (on: boolean) => {
        saveBtn.disabled = on;
        testBtn.disabled = on;
    };

    saveBtn.addEventListener("click", async () => {
        const { input, valid } = collect();
        if (!valid) {
            return;
        }
        busy(true);
        try {
            if (editing) {
                await SessionService.UpdateSession(input);
            } else {
                await SessionService.CreateSession(input);
            }
            dialog.close(true);
            void store.refreshTree();
            toast("info", editing ? "Session updated" : "Session created");
        } catch (err) {
            mapErrors(String(err));
        } finally {
            busy(false);
        }
    });

    cancelBtn.addEventListener("click", () => dialog.close(false));

    testBtn.addEventListener("click", async () => {
        const { input, valid } = collect();
        if (!valid) {
            return;
        }
        status.textContent = "Testing…";
        status.classList.remove("error");
        testBtn.disabled = true;
        try {
            await SessionService.TestConnection(input);
            status.textContent = "Connection OK";
            status.classList.remove("error");
        } catch (err) {
            status.textContent = String(err);
            status.classList.add("error");
        } finally {
            testBtn.disabled = false;
        }
    });

    return dialog.done;
}

/** A labelled compact field used inside jump-host rows. */
function gridField(labelText: string, input: HTMLElement): HTMLElement {
    const wrap = document.createElement("label");
    wrap.className = "grid-field";
    const span = document.createElement("span");
    span.textContent = labelText;
    wrap.append(span, input);
    return wrap;
}