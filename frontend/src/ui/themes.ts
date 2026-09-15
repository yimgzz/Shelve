// ui/themes.ts — the concrete theme-variant catalog (plan P001 §4.2).
//
// A *mode* ("system" | "light" | "dark") chooses the family; a *variant*
// picks the concrete palette within that family. Variant ids map 1:1 to
// the `data-variant` selectors in style/themes.css. The empty variant id
// ("") means "family default" and is resolved here at apply time.
//
// The backend keeps only the mode + an opaque `themeVariant` string in
// settings.json; cross-family mismatches are resolved here (master plan §4.1).

export type ThemeFamily = "light" | "dark";

export interface ThemeVariant {
    /** Canonical id; also the `data-variant` value in themes.css. */
    id: string;
    label: string;
    family: ThemeFamily;
}

/** The 18 selectable variants (9 dark + 9 light), plan P001 §1. */
export const THEME_CATALOG: ThemeVariant[] = [
    // Dark family
    { id: "default-dark", label: "Default Dark", family: "dark" },
    { id: "catppuccin-mocha", label: "Catppuccin Mocha", family: "dark" },
    { id: "dracula", label: "Dracula", family: "dark" },
    { id: "nord", label: "Nord", family: "dark" },
    { id: "gruvbox-dark", label: "Gruvbox Dark", family: "dark" },
    { id: "one-dark", label: "One Dark", family: "dark" },
    // Dark family (continued)
    { id: "vscode-dark-plus", label: "VS Code Dark+", family: "dark" },
    { id: "solarized-dark", label: "Solarized Dark", family: "dark" },
    { id: "tokyo-night", label: "Tokyo Night", family: "dark" },
    // Light family
    { id: "default-light", label: "Default Light", family: "light" },
    { id: "catppuccin-latte", label: "Catppuccin Latte", family: "light" },
    { id: "solarized-light", label: "Solarized Light", family: "light" },
    { id: "nord-light", label: "Nord Light", family: "light" },
    { id: "gruvbox-light", label: "Gruvbox Light", family: "light" },
    { id: "github-light", label: "GitHub Light", family: "light" },
    // Light family (continued)
    { id: "vscode-light-plus", label: "VS Code Light+", family: "light" },
    { id: "one-light", label: "One Light", family: "light" },
    { id: "quiet-light", label: "Quiet Light", family: "light" },
];

/** The default variant id for a family ("" semantics, plan P001 §4.1). */
export function familyDefaultVariant(family: ThemeFamily): string {
    return family === "dark" ? "default-dark" : "default-light";
}

/** True when `id` is one of the two family-default placeholders, i.e. it does
 *  not represent an explicit palette choice. System-mode resolution uses this
 *  to let real variant ids override the OS family while keeping the defaults
 *  OS-following. */
export function isFamilyDefaultVariant(id: string): boolean {
    return id === "default-dark" || id === "default-light";
}

/** True when `id` is a known variant belonging to `family`. */
export function variantInFamily(family: ThemeFamily, id: string): boolean {
    return THEME_CATALOG.some((v) => v.id === id && v.family === family);
}

/** Look up a variant by id; undefined if unknown. */
export function variantById(id: string): ThemeVariant | undefined {
    return THEME_CATALOG.find((v) => v.id === id);
}

/** All variants of a family, in catalog order. */
export function variantsForFamily(family: ThemeFamily): ThemeVariant[] {
    return THEME_CATALOG.filter((v) => v.family === family);
}