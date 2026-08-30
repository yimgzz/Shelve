// ui/dialog.ts — Promise-based modal dialog primitive.
//
// Provides a single reusable modal: overlay + card, focus trap (Tab
// cycling), Esc to close (optional), backdrop-click policy (optional),
// aria-modal, open/close animations. confirm.ts, prompts.ts and the
// session/settings dialogs (4b–4d) build on this verbatim.

export interface DialogOptions {
    title?: string;
    body: HTMLElement;
    /** Optional footer element (e.g. action buttons). */
    footer?: HTMLElement | null;
    /** Close when the backdrop is clicked. Default true. */
    backdropClose?: boolean;
    /** Close on Escape. Default true. */
    escClose?: boolean;
    /** Card width in px. Default 440. */
    width?: number;
    /** Whether to render the header close (×) button. Default true. */
    showClose?: boolean;
}

export interface DialogHandle<T> {
    el: HTMLElement;
    /** Close the dialog, resolving the promise with `result`. */
    close: (result: T) => void;
    /** Resolves with the result when the dialog is closed. */
    done: Promise<T>;
}

const FOCUSABLE =
    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

/**
 * Open a modal dialog and return a handle. Focus is moved into the card,
 * trapped via Tab cycling, and restored to the previously focused element
 * on close. The returned `done` promise resolves with the close result.
 */
export function openDialog<T>(opts: DialogOptions): DialogHandle<T> {
    const previouslyFocused = document.activeElement as HTMLElement | null;

    const backdrop = document.createElement("div");
    backdrop.className = "modal-backdrop";
    backdrop.setAttribute("role", "presentation");

    const card = document.createElement("section");
    card.className = "modal-card";
    card.setAttribute("role", "dialog");
    card.setAttribute("aria-modal", "true");
    if (opts.title) {
        card.setAttribute("aria-label", opts.title);
    }
    if (opts.width) {
        card.style.width = `${opts.width}px`;
    }

    const backdropClose = opts.backdropClose ?? true;
    const escClose = opts.escClose ?? true;
    const showClose = opts.showClose ?? true;

    let result: T;
    let resolve!: (value: T) => void;
    let settled = false;
    const done = new Promise<T>((res) => {
        resolve = res;
    });

    const finish = (value: T) => {
        if (settled) {
            return;
        }
        settled = true;
        result = value;
        if (backdrop.parentNode) {
            backdrop.remove();
        }
        document.removeEventListener("keydown", onKeydown);
        if (previouslyFocused && typeof previouslyFocused.focus === "function") {
            previouslyFocused.focus();
        }
        resolve(value);
    };

    // Header
    const header = document.createElement("header");
    header.className = "modal-header";
    if (opts.title) {
        const h = document.createElement("h2");
        h.className = "modal-title";
        h.textContent = opts.title;
        header.appendChild(h);
    }
    if (showClose) {
        const x = document.createElement("button");
        x.type = "button";
        x.className = "modal-close";
        x.setAttribute("aria-label", "Close");
        x.textContent = "×";
        x.addEventListener("click", () => finish(result!));
        header.appendChild(x);
    }
    card.appendChild(header);

    // Body
    const body = document.createElement("div");
    body.className = "modal-body";
    body.appendChild(opts.body);
    card.appendChild(body);

    // Footer
    if (opts.footer) {
        const footer = document.createElement("footer");
        footer.className = "modal-footer";
        footer.appendChild(opts.footer);
        card.appendChild(footer);
    }

    backdrop.appendChild(card);
    document.body.appendChild(backdrop);

    // Focus trap: cycle Tab within the card, restore on close.
    const getFocusable = (): HTMLElement[] =>
        Array.from(card.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
            (el) => !el.hasAttribute("disabled"),
        );

    function onKeydown(e: KeyboardEvent) {
        if (escClose && e.key === "Escape") {
            e.preventDefault();
            finish(result!);
            return;
        }
        if (e.key !== "Tab") {
            return;
        }
        const els = getFocusable();
        if (els.length === 0) {
            e.preventDefault();
            return;
        }
        const first = els[0];
        const last = els[els.length - 1];
        const current = document.activeElement;
        if (e.shiftKey) {
            if (current === first || !card.contains(current)) {
                e.preventDefault();
                last.focus();
            }
        } else if (current === last || !card.contains(current)) {
            e.preventDefault();
            first.focus();
        }
    }
    document.addEventListener("keydown", onKeydown);

    backdrop.addEventListener("mousedown", (e) => {
        if (backdropClose && e.target === backdrop) {
            finish(result!);
        }
    });

    // Move focus into the card after animation starts.
    requestAnimationFrame(() => {
        const els = getFocusable();
        (els[0] ?? card).focus();
    });

    return {
        el: backdrop,
        close: finish,
        done,
    };
}