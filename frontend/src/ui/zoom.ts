// ui/zoom.ts — user zoom, kept strictly separate from OS DPI (phase E4 T7).
//
// VSCode's model has two independent notions: the display device scale
// (per-monitor, owned by Chromium and driven by the E4 DPI path) and *user
// zoom* (`1.2 ** zoomLevel`, applied through the frame). E4 ships the
// mechanism only — no UI (and no Settings control) exposes it — so the default
// level 0 reproduces the previous behavior exactly. The persisted
// `ui.zoomLevel` is applied once at boot; a later plan can add the UI without
// reworking any of this.

/** VSCode's zoom-level bounds. */
export const MIN_ZOOM_LEVEL = -8;
export const MAX_ZOOM_LEVEL = 8;

/** Clamp an arbitrary level to the supported range (whole levels only). */
export function clampZoomLevel(level: number): number {
    if (!Number.isFinite(level)) {
        return 0;
    }
    return Math.min(MAX_ZOOM_LEVEL, Math.max(MIN_ZOOM_LEVEL, Math.round(level)));
}

/**
 * Apply a zoom level through the preload bridge (`webFrame.setZoomLevel`).
 * Returns the clamped level actually applied. Never touches the OS scale;
 * Chromium performs the `1.2 ** level` mapping itself.
 */
export function applyZoomLevel(level: number): number {
    const clamped = clampZoomLevel(level);
    window.shelve.zoom.setLevel(clamped);
    return clamped;
}

/** The level currently applied to the frame. */
export function currentZoomLevel(): number {
    return window.shelve.zoom.getLevel();
}
