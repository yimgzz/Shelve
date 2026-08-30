// components/toasts.ts — top-right toast stack (master plan §6).
// Auto-dismiss: 4s for info, 6s for error.

export type ToastLevel = "info" | "error";

const STACK_ID = "dsm-toast-stack";

function stack(): HTMLElement {
    let el = document.getElementById(STACK_ID);
    if (!el) {
        el = document.createElement("div");
        el.id = STACK_ID;
        el.className = "toast-stack";
        document.body.appendChild(el);
    }
    return el;
}

/** Show a transient toast notification. */
export function toast(level: ToastLevel, message: string): void {
    const host = stack();
    const el = document.createElement("div");
    el.className = `toast ${level}`;
    el.setAttribute("role", level === "error" ? "alert" : "status");
    el.textContent = message;

    host.appendChild(el);

    const dismiss = () => {
        el.remove();
    };
    const timeoutMs = level === "error" ? 6000 : 4000;
    window.setTimeout(dismiss, timeoutMs);
}