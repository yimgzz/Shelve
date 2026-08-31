// ui/theme.ts — theme engine (master plan §6 Theming; plan P001).
//
// Sets <html data-theme> ("light" | "dark") AND <html data-variant>
// (a concrete palette id) from the settings mode ("system" | "light" |
// "dark") plus an optional variant. The effective values are cached in
// localStorage["dsm-theme"] / localStorage["dsm-variant"] so the index.html
// FOUC guard can apply them before first paint; this module keeps that cache
// in sync.
//
// The terminal palette (4c/5) reads currentThemeTokens() to bind xterm
// colors to the *variant*, not just the family.

import { familyDefaultVariant, isFamilyDefaultVariant, variantById, variantInFamily } from "./themes";

export type ThemeMode = "system" | "light" | "dark";
export type EffectiveTheme = "light" | "dark";

const CACHE_KEY = "dsm-theme";
const VARIANT_CACHE_KEY = "dsm-variant";
const mediaDark = window.matchMedia("(prefers-color-scheme: dark)");

let currentMode: ThemeMode = "system";
let currentVariant = "";

/** Optional hook notified after every applied theme change (main.ts wires
 *  it to re-paint live terminals). */
type ThemeListener = () => void;
const listeners = new Set<ThemeListener>();

/** Subscribe to theme changes; returns an unsubscribe function. */
export function onThemeApplied(fn: ThemeListener): () => void {
    listeners.add(fn);
    return () => listeners.delete(fn);
}

function notify(): void {
    // forEach avoids Set iteration (tsconfig has no --downlevelIteration).
    listeners.forEach((fn) => fn());
}

/** Normalize an arbitrary settings.theme string to a ThemeMode. */
export function normalizeTheme(value: string): ThemeMode {
    if (value === "light" || value === "dark" || value === "system") {
        return value;
    }
    return "system";
}

/** Resolve a mode to the effective light/dark theme for the OS. */
function resolveTheme(mode: ThemeMode): EffectiveTheme {
    if (mode === "light" || mode === "dark") {
        return mode;
    }
    return mediaDark.matches ? "dark" : "light";
}

/** Pick the concrete variant id for an effective family. Unknown or
 *  cross-family ids fall back to the family default (plan P001 §4.2). */
function resolveVariant(family: EffectiveTheme, variant: string): string {
    if (variant && variantInFamily(family, variant)) {
        return variant;
    }
    return familyDefaultVariant(family);
}

/** Apply a mode (and optional variant) to the DOM + FOUC cache. */
export function applyTheme(mode: ThemeMode, variant = ""): EffectiveTheme {
    currentMode = mode;
    let effective = resolveTheme(mode);
    // In system mode an explicitly-named variant (i.e. not a family-default
    // placeholder) names its own family — honor it over the OS preference so
    // e.g. a persisted "system" + dark-variant combination actually applies
    // instead of being silently replaced by the family default (bugfix 2026-08-31).
    if (mode === "system" && variant) {
        const v = variantById(variant);
        if (v && !isFamilyDefaultVariant(v.id)) {
            effective = v.family;
        }
    }
    const resolved = resolveVariant(effective, variant);
    currentVariant = resolved;
    document.documentElement.dataset.theme = effective;
    document.documentElement.dataset.variant = resolved;
    try {
        localStorage.setItem(CACHE_KEY, effective);
        localStorage.setItem(VARIANT_CACHE_KEY, resolved);
    } catch {
        /* ignore quota/unavailable errors */
    }
    notify();
    return effective;
}

/** Re-apply the OS preference — used when system theme changes while in
 *  system mode (matchMedia listener or the Wails ThemeChanged event). */
export function refreshFromSystem(): void {
    if (currentMode === "system") {
        applyTheme("system", currentVariant);
    }
}

/** Current effective theme family (kept for convenience/back-compat). */
export function currentTheme(): EffectiveTheme {
    return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/** Current applied variant id (may be a family-default id). */
export function currentVariantId(): string {
    return currentVariant;
}

/** Terminal palette consumed by xterm.js (plan P003 T1). All fields are
 *  concrete CSS color strings bound to the active variant. */
export interface TerminalPalette {
    background: string;
    foreground: string;
    cursor: string;
    cursorAccent: string;
    selectionBackground: string;
}

/** Convert a #rrggbb hex color to rgba() with the given alpha; null when the
 *  input is not a plain 6-digit hex (custom-property values keep their token
 *  text, so variants' hex accents arrive verbatim; xterm's color parser
 *  rejects computed color-mix serializations, hence building rgba() here). */
function hexToRgba(hex: string, alpha: number): string | null {
    const m = /^#([0-9a-f]{6})$/i.exec(hex.trim());
    if (!m) {
        return null;
    }
    const n = parseInt(m[1], 16);
    return `rgba(${(n >> 16) & 0xff}, ${(n >> 8) & 0xff}, ${n & 0xff}, ${alpha})`;
}

/** Terminal palette (background/foreground/cursor/selection) for the active
 *  variant, read from the CSS tokens set by data-variant. Falls back to the
 *  CSS-resolved default on error. The cursor mirrors the text polarity —
 *  dark-on-light in light variants, light-on-dark in dark variants — so it
 *  stays visible in any theme (xterm's unset cursor defaults to white, which
 *  is invisible on light backgrounds). */
export function currentThemeTokens(): TerminalPalette {
    const cs = getComputedStyle(document.documentElement);
    const bg = cs.getPropertyValue("--terminal-bg").trim();
    const fg = cs.getPropertyValue("--terminal-fg").trim();
    const accent = cs.getPropertyValue("--accent").trim();
    const dark = currentTheme() === "dark";
    const background = bg || (dark ? "#1e1e2e" : "#f7f8fa");
    const foreground = fg || (dark ? "#cdd6f4" : "#24292f");
    return {
        background,
        foreground,
        cursor: foreground,
        cursorAccent: background,
        // Accent at 30% alpha; xterm's default white-30% selection is
        // invisible on light backgrounds.
        selectionBackground:
            hexToRgba(accent, 0.3) ?? (dark ? "rgba(102, 163, 255, 0.3)" : "rgba(30, 111, 217, 0.3)"),
    };
}

/** Current mode as stored in settings. */
export function themeMode(): ThemeMode {
    return currentMode;
}

/**
 * Initialize the theme from settings. In "system" mode it installs a
 * prefers-color-scheme change listener. Call once at bootstrap after
 * settings are loaded. The Wails ThemeChanged listener is wired in
 * main.ts (the single event owner) and calls refreshFromSystem().
 */
export function initTheme(settingsTheme: string, settingsVariant = ""): void {
    applyTheme(normalizeTheme(settingsTheme), settingsVariant);
    mediaDark.addEventListener("change", refreshFromSystem);
}