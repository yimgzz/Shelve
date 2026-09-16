# Terminal Tab Splits (VS Code-style editor groups)

**Status:** ready to implement
**Scope:** renderer only (`frontend/src/**`, CSS). No backend, RPC, DTO, event,
or `/terminal` framing changes. Tabs/groups stay ephemeral (master plan A3).

## Goal

Right-clicking a session tab offers **Split to Right** and **Split to Left**.
Selecting a direction moves the right-clicked tab into a terminal group on that
side (creating a group if none is there), so several live SSH sessions are
visible side by side. Groups behave like VS Code editor groups: one tab strip +
one visible terminal each, draggable dividers, VS Code split chords.

## Locked decisions

1. **VS Code-style groups, arbitrary columns.** Each group has its own tab strip
   and one visible terminal. `left | split | terminal-area | sftp-split | sftp`
   grid is unchanged; the terminal area becomes a horizontal row of groups.
2. **Move the clicked tab, reuse an adjacent group.** Split moves *only* the
   right-clicked tab. Right: use `groups[i+1]` if it exists, else insert a new
   group at `i+1`. Left: use `groups[i-1]` if it exists, else insert at `i`.
   The moved tab becomes its destination group's active tab and the destination
   group becomes focused. A source group left empty is removed.
3. **Draggable dividers, ephemeral widths.** Equal widths on split; dragging a
   divider rebalances. Widths are module-local (not in the store, not persisted).
4. **Per-group close actions.** Close Others / Close All Tabs / Close Tabs to
   the Right act only on the clicked tab's group.
5. **Drag within + across groups.** Within-group drag reorders; dropping on
   another group's strip moves the tab there (append at drop index, focus it).
