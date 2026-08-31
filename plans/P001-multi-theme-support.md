# Plan P001 — Multi-Theme Support (Dark & Light Theme Families)

> Status: Draft for review
> Scope: frontend theming + settings schema + terminal palette binding
> Owner: Architect (this document) → implemented in Code mode

## 1. Goal

Add **4–6 dark** and **4–6 light** theme variants to Dummy SSH Manager, selectable by
the user, layered on top of the existing `system | light | dark` mode switch. The
user picks a *mode* (how the app chooses a family) and a *variant* (which concrete
palette within that family is applied). Terminal palettes must follow the selected
variant, not just light/dark.

**Accepted theme family:** `Default Dark`, `Catppuccin Mocha`, `Dracula`, `Nord`,
`Gruvbox Dark`, `One Dark` (dark); `Default Light`, `Catppuccin Latte`,
`Solarized Light`, `Nord Light`, `Gruvbox Light`, `GitHub Light` (light).

## 2. Master-plan references (read before implementation)

Read these sections first:

- [`.kilo/plans/1787912690309-master-plan.md`](.kilo/plans/1787912690309-master-plan.md)
  - **§4 Data Model & Storage** — `settings.json` schema; adding a theme-variant field without leaking secrets; normalize() safe-fallback rules.
  - **§5 Architecture** — `AppService.GetSettings/SaveSettings` surface; DTO contract; `config.Settings` is the settings JSON shape verbatim.
  - **§6 UI/UX Specification → Theming** — the token set (`--bg, --bg-panel, --bg-hover, --bg-active, --border, --text, --text-dim, --accent, --error, --ok, --warn, --terminal-bg, --terminal-fg`), `matchMedia`/`ThemeChanged` default, and the terminal palette binding rule.
  - **§11 Risks & Mitigations** — Wails beta isolation; keep theme changes limited to frontend + `config`, no new wails surface beyond existing AppService.
- Phase plans to re-check for the current wiring:
  - [`.kilo/plans/1788003650840-phase-4a-shell-unlock.md`](.kilo/plans/1788003650840-phase-4a-shell-unlock.md) (theme engine: [`ui/theme.ts`](frontend/src/ui/theme.ts), FOUC guard in [`index.html`](frontend/index.html), token block in [`themes.css`](frontend/src/style/themes.css)).
  - [`.kilo/plans/1788003650840-phase-4d-settings-lock-shortcuts.md`](.kilo/plans/1788003650840-phase-4d-settings-lock-shortcuts.md) (settings dialog live-apply/revert pattern in [`settings-dialog.ts`](frontend/src/components/settings-dialog.ts)).

## 3. Current state (as-is)

- [`internal/config/settings.go`](internal/config/settings.go): `Settings.Theme` is a single
  string `"system" | "light" | "dark"` (lines 33, 92–97); there is **no variant field**.
- [`frontend/src/ui/theme.ts`](frontend/src/ui/theme.ts): resolves mode → `data-theme="light"|"dark"`,
  caches `localStorage["dsm-theme"]` for the FOUC guard; `currentTheme()` returns only the family.
- [`frontend/src/style/themes.css`](frontend/src/style/themes.css): exactly two token blocks,
  `:root[data-theme="light"]` and `:root[data-theme="dark"]`, with fixed terminal bg/fg.
- [`frontend/src/components/settings-dialog.ts`](frontend/src/components/settings-dialog.ts): three
  radio buttons (`system/light/dark`), live-apply via `applyTheme(mode)`, revert on Cancel.
- Terminal palette binds to the *family only* via `currentTheme()` in
  [`frontend/src/terminal/xterm.ts`](frontend/src/terminal/xterm.ts).

## 4. Target design (to-be)

### 4.1 Settings schema (`internal/config/settings.go`)

Keep `Theme` as the **mode**. Add a **variant** selector:

```jsonc
{
  "theme": "system | light | dark",   // unchanged — the mode
  "themeVariant": "catppuccin-mocha", // NEW — concrete palette id; default "" = family default
  ...
}
```

