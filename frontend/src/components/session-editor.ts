// components/session-editor.ts — the session create/edit modal
// (master plan §6 Session editor; Phase 4b task 3).
//
// Builds on the 4a dialog primitive. All fields live in the form DOM;
// Save builds a SessionInput and calls CreateSession/UpdateSession, then
// refreshes the tree. Server/model validation errors are mapped to
// inline field errors ("session.<field>: <rule>"). [Test connection]
// runs against the UNSAVED draft via SessionService.TestConnection
// (host-key/key-passphrase prompt modals may appear mid-test = correct).

import { SessionService } from "../rpc";
import { openDialog, type DialogHandle } from "../ui/dialog";
import { store, type SessionDTO, type JumpHostDTO, type CredentialDTO, type SavedJumpHostDTO } from "../store";
import { toast } from "./toasts";
import { openCredentialManager } from "./credential-dialog";
import { openJumpHostManager } from "./jump-host-dialog";

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
    // Present-but-maybe-undefined, matching the rpc DTO model.
    password: string | undefined;
    keyPath: string | undefined;
    /** Bastion-style hop (plan P009): last handshake, target-embedding. */
    bastion: boolean;
}

export interface SessionInput {
    // The rpc SessionInput model declares id/sftpInitialPath/
    // credentialId as present-but-maybe-undefined members, so the local
    // draft type matches ("" = absent/global-default).
    id: string;
    folderId: string;
    name: string;
    host: string;
    port: number;
    user: string;
    authType: number;
    password: string | undefined;
    keyPath: string | undefined;
    jumpHosts: JumpHostInput[];
    extraArgs: string;
    /** Per-session SFTP browser start path ("" = global default). Plan P002. */
    sftpInitialPath: string;
    /** Optional reference to a saved credential ("" = none). Plan P003. */
    credentialId: string;
    /**
     * Optional reference to a saved jump host ("" = none). While set, the
     * backend replaces the whole inline chain with the saved host's hop at
     * connect/test time (plan P006).
     */
    jumpHostRef: string;
}

const EXTRA_ARGS_HELP =
    "Supported: -L, -D, -o ServerAliveInterval|ServerAliveCountMax|ConnectTimeout|StrictHostKeyChecking, ProxyJump=user@host[:port]";