6. **VS Code chords:** `Ctrl+\` = Split to Right, `Ctrl+Shift+\` = Split to Left,
   on the focused group's active tab. **Accepted tradeoff:** `Ctrl+\` no longer
   forwards `0x1c` (SIGQUIT) to remote shells; `Backslash`/`IntlBackslash` are
   intercepted in xterm's custom key handler as well as the global router.
7. **Focus model.** `activeGroupID` = focused group; the globally active tab is
   the focused group's active tab. The monitor bar and SFTP panel keep binding
   to that one tab, so they need only a selector swap. New sessions open in the
   focused group (a first group is created if none exists).
8. **No-op guard.** Split to Right is disabled when the tab is the only tab in
   the **last** group; Split to Left is disabled when it is the only tab in the
   **first** group (the move would recreate an identical layout).

## Data model (`frontend/src/store.ts`)

```ts
export interface TabGroup {
    id: string;              // ephemeral "grp-<counter36>"
    tabs: Tab[];
    activeTabID: string | null;
}
// StoreState: remove `tabs` + `activeTabID`; add:
groups: TabGroup[];
activeGroupID: string | null;
```

Pure selectors (exported, used by every consumer):

- `allTabs(s): Tab[]` — flatten in group order.
- `findTab(s, id): { group: TabGroup; groupIndex: number; tabIndex: number } | undefined`
- `activeGroup(s): TabGroup | undefined`
- `activeTab(s): Tab | undefined`
- `activeTabID(s): string | null`

`initialState`, `afterImport` (replace) and the lock reset in `main.ts` set
`groups: []`, `activeGroupID: null`.

## Ordered tasks

### 1. Store model + selectors
Implement the model above. Keep `set()` creating fresh `groups`/group objects so
reference-identity guards in the views work.

### 2. Store actions (rewrite the tab actions)
- `connectSession`: resolve the focused group (or create `grp-1`); append the
  optimistic tab there; set that group's `activeTabID = tempId` and
  `activeGroupID`; `replaceTab` must find the temp tab across groups and update
  its group's `activeTabID`.
- `activateGroup(groupID)`: no-op if already active; else set `activeGroupID`
  and activate that group's active tab's pane.
- `activateTab(tabID)`: find the group, set its `activeTabID`, set
  `activeGroupID`, keep the existing right-dock SFTP auto-open logic.
- `closeTab(tabID)`: remove from its group; if it was the group's active tab,
  choose the neighbour (`remaining[idx] ?? remaining[idx-1] ?? null`); if the
  group is now empty, remove it and move `activeGroupID` to a neighbouring
  group's active tab (or null). Keep the forwards/sftpTransfers/monitor cleanup.
- `closeOtherTabs` / `closeAllTabs` / `closeTabsToRight`: operate on the clicked
  tab's group only (`dropTabs(ids, anchorID, groupID)`), removing the group when
  it empties. `closeAllTabs` takes the clicked tab's id (not "all tabs").
- `moveTab(tabID, toGroupID, toIndex)`: remove the tab; if the source group
  empties and differs from `toGroupID`, remove it; insert at the clamped index
  in the target; set target active + `activeGroupID`; if same group, keep the
  existing reorder semantics.
- `splitTab(tabID, direction)`: new. Locate `gi`; resolve/create the target group
  per decision 2 (remove an emptied source group; adjust indices carefully);
  append the tab to the target; set target `activeTabID` + `activeGroupID`.
- `splitActiveTab(direction)`: convenience used by shortcuts — resolve
  `activeGroup(s)?.activeTabID` and call `splitTab`.
- `replaceTab`, `setTabState`, `setTabExited`, `setForward`, `setSftpTransfer`,
  `setMonitorMetrics`, `clearSftpTransfers`: patch the tab inside whichever group
  holds it (`findTab`); no signature changes needed except internal lookup.
- `hasReadyActiveTab` / `sftpPanelVisible`: use `activeTab(s)`.

### 3. `frontend/src/components/tabs.ts` — per-group strip
- Replace the singleton `renderTabStrip(host)` with
  `mountTabStrip(host, groupID): { destroy(): void }`; move `stripHost`,
  `stripUnsub`, `lastKey` into per-instance closure state. `destroy()` unsubs.
- `snapshotKey` hashes the group's tab ids/states/names plus the group's
  `activeTabID` and the global `activeGroupID` (so the active-group highlight
  updates).
- Keep pointerdown activation, middle-click close, click activation,
  `pointerup`/`pointercancel` drag, drop indicators, and per-strip
  scroll-reveal of the active tab.
- Drag: module-level `tabDrag` gains `fromGroupID`. On move, resolve the hovered
  `.tab` via `elementFromPoint`; read its `data-group-id` to support
  cross-group drop. On release call
  `store.moveTab(tabID, targetGroupID, dropIndexFor(...))`, where the index is
  computed against the target group's order (no source-shift correction when
  moving across groups).
- Context menu (exact order): `Split to Right`, `Split to Left`,
  separator, `Close Others`, `Close All Tabs`, `Close Tabs to the Right`.
  Disabled rules per decision 8; keep the existing close labels (the strip only
  shows its own group's tabs, so they read naturally).

### 4. `frontend/src/components/context-menu.ts`
- Add `separatorBefore?: boolean` to `MenuItem`; render a
  `.context-menu-sep` element before that item. Existing callers unchanged.

### 5. `frontend/src/components/terminal-view.ts` — the group area
- `renderTerminalArea(host)` builds `.terminal-area` inside `host` and keeps the
  single store subscription + reference-identity reconcile guard (now keyed on
  `groups`, `activeGroupID`, `settings.terminal`).
- Per group, maintain a stable container `.term-group[data-group-id]` holding
  `.tab-strip-host` (mount a strip via `mountTabStrip`) and
  `.terminal-pane-host`; reuse the existing `panes: Map<tabID, Pane>` and
  `TermPool` unchanged.
- Reconcile: create/remove group containers and their strips, reorder elements
  to match `groups`, re-parent each tab's `.term-pane` into its group's pane
  host (re-parenting preserves the pooled xterm; the ResizeObserver refits),
  set `display: flex` only for the group's `activeTabID`, render overlays, and
  activate (fit + focus) the focused group's active tab on id/state change.
- Empty state: reuse `showEmptyState()` when `groups.length === 0`; do not show
  per-group empty states (empty groups never persist).
- Group focus: capture-phase `pointerdown` on `.term-group` →
  `store.activateGroup(groupID)` (guarded no-op when already active); toggle an
  `.active` class for the focus indicator.
- Dividers: insert a `.group-splitter` element between adjacent groups. On drag,
  set `widths.set(leftGroupID, clamp(mouseX - leftRect.left, MIN_GROUP_W,
  leftW + rightW - MIN_GROUP_W))` and `widths.delete(rightGroupID)` (right
  flexes to fill); apply via inline `flex: 0 0 <px>` (absent → `flex: 1 1 0`).
  `MIN_GROUP_W = 240`. Drop width entries for removed groups on reconcile.
- Rename the exported mount to `renderTerminalArea` (or keep
  `renderTerminalView`); update `shell.ts` accordingly.

### 6. `frontend/src/components/shell.ts`
- Replace `tabHost` + `paneHost`/`#terminal-pane` creation with a single
  `.terminal-area-host` in `.right-pane`, then `renderTerminalArea(areaHost)`.
  Remove the `renderTabStrip` import/wiring (now owned by the area).

### 7. Keyboard (`frontend/src/ui/shortcuts.ts`, `frontend/src/terminal/xterm.ts`)
- `shortcuts.ts`: scope `cycleTab`, `activateNth`, `closeActiveTab` to the
  focused group (`activeGroup`); add
  `ctrl && !alt && (code === "Backslash" || code === "IntlBackslash")` →
  `store.splitActiveTab(shift ? "left" : "right")`.
