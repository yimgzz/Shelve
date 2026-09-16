// ui/titlebar.ts — renderer-drawn window controls for the frameless window
// (master plan §5; plan ui-ux-refinements §A).
//
// The bar is static markup in index.html (no first-paint layout jump) and is
// hidden by CSS unless body.frameless is set. initTitleBar() enables it only
// when the preload reports the frameless mode (SHELVE_TITLEBAR=native keeps the
// OS frame), wires the three window controls and the maximize-state glyph swap.
// Maximize/restore is the #tb-max button only: the bar is a drag region and
// Chromium does not deliver double-click events from draggable regions, so a
// double-click handler there would be dead code (the native frame offers
// double-click when SHELVE_TITLEBAR=native is set).

const MAX_GLYPH = "\u25a1"; // □
const RESTORE_GLYPH = "\u2750"; // ❐

/** Swap the maximize button glyph + tooltip for the current window state. */
function updateMaximized(maximized: boolean): void {
    const btn = document.getElementById("tb-max");
    if (!btn) {
        return;
    }
    btn.textContent = maximized ? RESTORE_GLYPH : MAX_GLYPH;
    const label = maximized ? "Restore" : "Maximize";
    btn.title = label;
    btn.setAttribute("aria-label", label);
}

/**
 * Enable the custom title bar. No-op when the app runs with the native OS
 * frame or the static markup is absent (safe degradation).
 */
export function initTitleBar(): void {
    const api = window.shelve?.titleBar;
    const bar = document.getElementById("titlebar");
    if (!api || api.frameless !== true || !bar) {
        return;
    }
    document.body.classList.add("frameless");

    document.getElementById("tb-min")?.addEventListener("click", () => api.minimize());
    document.getElementById("tb-max")?.addEventListener("click", () => api.toggleMaximize());
    document.getElementById("tb-close")?.addEventListener("click", () => api.close());

    window.shelve.windowState.onChange((s) => updateMaximized(!!s.maximized));
}
