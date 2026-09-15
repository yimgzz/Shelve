// components/prompts.ts — blocking connection prompt modals
// (master plan §5): vault:hostkey-prompt → Accept/Reject,
// vault:key-prompt → single password input. Blocking UX: no backdrop
// or Esc close while a prompt is pending.

import { openDialog } from "../ui/dialog";
import { VaultService } from "../rpc";
import { toast } from "./toasts";

export interface HostKeyPromptPayload {
    connID: string;
    host: string;
    port: number;
    keyType: string;
    keyB64: string;
    fingerprint: string;
}

export interface KeyPromptPayload {
    connID: string;
    keyPath: string;
}

/**
 * One keyboard-interactive round on a bastion hop (plan P009).
 * `name`/`instruction`/`questions` are non-sensitive server text; `prefill`
 * is the single §8 IPC exception (the bastion hop's stored password), offered
 * only to prefill the masked inputs and never stored beyond this dialog.
 */
export interface KbdintPromptPayload {
    connID: string;
    name?: string;
    instruction?: string;
    questions: string[];
    echo: boolean[];
    prefill?: string;
}

/** Render the host-key verification modal (TOFU, master plan A1). */
export function showHostKeyPrompt(payload: HostKeyPromptPayload): void {
    const body = document.createElement("div");
    body.style.display = "flex";
    body.style.flexDirection = "column";
    body.style.gap = "10px";

    const intro = document.createElement("p");
    intro.style.margin = "0";
    intro.textContent =
        "The authenticity of the host can't be established. Verify the fingerprint before connecting.";
    body.appendChild(intro);

    const info = document.createElement("dl");
    info.style.margin = "0";
    info.style.display = "grid";
    info.style.gridTemplateColumns = "auto 1fr";
    info.style.gap = "4px 12px";
    info.style.fontSize = "13px";
    const rows: Array<[string, string]> = [
        ["Host", `${payload.host}:${payload.port}`],
        ["Key type", payload.keyType],
        ["Fingerprint", payload.fingerprint],
    ];
    for (const [k, v] of rows) {
        const dt = document.createElement("dt");
        dt.textContent = k;
        dt.style.color = "var(--text-dim)";
        const dd = document.createElement("dd");
        dd.style.margin = "0";
        dd.style.fontFamily = "ui-monospace, SFMono-Regular, Menlo, monospace";
        dd.textContent = v;
        dd.style.overflowWrap = "anywhere";
        info.append(dt, dd);
    }
    body.appendChild(info);

    const footer = document.createElement("div");
    const reject = document.createElement("button");
    reject.type = "button";
    reject.className = "btn";
    reject.textContent = "Reject";
    reject.addEventListener("click", () => {
        handle.close();
        void VaultService.RejectHostKey(payload.connID).catch((err) =>
            toast("error", String(err)),
        );
    });

    const accept = document.createElement("button");
    accept.type = "button";
    accept.className = "btn primary";
    accept.textContent = "Accept and connect";
    accept.addEventListener("click", () => {
        handle.close();
        void VaultService.ApproveHostKey(payload.connID).catch((err) =>
            toast("error", String(err)),
        );
    });

    footer.append(reject, accept);

    const handle = openDialog<void>({
        title: "Verify host key",
        body,
        footer,
        backdropClose: false,
        escClose: false,
        showClose: false,
        width: 460,
    });
    void handle.done;
}

