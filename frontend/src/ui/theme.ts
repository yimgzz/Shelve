// ui/theme.ts — theme engine (master plan §6 Theming).
//
// Sets <html data-theme> ("light" | "dark") from the settings mode
// ("system" | "light" | "dark"). The effective value is cached in
// localStorage["dsm-theme"] so the index.html FOUC guard can apply it
// before first paint; this module keeps that cache in sync.
//
// The terminal palette (4c) reads currentTheme() to bind xterm colors.

export type ThemeMode = "system" | "light" | "dark";
export type EffectiveTheme = "light" | "dark";

const CACHE_KEY = "dsm-theme";
const mediaDark = window.matchMedia("(prefers-color-scheme: dark)");

let currentMode: ThemeMode = "system";

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

/** Apply a mode to the DOM and the FOUC cache; returns the effective theme. */
export function applyTheme(mode: ThemeMode): EffectiveTheme {
    currentMode = mode;
    const effective = resolveTheme(mode);
    document.documentElement.dataset.theme = effective;
    try {
        localStorage.setItem(CACHE_KEY, effective);
    } catch {
        /* ignore quota/unavailable errors */
    }
    return effective;
}

/** Re-apply the OS preference — used when system theme changes while in
 *  system mode (matchMedia listener or the Wails ThemeChanged event). */
export function refreshFromSystem(): void {
    if (currentMode === "system") {
        applyTheme("system");
    }
}

/** Current effective theme (consumed by the 4c terminal palette). */
export function currentTheme(): EffectiveTheme {
    return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
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
export function initTheme(settingsTheme: string): void {
    applyTheme(normalizeTheme(settingsTheme));
    mediaDark.addEventListener("change", refreshFromSystem);
}