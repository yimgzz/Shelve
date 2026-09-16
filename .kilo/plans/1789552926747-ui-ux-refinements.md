# UI/UX refinements: custom title bar, SFTP button, tree & scrollbars

**Status:** ready to implement
**Scope:** renderer (`frontend/src/**`, `frontend/index.html`, CSS) + Electron
`main`/`preload` + the shared IPC types. **No Go/backend, RPC/DTO, event or
`/terminal` framing changes.** Tabs/groups/SFTP-panel-open stay ephemeral (A3).

This is an intentional user-visible change set (requested), so the master-plan
"no functional change rule" does not apply. Update the docs (§F) in the same
change.

## Locked decisions

1. **Custom title bar** — frameless window (`frame: false`) with a renderer-drawn,
   theme-aware bar: `Shelve` label + drag region + minimize/maximize/close on the
   right. The gear/Settings menu stays in the left toolbar. A
   `SHELVE_TITLEBAR=native` env override keeps the OS frame and hides the bar.
2. **SFTP toggle** — left dock gets a labelled `SFTP` button in the left toolbar
   (opens the browser) and a labelled `Sessions` button in the SFTP panel header
   (returns to the tree). The slim top-of-column icon toggle is removed. Right
   dock is unchanged (`SFTP` reopens, `×` closes).
3. **SFTP auto-open** — the ready-tab auto-open applies on **both** dock sides
   (not just right): activating/connecting a ready tab opens the browser.
4. **Tree indentation** — larger indent step (`16 → 18 px`), a chevron-width
   spacer on session rows so icons align per depth, and a vertical indent guide
   per children block.
5. **Collapse all / Expand all** — two icon buttons at the right end of the
   search header row (`.left-header`), plus entries in the empty-area tree
   context menu. (Not the toolbar: it would clip the gear at 240 px.)
6. **Scrollbars** — one global thin, theme-token-coloured scrollbar rule
   (Chromium `scrollbar-width`/`scrollbar-color`), covering the tree, SFTP list,
   tab strip, modal bodies and the xterm viewport.

## Ordered tasks

### A. Custom (frameless) title bar

1. **`electron/main.ts`**
   - Top-level: `const FRAMELESS_TITLEBAR = (process.env.SHELVE_TITLEBAR ?? "").trim().toLowerCase() !== "native";`
   - `createWindow()`: set `frame: !FRAMELESS_TITLEBAR`; add
     `additionalArguments: FRAMELESS_TITLEBAR ? ["--shelve-frameless"] : []` to
     `webPreferences` (documented way to pass a sync flag to a sandboxed
     preload). Keep `autoHideMenuBar`/`Menu.setApplicationMenu(null)`.
   - `wireWindowState(w)`: include `maximized: w.isMaximized()` in the pushed
     `WindowState`; call `schedule()` on `maximize`/`unmaximize` as well as
     `resize`/`move` (so the restore glyph updates).
   - `registerIpc()`: add fire-and-forget handlers `window:minimize`,
     `window:toggle-maximize` (`isMaximized() ? unmaximize() : maximize()`),
     `window:close`, each ignoring events whose `sender` is not `win.webContents`
     (same trust rule as the invoke handlers, but a silent return since `on`
     has no rejection channel).
   - Log whether the frameless title bar is active.
2. **`electron/preload.ts`** — add to the exposed object:
   ```ts
   titleBar: {
       frameless: process.argv.includes("--shelve-frameless"),
       minimize: () => ipcRenderer.send("window:minimize"),
       toggleMaximize: () => ipcRenderer.send("window:toggle-maximize"),
       close: () => ipcRenderer.send("window:close"),
   },
   ```
   No secrets, no Node objects; three void controls + one boolean.