/** Render the encrypted-key passphrase prompt (master plan A2). */
export function showKeyPrompt(payload: KeyPromptPayload): void {
    const body = document.createElement("div");
    body.style.display = "flex";
    body.style.flexDirection = "column";
    body.style.gap = "10px";

    const intro = document.createElement("p");
    intro.style.margin = "0";
    intro.textContent = `Enter the passphrase for the SSH key:`;
    body.appendChild(intro);

    const keyPath = document.createElement("code");
    keyPath.style.fontSize = "12px";
    keyPath.style.color = "var(--text-dim)";
    keyPath.style.overflowWrap = "anywhere";
    keyPath.textContent = payload.keyPath;
    body.appendChild(keyPath);

    const field = document.createElement("label");
    field.style.display = "flex";
    field.style.flexDirection = "column";
    field.style.gap = "6px";
    field.style.fontSize = "12px";
    field.style.color = "var(--text-dim)";
    field.textContent = "Passphrase";
    const input = document.createElement("input");
    input.type = "password";
    input.className = "input";
    input.autocomplete = "off";
    field.appendChild(input);
    body.appendChild(field);

    const errorEl = document.createElement("div");
    errorEl.className = "unlock-error";
    body.appendChild(errorEl);

    const footer = document.createElement("div");
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "btn";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => handle.close());
    const submit = document.createElement("button");
    submit.type = "button";
    submit.className = "btn primary";
    submit.textContent = "Unlock key";

    const submitFn = async () => {
        submit.disabled = true;
        errorEl.textContent = "";
        try {
            await VaultService.SubmitKeyPassphrase(payload.connID, input.value);
            handle.close();
        } catch (err) {
            submit.disabled = false;
            errorEl.textContent = String(err);
            input.focus();
        }
    };
    submit.addEventListener("click", submitFn);
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
            e.preventDefault();
            void submitFn();
        }
    });

    footer.append(cancel, submit);

    const handle = openDialog<void>({
        title: "Key passphrase",
        body,
        footer,
        backdropClose: false,
        escClose: false,
        showClose: false,
        width: 420,
    });
    void handle.done;
}

/**
 * Render one keyboard-interactive round for a bastion hop (plan P009):
 * one input per question, masked per `echo[i]`, with a single `prefill`
 * applied to the masked inputs. Mirrors showKeyPrompt's blocking UX.
 * Submit answers all at once; Cancel aborts the dial fail-fast.
 */
export function showKbdintPrompt(payload: KbdintPromptPayload): void {
    const body = document.createElement("div");
    body.style.display = "flex";
    body.style.flexDirection = "column";
    body.style.gap = "10px";

    const intro = document.createElement("p");
    intro.style.margin = "0";
    intro.textContent = payload.instruction || payload.name || "Enter the requested credentials:";
    body.appendChild(intro);

    const inputs: HTMLInputElement[] = [];
    payload.questions.forEach((label, i) => {
        const field = document.createElement("label");
        field.style.display = "flex";
        field.style.flexDirection = "column";
        field.style.gap = "6px";
        field.style.fontSize = "12px";
        field.style.color = "var(--text-dim)";
        field.textContent = label;
        const masked = !payload.echo[i];
        const input = document.createElement("input");
        input.type = masked ? "password" : "text";
        input.className = "input";
        input.autocomplete = "off";
        // Prefill (the bastion's stored password) fills only the masked
        // inputs; the user overwrites any that need a different answer.
        if (masked && payload.prefill) {
            input.value = payload.prefill;
        }
        field.appendChild(input);
        body.appendChild(field);
        inputs.push(input);
    });

    const errorEl = document.createElement("div");
    errorEl.className = "unlock-error";
    body.appendChild(errorEl);

    const footer = document.createElement("div");
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "btn";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => {
        void VaultService.CancelKbdint(payload.connID).catch((err) =>
            toast("error", String(err)),
        );
        handle.close();
    });
    const submit = document.createElement("button");
    submit.type = "button";
    submit.className = "btn primary";
    submit.textContent = "Send";

    const submitFn = async () => {
        submit.disabled = true;
        cancel.disabled = true;
        errorEl.textContent = "";
        try {
            const answers = inputs.map((el) => el.value);
            await VaultService.SubmitKbdintResponse(payload.connID, answers);
            handle.close();
        } catch (err) {
            submit.disabled = false;
            cancel.disabled = false;
            errorEl.textContent = String(err);
        }
    };
    submit.addEventListener("click", submitFn);
    inputs.forEach((input) =>
        input.addEventListener("keydown", (e) => {
            if (e.key === "Enter") {
                e.preventDefault();
                void submitFn();
            }
        }),
    );

    footer.append(cancel, submit);

    const handle = openDialog<void>({
        title: "SSH authentication",
        body,
        footer,
        backdropClose: false,
        escClose: false,
        showClose: false,
        width: 460,
    });
    // Focus the first input so the user can type immediately.
    if (inputs[0]) {
        inputs[0].focus();
        if (inputs[0].value) {
            inputs[0].select();
        }
    }
    void handle.done;
}