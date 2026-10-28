# SFTP browser: optional auto-open setting

## Goal

Add a second checkbox next to the SFTP browser setting that controls whether the
browser opens automatically when a session becomes ready. With the browser
enabled and auto-open **off**, the panel stays closed (the session tree stays
visible when docked left) and the existing toolbar `[SFTP]` button opens it
immediately — no trip to Settings needed.

## Current behavior (evidence)

- Settings schema: `internal/config/settings.go:49-61`.
- Settings UI: `frontend/src/components/settings-dialog.ts:167-193`
  (browser checkbox + dock-side select) and the save payload at `:310-326`.
- Renderer settings type: `frontend/src/rpc/types.ts:198-212`.
- Normalization/defaults: `frontend/src/store.ts:197-228`, `:230-264`.
- Two unconditional auto-open points:
  - `Store.activateTab` — `frontend/src/store.ts:455-460`
  - `Store.setTabState` (ready event) — `frontend/src/store.ts:827-833`
- Toolbar button already opens the panel: `frontend/src/components/shell.ts:153-158`.
- Visibility predicates: `sftpPanelVisible`/`sftpReopenVisible`,
  `frontend/src/store.ts:903-918`.

## Decisions

1. **New persisted boolean setting `sftpAutoOpen`** (JSON key `sftpAutoOpen`),
   default **`true`**. Existing configs without the key keep today's behavior;
   an explicit `false` wins. Because `Load` unmarshals into `DefaultSettings()`,
   a missing key automatically keeps `true` — no extra presence-aware block is
   needed.
2. The setting controls **only automatic opening**. It never force-closes a
   panel the user opened manually, and opening/closing via `[SFTP]`,
   `[Sessions]` or `×` is unchanged.
3. It applies to **both** auto-open points and **both** dock sides.
4. Checkbox label: `Open SFTP browser automatically`, placed directly under the
   `SFTP browser` checkbox; hint: `Opens the browser when a session becomes
   ready. When off, use the [SFTP] button to open it.` Always enabled (it can be
   pre-configured while the browser itself is disabled).
5. No new RPC method/event/service surface. The additive `Settings` field flows
   through `AppService.GetSettings`/`SaveSettings` and configuration
   export/import unchanged.

## Implementation tasks

1. **Backend config** — `internal/config/settings.go`
   - Add `SftpAutoOpen bool `json:"sftpAutoOpen"`` next to `SftpBrowserEnabled`.
   - Set `SftpAutoOpen: true` in `DefaultSettings()`.
   - No `normalize()` change required (plain bool).
2. **Backend tests** — `internal/config/config_test.go`
   - Add `sftpAutoOpen` to the secret-key allow-list switch at `:437`
     (otherwise `TestSettingsNeverContainsSecretFields` fails).
   - Add a focused test: default is `true`; `{"sftpAutoOpen":false}` loads
     `false`; explicit `true` round-trips through `Save`/`Load`.
3. **Renderer settings type** — `frontend/src/rpc/types.ts`
   - Add `sftpAutoOpen: boolean;` to `Settings` with a short comment.
4. **Store** — `frontend/src/store.ts`
   - `normalizeSettings`: `sftpAutoOpen: Boolean(raw.sftpAutoOpen ?? true)`.
   - `initialState.settings`: `sftpAutoOpen: true`.
   - Gate both auto-open conditions on the new flag:
     - `activateTab`: `tab.state === "ready" && sftpBrowserEnabled && sftpAutoOpen`
     - `setTabState`: add `settings.sftpAutoOpen` to the predicate.
   - Update the explanatory comments (including the `sftpPanelOpen` doc block
     at `:124-132`) so they state that auto-open is conditional.
   - Leave `sftpPanelVisible`/`sftpReopenVisible` unchanged: when auto-open is
     off the panel is simply closed, so `[SFTP]` is already visible.
5. **Settings dialog** — `frontend/src/components/settings-dialog.ts`
   - Create a checkbox `autoOpenCheck` (`settings-check`,
     `checked = current.sftpAutoOpen`) wrapped via `settingField` with the label
     and hint above; append it immediately after the SFTP browser field.
   - Include `sftpAutoOpen: autoOpenCheck.checked` in the `full` save payload.
6. **Docs**
   - `.kilo/plans/1789467100000-master-plan.md`: add `sftpAutoOpen` to the §4
     schema and note it in the presence-aware/default bullet; adjust the §5
     right/left SFTP panel bullets (auto-open is conditional); mention it in the
     §6 SFTP dock-side convention; add a §10 workstream row
     (`SFTP browser auto-open setting`, status `Done` once merged).
   - `AGENTS.md` §7 SFTP dock-side bullet: describe the new setting.

## Validation

- `make test` (config tests green).
- `make lint` (Go vet + renderer/Electron `tsc --noEmit`; no new dependency, so
  licenses-check stays green).
- No `make test-integration` needed: no SSH/SFTP/network code changes.
- Manual QA:
  1. Browser enabled + auto-open on (default): unchanged behavior — connecting
     or activating a ready tab opens the panel on both dock sides.
  2. Browser enabled + auto-open off, docked left: new session leaves the tree
     visible; `[SFTP]` is visible and opens the browser on one click; `Sessions`
     returns to the tree; switching between ready tabs does not force it open.
  3. Same with docked right: right column stays collapsed; `[SFTP]` opens it;
     `×` closes it.
  4. Restart the app: the choice persists; a pre-feature `settings.json`
     (no key) behaves as auto-open on.
  5. Disabling the browser still hides the panel and the `[SFTP]` button;
     re-enabling with auto-open off leaves it closed.
  6. Export/import (Replace) carries the setting.

## Risks / notes

- `sftpPanelOpen` remains ephemeral, so a panel opened manually stays open
  across tab switches/connects; the setting is "do not auto-open", not "always
  closed". This is intentional and matches the user's request to open it with
  the `[SFTP]` button.
- Default `true` avoids any user-visible behavior change for existing configs.