- `xterm.ts` `attachCustomKeyEventHandler`: before `controlCharForCode`, add the
  same Backslash/IntlBackslash branch → `store.splitTab(tabID, shift ? "left" :
  "right")`, `preventDefault`/`stopPropagation`, return `false` (this is what
  removes the `0x1c` byte). Add a header-comment note about the SIGQUIT
  tradeoff.

### 8. Selector swaps in consumers
- `components/monitor-bar.ts`: `const tab = activeTab(st)`.
- `components/sftp-panel.ts`: `activeTab` / `activeTabID` selectors (3 sites).
- `components/gear.ts`: `allTabs(store.getState())` for the ready-tab count.
- `main.ts`: `activeTabID(st)` in `restoreTerminalFocus`; lock reset sets
  `groups: []`, `activeGroupID: null`.

### 9. CSS (`frontend/src/style/base.css`, `components.css`)
- `.terminal-area-host { flex: 1; min-height: 0; }`
- `.terminal-area { display: flex; height: 100%; min-width: 0; }`
- `.term-group { display: flex; flex-direction: column; min-width: 0;
  position: relative; flex: 1 1 0; }`
- `.group-splitter`: 4 px, `cursor: col-resize`, hover/drag accent, same tokens
  as `.splitter`.
- Active-group indicator: e.g. `.term-group.active .tab-strip-host` gets a top
  accent line / slightly raised background using existing theme tokens.
- Replace the `#terminal-pane` selector with `.terminal-pane-host`; scope
  `.term-pane` rules under `.terminal-area`.

### 10. Docs
Update the single source of truth, then the agent guide:
- `plans/1789467100000-master-plan.md` §6: Tabs bullet (splits/group model) and
  the Shortcuts list (`Ctrl+\`, `Ctrl+Shift+\`, the SIGQUIT note).
- `AGENTS.md` §7 (Tabs/shortcuts bullets) to match.

## Edge cases and failure modes

- **Solo tab in an edge group**: outward split is disabled; inward split merges
  into the neighbour and removes the emptied group.
- **Group removed while its tab was focused**: `activeGroupID` falls back to the
  neighbour group's active tab; monitor bar `Start`/`Stop` follows the new
  focused tab exactly as with a tab switch.
- **Re-parenting a pane between groups** must not dispose the TermPool entry;
  append the existing `.term-pane` and let the ResizeObserver + `activate()`
  refit. Never call `TermPool.destroy` except on tab removal.
- **Reconcile churn**: guard on `groups` reference identity; monitor metrics /
  sftp progress / search must not rebuild group DOM (would break the divider
  drag and terminal focus).
- **Drag vs. group focus**: the group's capture-phase pointerdown must not
  `preventDefault`, or xterm loses text selection/focus.
- **Listener audit**: every removed group's strip `destroy()` must unsubscribe;
  verify no listener growth after many splits/closes.
- **Backend untouched**: events stay tabID-keyed; no DTO/event/transport change.
  The only user-visible regressions are the intended split behavior and the
  `Ctrl+\` SIGQUIT removal.

## Validation

- `make lint` (renderer + Electron `tsc --noEmit`, Go `gofmt`/`vet`) and
  `make test`; `make build` and `make run` for a manual pass. No integration
  change expected (`make test-integration` optional; no networking touched).
- Manual checklist:
  1. Multi-tab group → Split Right/Left produce two columns; the moved tab is
     active/focused; the source keeps its other tabs; split into an existing
     adjacent group reuses it.
  2. Solo tab: outward item disabled on edge groups; inward split merges and
     collapses the emptied group.
  3. Drag reorder within a group; drag a tab onto another group's strip (moves +
     focuses); emptying a group removes it.
  4. Close Others / All / To the Right are group-scoped; closing the last tab
     removes the group; the empty state returns when no groups remain.
  5. Monitor bar and SFTP follow the focused group's active tab; both SFTP dock
     sides and the left-column toggle still work.
  6. `Ctrl+Tab`, `Ctrl+1..9`, `Ctrl+W` act within the focused group;
     `Ctrl+\`/`Ctrl+Shift+\` split; the remote shell no longer gets `0x1c`.
  7. Divider drag resizes with a 240 px minimum; widths reset on new groups and
     are never persisted.
  8. Lock/unlock and config import (Replace) clear all groups; no orphan
     terminals; DevTools shows no listener growth after repeated splits.
  9. Split, move, and close while `stty size` is visible: the pty resizes after
     re-parenting; DPI/zoom refits still apply.

## Out of scope

- Vertical splits; arbitrary 2D grids.
- Persisting group layout or divider widths (A3 — ephemeral).
- Edge-drop to create a new group, "Move to Next/Previous Group", "Close Group".
- Context menu on the terminal surface; split actions outside the tab menu and
  the two chords.
