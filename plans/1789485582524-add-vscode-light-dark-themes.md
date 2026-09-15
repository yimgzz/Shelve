# Add 6 new theme variants (VS Code Dark+/Light+, Solarized Dark, Tokyo Night, One Light, Quiet Light)

## Goal

Extend the existing theme catalog with **3 new dark + 3 new light variants**, so
users can pick recognizable VS Code-style palettes. Pure frontend, declarative
change: catalog entry + one CSS token block per variant. No backend, DTO, or
transport change.

## Current state (verified)

- Variant catalog: `frontend/src/ui/themes.ts` → `THEME_CATALOG` holds **12**
  variants (6 dark, 6 light), grouped dark-then-light, listed in catalog order.
- Palettes: `frontend/src/style/themes.css` → one `:root[data-theme=…][data-variant=…]`
  block per variant, defining **all** tokens:
  `--bg, --bg-panel, --bg-hover, --bg-active, --border, --text, --text-dim,
  --accent, --error, --ok, --warn, --terminal-bg, --terminal-fg`, plus
  `color-scheme`.
- Settings UI (`frontend/src/components/settings-dialog.ts`) builds the variant
  `<select>` from `variantsForFamily()` — no hardcoded list, so new entries
  appear automatically (flat list for light/dark modes, `<optgroup>`s in
  system mode).
- Terminal palette (`frontend/src/ui/theme.ts` `currentThemeTokens()`) derives
  bg/fg/cursor/selection from `--terminal-bg`/`--terminal-fg`/`--accent`. The
  16 ANSI colors are **not** themed (xterm defaults) for all 12 existing
  variants.
- FOUC guard in `frontend/index.html` is generic (replays cached
  `data-theme`/`data-variant`), needs no change.
- No test pins the variant count. `internal/config` `themeVariant` is an opaque
  string with a whitespace-trim round-trip test — unaffected. RPC DTO stays
  `themeVariant: string`.

## Decisions (resolved with user)

1. Add **6** variants: 3 dark + 3 light (see palette table below).
2. **No themed ANSI colors.** New variants expose only the same 13 tokens as the
   existing 12. `theme.ts` / `xterm.ts` are untouched; terminal ANSI behavior is
   unchanged app-wide.

## Out of scope

- Any Go / backend / DTO / settings-schema change.
- `TerminalPalette` + `--ansi-*` tokens (explicitly deferred; would change
  terminal rendering for existing themes).
- Theme preview swatches, custom user themes, per-theme fonts.
- Removing/renaming existing variants.

## Task list

### T1 — Add catalog entries (`frontend/src/ui/themes.ts`)

Append to `THEME_CATALOG` in this order (keeps existing order stable; the dark
group lists the new darks after `one-dark`, then the light group lists the new
lights after `github-light`):

```ts
    // Dark family (continued)
    { id: "vscode-dark-plus", label: "VS Code Dark+", family: "dark" },
    { id: "solarized-dark",   label: "Solarized Dark", family: "dark" },
    { id: "tokyo-night",      label: "Tokyo Night",    family: "dark" },
    // Light family (continued)
    { id: "vscode-light-plus", label: "VS Code Light+", family: "light" },
    { id: "one-light",         label: "One Light",       family: "light" },
    { id: "quiet-light",       label: "Quiet Light",     family: "light" },
```

Update the file header comment ("The 12 selectable variants (6 dark + 6 light)")
to say 18 (9 + 9). Do not reorder the other helpers
(`familyDefaultVariant`, `isFamilyDefaultVariant`, `variantInFamily`,
`variantById`, `variantsForFamily`) — they are family/id-generic.

### T2 — Add CSS token blocks (`frontend/src/style/themes.css`)

Add three blocks at the end of the dark-family section (after `one-dark`):

```css
:root[data-theme="dark"][data-variant="vscode-dark-plus"] {
    --bg: #1e1e1e;
    --bg-panel: #252526;
    --bg-hover: #2d2d30;
    --bg-active: #37373d;
    --border: #3c3c3c;
    --text: #d4d4d4;
    --text-dim: #9d9d9d;
    --accent: #007acc;
    --error: #f14c4c;
    --ok: #89d185;
    --warn: #cca700;

    --terminal-bg: #1e1e1e;
    --terminal-fg: #cccccc;

    color-scheme: dark;
}

:root[data-theme="dark"][data-variant="solarized-dark"] {
    --bg: #002b36;
    --bg-panel: #073642;
    --bg-hover: #0d3d4a;
    --bg-active: #15505f;
    --border: #13505e;
    --text: #93a1a1;
    --text-dim: #657b83;
    --accent: #268bd2;
    --error: #dc322f;
    --ok: #859900;
    --warn: #b58900;

    --terminal-bg: #002b36;
    --terminal-fg: #839496;

    color-scheme: dark;
}

:root[data-theme="dark"][data-variant="tokyo-night"] {
    --bg: #1a1b26;
    --bg-panel: #1f2335;
    --bg-hover: #292e42;
    --bg-active: #3b4261;
    --border: #3b4261;
    --text: #c0caf5;
    --text-dim: #9aa5ce;
    --accent: #7aa2f7;
    --error: #f7768e;
    --ok: #9ece6a;
    --warn: #e0af68;

    --terminal-bg: #1a1b26;
    --terminal-fg: #c0caf5;

    color-scheme: dark;
}
```

