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

import { familyDefaultVariant, variantInFamily } from "./themes";

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
    const effective = resolveTheme(mode);
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

/** Terminal palette (background/foreground) for the active variant, read
 *  from the CSS tokens set by data-variant. Falls back to the CSS-resolved
 *  default on error. */
export function currentThemeTokens(): { background: string; foreground: string } {
    const cs = getComputedStyle(document.documentElement);
    const bg = cs.getPropertyValue("--terminal-bg").trim();
    const fg = cs.getPropertyValue("--terminal-fg").trim();
    const dark = currentTheme() === "dark";
    return {
        background: bg || (dark ? "#1e1e2e" : "#f7f8fa"),
        foreground: fg || (dark ? "#cdd6f4" : "#24292f"),
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