// Plan P009: bastion-style jump host UI copy.
const BASTION_TOOLTIP = "Route this session's target through this bastion as login user@target";
const BASTION_ROW_HINT =
    "Session Host is reached through this bastion; target login = bastion login; target port must be 22 (v1).";

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
    // unavailable. window.shelve.pickFile returns the full path.
    const file = document.createElement("input");
    file.type = "file";
    file.style.display = "none";
    const browse = document.createElement("button");
    browse.type = "button";
    browse.className = "btn small";
    browse.textContent = "Browse…";
    browse.addEventListener("click", async () => {
        try {
            const picked = await window.shelve.pickFile();
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
    // Plan P009: shown while a bastion jump host is active (target must be 22).
    const portBastionHint = document.createElement("div");
    portBastionHint.className = "hint";
    portBastionHint.style.display = "none";
    portBastionHint.textContent = "Must be 22 while a bastion jump host is active (v1).";
    portF.wrap.appendChild(portBastionHint);

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

    // Plan P009: static note shown in place of the target credentials while
    // a bastion jump host is active (the bastion relays target auth; the
    // saved values are preserved but unused).
    const bastionAuthNote = document.createElement("div");
    bastionAuthNote.className = "hint";
    bastionAuthNote.style.display = "none";
    bastionAuthNote.textContent =
        "Bastion handles target authentication — you will be prompted when connecting (your saved bastion password prefills the prompt).";

    const authSection = document.createElement("div");
    authSection.className = "field";
    const authLabel = document.createElement("label");
    authLabel.textContent = "Authentication";
    authSection.append(authLabel, authRadios, pwF.wrap, keyF.wrap, bastionAuthNote, authErr);

    const applyAuthMode = () => {
        const key = keyRb.rb.checked;
        pwF.wrap.style.display = key ? "none" : "";
        keyF.wrap.style.display = key ? "" : "none";
    };
    pwRb.rb.addEventListener("change", applyAuthMode);
    keyRb.rb.addEventListener("change", applyAuthMode);
    applyAuthMode();

    // ---- Saved credential (plan P003 §4.4) ----
    // A dropdown of named credentials. Picking one prefills User+Auth and
    // sets credentialId on the saved input; the backend keeps the
    // credential authoritative at connect time while the reference is set.
    const credSelect = document.createElement("select");
    credSelect.className = "input";
    const manageCredsBtn = document.createElement("button");
    manageCredsBtn.type = "button";
    manageCredsBtn.className = "btn small";
    manageCredsBtn.textContent = "Manage…";

    let credentialId = editing ? initial!.credentialId || "" : "";

    function fillCredentialOptions(selectedID: string): void {
        credSelect.replaceChildren();
        const none = document.createElement("option");
        none.value = "";
        none.textContent = "None (inline credentials)";
        credSelect.appendChild(none);
        for (const c of store.getState().credentials) {
            const opt = document.createElement("option");
            opt.value = c.id;
            opt.textContent = `${c.name} — ${c.user} (${c.authType === AUTH_KEY ? "key" : "password"})`;
            credSelect.appendChild(opt);
        }
        credSelect.value = selectedID;
    }

    /** Apply a picked credential: prefill User+Auth, set credentialId. */
    function applyCredential(cred: CredentialDTO | null): void {
        if (!cred) {
            credentialId = "";
            pwInput.placeholder = "Password";
            return;
        }
        credentialId = cred.id;
        user.value = cred.user;
        if (cred.authType === AUTH_KEY) {
            pwRb.rb.checked = false;
            keyRb.rb.checked = true;
            keyPathInput.value = cred.keyPath || "";
            keyPathInput.placeholder = "/path/to/id_ed25519";
        } else {
            keyRb.rb.checked = false;
            pwRb.rb.checked = true;
            pwInput.value = "";
            pwInput.placeholder = "from saved credential (masked)";
        }
        applyAuthMode();
    }

    credSelect.addEventListener("change", () => {
        const id = credSelect.value;
        const cred = id ? store.getState().credentials.find((c) => c.id === id) || null : null;
        applyCredential(cred);
    });

    manageCredsBtn.addEventListener("click", async () => {
        const picked = await openCredentialManager({ pick: true });
        await store.refreshCredentials();
        if (picked) {
            fillCredentialOptions(picked.id);
            applyCredential(picked);
        } else {
            fillCredentialOptions(credentialId);
        }
    });

    fillCredentialOptions(credentialId);
    if (credentialId) {
        const cred = store.getState().credentials.find((c) => c.id === credentialId);
        if (cred && cred.authType === AUTH_PASSWORD) {
            pwInput.placeholder = "from saved credential (masked)";
        }
    }

    const credRow = document.createElement("div");
    credRow.className = "cred-pick-row";
    credRow.append(credSelect, manageCredsBtn);

    // ---- Jump hosts ----
    const jumpSection = document.createElement("div");
    jumpSection.className = "jump-section";
    const jumpTitle = document.createElement("div");
    jumpTitle.className = "section-label";
    jumpTitle.textContent = "Jump hosts";
    // Saved jump host picker (plan P006): picking one makes it the
    // session's only hop (authoritative at connect time); "None" restores
    // the editable inline chain.
    const pickRow = document.createElement("div");
    pickRow.className = "cred-pick-row";
    const jhSelect = document.createElement("select");
    jhSelect.className = "input";
    const manageJhBtn = document.createElement("button");
    manageJhBtn.type = "button";
    manageJhBtn.className = "btn small";
    manageJhBtn.textContent = "Manage…";
    pickRow.append(jhSelect, manageJhBtn);
    const jhHint = document.createElement("div");
    jhHint.className = "hint";
    jhHint.style.display = "none";
    const jumpRows = document.createElement("div");
    jumpRows.className = "jump-rows";
    const addJump = document.createElement("button");
    addJump.type = "button";
    addJump.className = "btn small";
    addJump.textContent = "+ Add jump host";
    jumpSection.append(jumpTitle, pickRow, jhHint, jumpRows, addJump);

    interface JumpHandles {
        el: HTMLElement;
        host: HTMLInputElement;
        port: HTMLInputElement;
        user: HTMLInputElement;
        pw: HTMLInputElement;
        keyRadio: HTMLInputElement;
        pwRadio: HTMLInputElement;
        keyPath: HTMLInputElement;
        /** Bastion checkbox (plan P009): routes the target through this hop. */
        bastion: HTMLInputElement;
        /** The row-level inline error slot (shown in either auth mode). */
        err: HTMLElement;
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

        // Plan P009: bastion checkbox — routes this session's target through
        // this hop as login user@target.
        const bastion = document.createElement("input");
        bastion.type = "checkbox";
        bastion.className = "settings-check";
        bastion.checked = !!(j && j.bastion);
        bastion.title = BASTION_TOOLTIP;
        const bastionWrap = document.createElement("label");
        bastionWrap.className = "jump-bastion";
        const bastionSpan = document.createElement("span");
        bastionSpan.textContent = "Bastion";
        bastionWrap.append(bastion, bastionSpan);
        const bastionHint = document.createElement("div");
        bastionHint.className = "hint";
        bastionHint.style.display = bastion.checked ? "" : "none";
        bastionHint.textContent = BASTION_ROW_HINT;

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

        row.append(
            gridField("Host", host),
            gridField("Port", port),
            gridField("User", user),
            authBox,
            bastionWrap,
            bastionHint,
            remove,
            err,
        );
        jumpRows.appendChild(row);

        const sync = () => {
            // Both auth radios stay visible at all times so the user can
            // switch between password and key; only the input controls
            // toggle (same pattern as the main auth section).
            const key = keyRb.rb.checked;
            keyPath.row.style.display = key ? "" : "none";
            pw.style.display = key ? "none" : "";
        };
        pwRb.rb.addEventListener("change", sync);
        keyRb.rb.addEventListener("change", sync);
        sync();
        bastion.addEventListener("change", () => {
            bastionHint.style.display = bastion.checked ? "" : "none";
            refreshBastionUI();
        });

        const h: JumpHandles = { el: row, host, port, user, pw, keyRadio: keyRb.rb, pwRadio: pwRb.rb, keyPath: keyPathInput, bastion, err };
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

    // ---- Saved jump host selection (plan P006) ----
    let jumpHostRefId = editing && initial!.jumpHostRef ? initial!.jumpHostRef : "";

    function fillJumpHostOptions(selectedID: string): void {
        jhSelect.replaceChildren();
        const none = document.createElement("option");
        none.value = "";
        none.textContent = "None (inline jump hosts)";
        jhSelect.appendChild(none);
        for (const jh of store.getState().savedJumpHosts) {
            const opt = document.createElement("option");
            opt.value = jh.id;
            opt.textContent = jh.name;
            jhSelect.appendChild(opt);
        }
        jhSelect.value = selectedID;
    }

    function clearJumpRows(): void {
        for (const h of [...jumpHandles]) {
            h.el.remove();
        }
        jumpHandles.length = 0;
    }

    /** Apply a picked saved jump host (replaces the inline chain) or clear it. */
    function applySavedJumpHost(jh: SavedJumpHostDTO | null): void {
        if (!jh) {
            jumpHostRefId = "";
            jhHint.style.display = "none";
            addJump.style.display = "";
            refreshBastionUI();
            return;
        }
        jumpHostRefId = jh.id;
        clearJumpRows();
        makeJumpRow({
            host: jh.host,
            port: jh.port,
            user: jh.user,
            authType: jh.authType,
            hasPassword: jh.hasPassword,
            keyPath: jh.keyPath,
            bastion: !!jh.bastion,
        });
        const row = jumpHandles[0];
        if (row && !row.keyRadio.checked && jh.hasPassword) {
            row.pw.placeholder = "from saved jump host (masked)";
        }
        addJump.style.display = "none";
        jhHint.style.display = "";
        jhHint.textContent = `"${jh.name}" replaces the inline chain at connect time. Choose "None" to edit hops manually.`;
        refreshBastionUI();
    }

    jhSelect.addEventListener("change", () => {
        const id = jhSelect.value;
        const jh = id ? store.getState().savedJumpHosts.find((x) => x.id === id) || null : null;
        applySavedJumpHost(jh);
    });

    manageJhBtn.addEventListener("click", async () => {
        const picked = await openJumpHostManager({ pick: true });
        await store.refreshSavedJumpHosts();
        fillJumpHostOptions(picked ? picked.id : jumpHostRefId);
        if (picked) {
            applySavedJumpHost(picked);
        }
    });

    fillJumpHostOptions(jumpHostRefId);
    if (jumpHostRefId) {
        const jh = store.getState().savedJumpHosts.find((x) => x.id === jumpHostRefId);
        if (jh) {
            applySavedJumpHost(jh);
        }
    }
    // Reflect the initial bastion state (inline rows in edit mode, or the
    // applied saved jump host) onto the target auth section + port hint.
    refreshBastionUI();

    // Plan P009: mirror the "any bastion row active" state onto the target
    // auth section and the port hint. The saved target credentials are
    // preserved (hidden, not cleared) — merely unused while a bastion is on.
    function refreshBastionUI(): void {
        const anyBastion = jumpHandles.some((h) => h.bastion.checked);
        if (anyBastion) {
            authRadios.style.display = "none";
            pwF.wrap.style.display = "none";
            keyF.wrap.style.display = "none";
            bastionAuthNote.style.display = "";
            portBastionHint.style.display = "";
        } else {
            bastionAuthNote.style.display = "none";
            portBastionHint.style.display = "none";
            authRadios.style.display = "";
            applyAuthMode(); // restore the visible password/key wrap
        }
    }

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

    const credF = field("Saved credential", credRow);
    body.append(nameF.wrap, hostF.wrap, portF.wrap, userF.wrap, credF.wrap, authSection, jumpSection, extraF.wrap, sftpPathF.wrap);

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
        const anyBastion = jumpHandles.some((h) => h.bastion.checked);
        const input: SessionInput = {
            id: editing ? initial!.id : "",
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
                bastion: h.bastion.checked,
            })),
            extraArgs: extraArgs.value.trim(),
            sftpInitialPath: sftpPath.value.trim(),
            credentialId: credentialId || "",
            jumpHostRef: jumpHostRefId || "",
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
        // Plan P009: while a bastion jump host is active the target
        // credentials are optional (the bastion relays target auth), so the
        // target keyPath requirement is relaxed.
        if (authTypeValue === AUTH_KEY && !input.keyPath && !anyBastion) {
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
                    show(row.err);
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