3. **`frontend/src/rpc/ipc.ts`** — add `maximized: boolean` to `WindowState`
   (additive; the renderer's `applyWindowState` ignores unknown fields).
4. **`frontend/src/rpc/types.ts`** — extend `ShelveNative` with
   `titleBar: { frameless: boolean; minimize(): void; toggleMaximize(): void; close(): void }`.
5. **`frontend/index.html`** — add static markup (before `#app-root`):
   `<header id="titlebar" class="titlebar">` with a `.titlebar-title` span
   ("Shelve"), a flex spacer, and buttons `#tb-min` (`–`), `#tb-max` (`□`,
   swapped to `❐` when maximized), `#tb-close` (`×`), each with `title` +
   `aria-label`. Static markup = no first-paint layout jump.
6. **`frontend/src/ui/titlebar.ts` (new)** — `initTitleBar()`:
   - Return early when `window.shelve?.titleBar?.frameless !== true` (or the
     element is missing); add `document.body.classList.add("frameless")`.
   - Wire click handlers to the three preload controls.
   - `dblclick` on the bar (ignoring the buttons) → `toggleMaximize()`
     (Electron does not implement drag-region double-click maximize on Linux;
     verify no double-toggle on GNOME/KDE and drop the handler if it does).
   - `window.shelve.windowState.onChange((s) => updateMaximized(!!s.maximized))`
     swaps the `#tb-max` glyph and its `title`/`aria-label`
     ("Maximize" ↔ "Restore").
7. **`frontend/src/main.ts`** — import and call `initTitleBar()` right after
   `await whenReady()` in `boot()` (before settings load, so the bar shows on the
   unlock screen too).
8. **`frontend/src/style/base.css`**
   - `:root { --titlebar-h: 0px; }`; `body.frameless { --titlebar-h: 30px; }`
     and `body { padding-top: var(--titlebar-h); }` (the bar is `position:fixed`
     and therefore out of the grid; `box-sizing: border-box` keeps the grid at
     `100vh - 30px`).
   - `.titlebar` — fixed at top, `height: var(--titlebar-h)`, flex row,
     `background: var(--bg-panel)`, `border-bottom: 1px solid var(--border)`,
     `z-index: 90`, `user-select: none`, `-webkit-app-region: drag`,
     `display: none`; `body.frameless .titlebar { display: flex; }`.
   - `.titlebar-title` (`12px`, `--text-dim`), `.titlebar-spacer { flex: 1 }`,
     `.titlebar-btn` (`-webkit-app-region: no-drag`, ~46×100 %, transparent,
     `--text-dim`, hover `--bg-hover`/`--text`), `.titlebar-btn.close:hover`
     (`--error` background, `#fff`).
   - `.toast-stack { top: calc(var(--titlebar-h) + 12px); }` so toasts clear the
     bar.

### B. SFTP toggle labels + both-side auto-open

1. **`frontend/src/store.ts`** — in `activateTab` and `setTabState` remove the
   `settings.sftpPanelSide === "right"` term from the ready-tab auto-open
   condition (keep `sftpBrowserEnabled` + ready + active); update the
   `sftpPanelOpen` / auto-open doc comments (both dock sides auto-open).
2. **`frontend/src/components/shell.ts`**
   - Left branch: delete the `.left-view-switch` switcher row + icon toggle
     (`switchRowEl`/`toggleBtnEl`); call `buildTreeView(treeViewEl, true)` and
     keep the returned `sftpBtn`.
   - Left `applySftpLayout`: also set the toolbar `SFTP` button's `display` with
     the same rule as right mode (`enabled && readyTab && !panel`); keep the
     hint rule (`enabled && !readyTab`) and the tree/SFTP view swap.
   - `buildTreeView`'s right/left `withSftpButton` distinction collapses: the
     button is created whenever the setting could show it (label "SFTP",
     `title` "Open the SFTP browser for the active session"); in left mode it
     sets `sftpPanelOpen: true` exactly like right mode.
3. **`frontend/src/components/sftp-panel.ts`** — in `buildStatic`, prepend a
   `.btn small sftp-sessions` button labelled "Sessions" (`title`/`aria-label`
   "Show sessions") that sets `sftpPanelOpen: false`; it sits before the `×`.
4. **`frontend/src/style/components.css`** — `.sftp-sessions { display: none }`,
   `body.sftp-left .sftp-sessions { display: inline-flex }`; keep
   `body.sftp-left .sftp-close { display: none }`; remove the now-unused
   `.left-view-switch` rule.

### C. Tree indentation + guides

1. **`frontend/src/components/tree.ts`** — introduce `const INDENT = 18;` and
   replace the three hard-coded `depth * 16` sites (folder row, session row,
   inline create, inline rename) with `depth * INDENT`.
2. **`frontend/src/components/tree.ts`** — `makeSessionRow` prepends an empty
   `<span class="chevron-spacer">` so session icons occupy the same column as
   folder icons.
3. **`frontend/src/components/tree.ts`** — in `makeFolderRow`, set
   `children.style.setProperty("--guide-x", \`${8 + depth * INDENT + 6}px\`)` on
   the `.tree-children` block (the parent chevron centre).
4. **`frontend/src/style/components.css`**
   - `.tree-row .chevron-spacer { width: 12px; flex: none; }`.
   - `.tree-children { position: relative; }` and
     `.tree-children::before { content: ""; position: absolute; top: 0; bottom: 0;
     left: var(--guide-x, 0); width: 1px; background: var(--border); opacity: 0.6;
     pointer-events: none; }`.
   - Leave flat search results untouched (no indent, no guide).

### D. Collapse all / Expand all

1. **`frontend/src/components/tree.ts`**
   - Export `collapseAllTree()` (walk `store.getState().tree`, add every folder
     id to the module-local `collapsed` set, `rerender()`) and
     `expandAllTree()` (`collapsed.clear()`, `rerender()`).
   - Add `Collapse all` / `Expand all` entries to the empty-area context menu
     (`onTreeEmptyContext`, next to New Session/New Folder; use `separatorBefore`
     for the group).
2. **`frontend/src/components/shell.ts`** — after `renderSearch(searchHost)`,
   append a `.tree-actions` group with two `.btn small icon-btn` buttons
   (`⊟` Collapse all, `⊞` Expand all) wired to the tree.ts exports, each with
   `title` + `aria-label`. Keep it inside `.left-header` so it only exists in the
   tree view.
3. **`frontend/src/style/components.css`** — `.left-header { display: flex;
   align-items: center; gap: 6px; }`, `.left-header .search-wrap { flex: 1;
   min-width: 0; }`, `.tree-actions { display: flex; gap: 4px; flex: none; }`.

### E. Thin, themed scrollbars

**`frontend/src/style/base.css`**
- `:root { --scrollbar: color-mix(in srgb, var(--text-dim) 40%, transparent);
  --scrollbar-hover: color-mix(in srgb, var(--text-dim) 65%, transparent); }`
  (lazy `color-mix` re-resolves per theme variant).
- `* { scrollbar-width: thin; scrollbar-color: var(--scrollbar) transparent; }`.
- `.xterm-viewport { scrollbar-color: var(--scrollbar) transparent; }` (the
  xterm viewport is the terminal's own scroller).
- Drop the now-redundant `scrollbar-width: thin` on `.tab-strip`.

### F. Docs

1. **`plans/1789467100000-master-plan.md`**
   - §5: window bullet — frameless custom title bar, renderer window controls,
     `SHELVE_TITLEBAR=native` override; preload surface now includes them.
   - §6 Layout: add the 30 px title-bar row; SFTP left dock uses the toolbar
     `SFTP` + panel `Sessions` labels; auto-open applies to both dock sides.
   - §6 Tree bullet: indent guides + Collapse all/Expand all; §6 Theming: thin
     themed scrollbars; §8.11: window controls are part of the reviewed preload.
2. **`AGENTS.md`** — §5 preload sentence (add window controls), §6/§7 bullets for
   the title bar, SFTP `[SFTP]`/`[Sessions]` toggle + both-side auto-open, tree
   indent guides/collapse-all, and thin scrollbars.

## Behavior spec (quick reference)

- **Title bar**: visible on the unlock screen and the shell; themed by CSS vars
  in every variant (light/dark/system); drag to move, double-click to
  maximize/restore, three controls work while locked or modal-open. With
  `SHELVE_TITLEBAR=native` the bar is absent, no body padding, OS frame returns.
- **Left dock**: no top icon row. Tree view toolbar shows `+ Session`,
  `+ Folder`, `SFTP` (when enabled + a ready tab + browser closed), gear; search
  row shows the search box + `⊟`/`⊞`. SFTP view header shows `Sessions`,
  `←`, path bar, `Upload`/`New folder`/`Refresh`.
- **Auto-open**: first tab reaching `ready` (and every later ready activation
  while the browser is enabled) sets `sftpPanelOpen = true`; `Sessions` sets it
  false. With no ready tab the panel is hidden and the tree hint shows.
- **Right dock**: unchanged; `SFTP`/`×` behave as today (the new `Sessions`
  button is hidden by CSS).

## Edge cases / failure modes

- **Frameless resize/move**: some WMs manage frameless resize differently; the
  `SHELVE_TITLEBAR=native` escape hatch is the fallback. QA on X11 and Wayland.
- **Preload flag**: `frameless` must be `true` under the default; if
  `additionalArguments` is missing the renderer shows no bar (safe degradation,
  but verify in `make run`).
- **Modal backdrop** (`z-index: 100`) covers the title bar while a dialog is
  open; the close/min/max controls are temporarily unreachable. Accepted.
- **Auto-open hides the tree** on every ready activation (intended). The
  `Sessions` button and `Ctrl+Shift+E` remain the escape routes.
- **Narrow panel**: `⊟`/`⊞` live in the search row precisely so the 240 px
  toolbar does not clip the gear.
- **Scrollbars**: global `scrollbar-width: thin` also thins the xterm viewport;
  confirm the terminal scrollbar stays grabbable. Standard properties disable
  `::-webkit-scrollbar` styling in Chromium — do not mix the two.

## Validation

- `make lint` (gofmt/vet + renderer & Electron `tsc --noEmit`) and `make test`
  in the container; both must pass. No Go test changes expected.
- `make build && make run` manual pass:
  1. Title bar themed in a light and a dark variant; move, resize, minimize,
     maximize/restore (glyph + tooltip update), close, double-click.
  2. `SHELVE_TITLEBAR=native make run` → OS frame, no bar, no padding gap,
     toasts back at the old offset.
  3. Left dock: connect a session → browser auto-opens; `Sessions` returns to
     the tree; `SFTP` reopens; disabled/hidden when the setting is off or no tab
     is ready.
  4. Right dock: unchanged (`SFTP`/`×`); `Sessions` hidden.
  5. Tree: nested sessions visibly indented with guides; `⊟` collapses all,
     `⊞` expands all; context-menu entries agree; inline create/rename indent
     matches.
  6. Scrollbars thin + themed in tree, SFTP list, tab strip, modal body, xterm;
     dark and light variants.
- `make test-integration` not required (no backend/networking change).

## Out of scope

- Menu bar inside the title bar / restoring a native application menu.
- Persisting tree collapsed state (master plan keeps it ephemeral) or tab/dock
  layout.
- Custom title bar on Windows/macOS; window-controls-overlay APIs.
- New keyboard shortcuts for collapse/expand (shortcut table unchanged).
- Per-session dock side, drag-to-re-dock, independent left-dock SFTP width.

## Risks

- Frameless-window behavior (resize handles, double-click maximize) varies by
  compositor → escape hatch + X11/Wayland QA.
- Growing the preload surface: keep it to the four reviewed members and update
  the master plan §8.11 note; no arbitrary IPC passthrough.
- Auto-open on both sides changes the left default view; it is the requested
  behavior and is reversible via Settings (`SFTP panel position: right`).