Add three blocks at the end of the light-family section (after `github-light`):

```css
:root[data-theme="light"][data-variant="vscode-light-plus"] {
    --bg: #ffffff;
    --bg-panel: #f3f3f3;
    --bg-hover: #e8e8e8;
    --bg-active: #dcdcdc;
    --border: #d4d4d4;
    --text: #1f1f1f;
    --text-dim: #717171;
    --accent: #007acc;
    --error: #cd3131;
    --ok: #14a44d;
    --warn: #bf8803;

    --terminal-bg: #ffffff;
    --terminal-fg: #333333;

    color-scheme: light;
}

:root[data-theme="light"][data-variant="one-light"] {
    --bg: #fafafa;
    --bg-panel: #f0f0f0;
    --bg-hover: #e5e5e6;
    --bg-active: #dcdcdd;
    --border: #d4d4d4;
    --text: #383a42;
    --text-dim: #696c77;
    --accent: #4078f2;
    --error: #e45649;
    --ok: #50a14f;
    --warn: #c18401;

    --terminal-bg: #fafafa;
    --terminal-fg: #383a42;

    color-scheme: light;
}

:root[data-theme="light"][data-variant="quiet-light"] {
    --bg: #f5f5f5;
    --bg-panel: #eaeaea;
    --bg-hover: #e4e4e4;
    --bg-active: #d8d8d8;
    --border: #d0d0d0;
    --text: #333333;
    --text-dim: #6a6a6a;
    --accent: #4b83cd;
    --error: #bd2c00;
    --ok: #448c27;
    --warn: #aa5500;

    --terminal-bg: #f5f5f5;
    --terminal-fg: #333333;

    color-scheme: light;
}
```

Each block MUST define every token in the list above (the base `:root` blocks
are only a FOUC fallback; a missing token leaks the previous variant's color).

### T3 — Update docs

Update the now-stale `12 variants` references to 18 + name the additions:

- `plans/1789467100000-master-plan.md` line 41 (`Dark & light theme families
  with 12 variants`) and §6 Theming: lines 430 and 495–496 (variant roster).
- `plans/1789467800000-electron-e7-acceptance-qa.md` line 68 (QA checklist
  "All 12 theme variants" → 18).

`AGENTS.md`, `README.md`, and the historical E1–E6 phase plans describe theming
without a hard count and stay unchanged.

### T4 — Validate

1. `make lint` — renderer `tsc --noEmit` must pass (new ids are string literals;
   `variantInFamily`/`variantById` are generic).
2. `make test` — no backend change expected to break; run to confirm.
3. Manual QA via `make run` (or `make dev`):
   - Settings → Variant shows the 6 new labels: in **system** mode under the
     correct Dark/Light `<optgroup>`; in **light**/**dark** mode in the flat list.
   - Selecting each new variant applies live (colors + terminal bg/fg/cursor
     change immediately via the theme listener; terminals repaint) and persists
     across restart.
   - Pick a new dark variant while in system mode on a light OS: the mode radio
     flips to Dark (existing `settings-dialog.ts:139` behavior) and the pair
     reloads correctly.
   - Cancel reverts to the pre-open theme/variant.
   - Set a new variant, then restore the old catalog (simulate unknown id):
     `resolveVariant` falls back to the family default without error.

## Acceptance criteria

- `THEME_CATALOG` has 18 entries; each of the 6 new ids has a matching CSS block
  defining all 13 tokens + `color-scheme`.
- Every new variant is selectable, live-applies, persists, and survives restart;
  FOUC guard restores it before first paint.
- No source change outside `frontend/src/ui/themes.ts` and
  `frontend/src/style/themes.css` (plus the doc updates in T3).
- `make lint` + `make test` green.

## Risks / notes

- **Contrast:** the values above target readable `--text`-on-`--bg`/`--bg-panel`
  and a visible `--accent` selection tint (terminal selection is `--accent` at
  30 % alpha in `theme.ts`). Spot-check each new variant during QA; if a pairing
  is too low-contrast, adjust only that variant's `--text`/`--text-dim`/`--accent`.
- **Order stability:** appending rather than interleaving avoids churn to the
  existing 12 entries and keeps diffs reviewable.
- **No ANSI theming** means the terminal's 16 colors remain xterm defaults in the
  new variants — identical to the existing 12; not a regression.
