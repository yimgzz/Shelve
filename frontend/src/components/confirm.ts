// components/confirm.ts — confirm dialog (master plan A8).
// Returns a Promise<boolean>. The "affected count" message for
// destructive tree ops is supplied by the caller in `message`.

import { openDialog } from "../ui/dialog";

export interface ConfirmOptions {
    title: string;
    message: string;
    confirmLabel?: string;
    danger?: boolean;
}

/** Show a confirm dialog; resolves true when the user confirms. */
export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
    const body = document.createElement("p");
    body.style.margin = "0";
    body.textContent = opts.message;

    const footer = document.createElement("div");
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "btn";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => handle.close(false));

    const ok = document.createElement("button");
    ok.type = "button";
    ok.className = opts.danger ? "btn danger" : "btn primary";
    ok.textContent = opts.confirmLabel ?? "Confirm";
    ok.addEventListener("click", () => handle.close(true));

    footer.append(cancel, ok);

    const handle = openDialog<boolean>({
        title: opts.title,
        body,
        footer,
        backdropClose: true,
        showClose: false,
        width: 420,
    });

    return handle.done;
}