Rules:
- `ThemeVariant` empty string = the family-default variant (e.g. `default-dark` for dark).
- `normalize()` maps an unknown/blank variant to `""`; cross-family mismatch is handled
  in the frontend (variant must be from the active family, else fall back to family default).
- Must remain **secret-free** (§4/§8: `settings.json` is audited to contain no secrets).

### 4.2 Frontend theme catalog (`frontend/src/ui/theme.ts`)

Introduce a static `THEME_CATALOG` mapping variant id → `{ id, label, family: "light"|"dark", }`.
`resolveTheme(mode, variant)`:
1. compute effective family from mode (or `prefers-color-scheme` when `system`);
2. if `variant` belongs to that family → use it; else use the family default;
3. set `documentElement.dataset.theme = family` **and** `documentElement.dataset.variant = variantId`.

`currentTheme()` (terminal palette) becomes `currentThemeTokens()` returning the resolved
variant's `--terminal-bg`/`--terminal-fg` (read via `getComputedStyle`), so xterm rebinds on
variant change — not just on family change.

FOUC guard in `index.html` must also read `localStorage["dsm-variant"]` (or a single cache
key like `dsm-theme=<family>:<variant>`) to avoid flash.

### 4.3 CSS (`frontend/src/style/themes.css`)

Keep `data-theme` controlling the *family base* (shared structural colors may stay per-family),
then add a `data-variant` layer that overrides the full token set per variant:

```css
:root[data-theme="dark"][data-variant="catppuccin-mocha"] {
    --bg: ...; --bg-panel: ...; --terminal-bg: #1e1e2e; --terminal-fg: #cdd6f4; ...
}
```

Each variant defines **all** tokens of the §6 set (including `--terminal-bg`/`--terminal-fg`).
This keeps component styles untouched — they already consume the tokens via
[`base.css`](frontend/src/style/base.css) / [`components.css`](frontend/src/style/components.css).

### 4.4 Settings dialog (`settings-dialog.ts`)

- Keep the `system|light|dark` mode radios.
- Add a **variant selector** that is live-applied (same revert-on-Cancel pattern):
  - when mode is `system`, show all variants grouped by family;
  - otherwise, show only variants of the chosen family (or grey out others).
- Persist both `theme` and `themeVariant` via `AppService.SaveSettings` (already wired through
  [`internal/wailsvc/appservice.go`](internal/wailsvc/appservice.go)).

## 5. Implementation steps (todo)

1. `internal/config/settings.go`: add `ThemeVariant string` to `Settings` + defaults + `normalize()`
   unknown-blank handling; update `DefaultSettings()`. Add unit test for normalize and secret-free
   round-trip (extend [`config_test.go`](internal/config/config_test.go)).
2. Define the 12-variant catalog in a new `frontend/src/ui/themes.ts` (id/label/family) and make
   `ui/theme.ts` resolve mode+variant, set `data-variant`, and expose `currentThemeTokens()`.
3. Update the FOUC guard in `index.html` and the `localStorage` cache keys to persist the variant.
4. Expand `themes.css`: keep `data-theme` blocks, add a `data-variant` token block per variant
   (all §6 tokens + terminal bg/fg).
5. Bind xterm palette in `terminal/xterm.ts` (and any reuse sites) to `currentThemeTokens()`
   instead of family-only; re-apply on variant change event.
6. `settings-dialog.ts`: add the variant control (grouped by family, live-apply, revert on Cancel);
   save both fields. Update the store state shape in `frontend/src/store.ts`.
7. QA: `make lint` (tsc + gofmt/vet) + `make test`; manual pass over each variant for contrast,
   tree/tabs/terminal legibility, and FOUC-free startup.

## 6. Exit criteria

- 6 dark + 6 light variants selectable; mode still controls family selection.
- `settings.json` persists `themeVariant`; unknown/blank normalizes safely; no secrets added.
- Terminal palette tracks the variant (checked visually + via `currentThemeTokens`).
- No flash-of-wrong-theme on startup for a persisted variant.
- `make test`, `make lint` green; unit test covers settings normalize + secret-free.