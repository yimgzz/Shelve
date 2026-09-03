# Plan P008 — Layout-independent hotkeys (terminal control keys + global shortcuts)

**Type:** Frontend-only bug fix + one new terminal shortcut (Ctrl+Shift+V paste). **Prereq:** none — touches Phase 4c (TermPool) and 4d (shortcuts router) code. **Scope:** 2 source files + 1 new module + docs.

**Master plan:** [`plans/1787912690309-master-plan.md`](1787912690309-master-plan.md) — read **§5 (event contract — unchanged), §6 (Terminal, Shortcuts, performance budgets), §8 (security — clipboard hygiene, no secret logging), §12 (global acceptance)** before starting.

**Related code to read first:**
- [`frontend/src/terminal/xterm.ts:505`](../frontend/src/terminal/xterm.ts#L505) — existing `attachCustomKeyEventHandler` (Ctrl+C, Shift+Backspace), `sendBytes()` at :75, right-click paste at :569, `onData` flush at :483.
- [`frontend/src/ui/shortcuts.ts:95`](../frontend/src/ui/shortcuts.ts#L95) — global shortcut router (all rules matched on `e.key`).
- [`frontend/src/ui/clipboard.ts`](../frontend/src/ui/clipboard.ts) — `readText()` (Wails `Clipboard.Text` + web fallback).
- [`frontend/src/main.ts:346`](../frontend/src/main.ts#L346) — capture keydown focus-recovery (no change; noted to avoid confusion).
- [`Dockerfile.dev:17`](../Dockerfile.dev#L17) — `libwebkitgtk-6.0-dev` from Debian trixie (WebKitGTK 2.4x — the runtime whose behavior this plan relies on).

## Goal

Make every hotkey and terminal control key fire on the **physical key**, regardless of the active keyboard layout (RU, QWERTZ, …):
1. Terminal control keys (Ctrl+C, Ctrl+Z, Ctrl+A…Ctrl+R, Shift+Backspace, symbol control combos) send the same bytes on any layout as they do on US QWERTY.
2. Global app shortcuts (Ctrl+K/L, Ctrl+T, Ctrl+W, Ctrl+1…9, Ctrl+Tab, Ctrl+,, Ctrl+Shift+E, F2, Delete) work on any layout.
3. New: **Ctrl+Shift+V = paste system clipboard into the active terminal** (user-confirmed; same path as right-click paste).

Ordinary text typing stays **layout-aware**: typing "привет" on a Russian layout must still send Cyrillic bytes.

No Go/Wails-service changes. No new event-contract entries. No xterm.js patching or vendoring. No settings-schema change.

## Root cause (verified in source — read this before coding)

WebKitGTK computes `KeyboardEvent` fields from the **layout-mapped** GDK keyval, not the physical key:
- `Source/WebCore/platform/gtk/PlatformKeyboardEventGtk.cpp` (webkitgtk-2.48):
  - `key` ← `gdk_keyval_to_unicode(keyval)` → Cyrillic letter (RU: physical C key gives `key = "с"`).
  - `keyCode` ← `windowsKeyCodeForGdkKeyCode(keyval)` → only Latin keyvals are mapped; Cyrillic keyval falls to `default: return 0`.
  - `code` ← `keyCodeForHardwareKeyCode(hardware scancode)` → a **fixed US-position table** (scancode `0x0036` → `"KeyC"`, `0x0037` → `"KeyV"`, …) — **layout-independent** for standard keyboards.

xterm.js 5.5 keydown pipeline (`src/browser/Terminal.ts` `_keyDown` → `common/input/Keyboard.ts` `evaluateKeyboardEvent`):
- `attachCustomKeyEventHandler` is called **first**; returning `false` makes xterm skip the event entirely (no `onData`).
- Ctrl+A…Z is derived from legacy `ev.keyCode` (65–90 range). On WebKitGTK with a Cyrillic layout `keyCode === 0` → no match → **nothing is sent** (Ctrl+C silently dies).
- Symbol Ctrl combos (Ctrl+3…8, Ctrl+[ , Ctrl+\, Ctrl+]) have the same `keyCode`-based breakage.
- Non-character keys (Enter, Tab, arrows, F2, Backspace, Delete) map from layout-invariant keyvals → already fine.

Current app code that depends on `e.key`:
- `frontend/src/terminal/xterm.ts:505-519` — Ctrl+C check `e.key.toLowerCase() === "c"` (fails: `key === "с"`) — and since xterm's own path also fails, Ctrl+C does **nothing** on RU.
- `frontend/src/ui/shortcuts.ts:112-171` — every letter/number/comma rule matched on `e.key` (all fail on RU).

Consequences match the user report: Ctrl+C and Ctrl+Shift+V (and every other letter/number Ctrl combo + all letter shortcuts) are dead on any non-English layout.

## Design decisions

- **D1 — Physical-key principle.** All *command* input (terminal control chars, app shortcuts) is identified by `KeyboardEvent.code` (US physical position). All *text* input keeps the layout (xterm's keypress/`compositionend` path — untouched).
- **D2 — Ctrl+Shift+V (physical) = paste** (user-confirmed). `readText()` → `term.paste(text)` — identical path to right-click paste (bracketed-paste wrapping honored by xterm, flows through the existing `onData` → WebSocket/Write pipeline). Only active in the `ready` state (same overlay gate as right-click paste).
- **D3 — Shift is ignored for Ctrl+letter** (real terminal semantics: Ctrl+Shift+C sends the same 0x03), **except** Ctrl+Shift+V which is reserved for paste. **Ctrl+V (no Shift) stays control char 0x16** (readline quoted-insert) — do **not** make plain Ctrl+V paste.
- **D4 — xterm.js 5.5 English-layout parity for symbol control combos** (table in T1) so RU users get exactly what US users get today, including the quirk combos (Ctrl+3 → ESC, etc.).
- **D5 — Global router keyed on `code`**; Ctrl+1…9 accepts both `DigitN` and `NumpadN` (parity with today's `e.key` behavior).
- **D6 — No xterm patching:** everything hooks the public `attachCustomKeyEventHandler` (returning `false` prevents xterm's broken `keyCode` path from double-sending or no-oping).

## Task list

### T1 — New module `frontend/src/ui/keys.ts`

Pure layout helpers (no DOM side effects, no imports of wails/bindings — importable from both `ui/` and `terminal/`):

1. `CTRL_CODE_CHAR: ReadonlyMap<string, string>` — physical `code` → control byte, **D4 parity table**:
   | `code` | modifiers required (on top of Ctrl) | byte |
   |---|---|---|
   | `KeyA`…`KeyZ` (26 entries) | none or Shift (Shift allowed, ignored — D3) | `0x01`…`0x1A` (KeyA=0x01 … KeyZ=0x1A) |
   | `Space` | no Shift | `0x00` |
   | `Digit2` | Shift | `0x00` |
   | `Digit3` / `Digit4` / `Digit5` / `Digit6` / `Digit7` | no Shift | `0x1B` / `0x1C` / `0x1D` / `0x1E` / `0x1F` |
   | `Digit8` | no Shift | `0x7F` |
   | `BracketLeft` | no Shift | `0x1B` |
   | `Backslash`, `IntlBackslash` | no Shift | `0x1C` |
   | `BracketRight` | no Shift | `0x1D` |
   | `Slash` | Shift | `0x1F` |
   (`IntlBackslash` covers ISO keyboards where the backslash sits next to Enter.)
2. `controlCharForCode(e: KeyboardEvent): string | null` — returns the byte when (and only when):
   - `(e.ctrlKey || e.metaKey) && !e.altKey && !e.repeat-guard-n/a` — i.e. Ctrl (or Meta) held, **Alt not held**;
   - `e.code === "KeyV" && e.shiftKey` → `null` **plus** a separate exported `isCtrlShiftV(e)` the caller uses for paste, so the letter row never swallows paste;
   - letters: `KeyA`–`KeyZ` regardless of Shift; symbol rows: exact modifier rules from the table (a `noShift` entry returns `null` when Shift is held; a `shift` entry returns `null` when Shift is not held).
3. Keep the function side-effect free so it can be reviewed line-by-line; ~60 lines total.

### T2 — Terminal: layout-independent control keys + Ctrl+Shift+V paste (`frontend/src/terminal/xterm.ts`)

Replace the body of the existing `attachCustomKeyEventHandler` at **xterm.ts:505-519** (keep the comment block, updated):

1. Order of checks inside the handler (keydown only):
   1. `isCtrlShiftV(e)` → `e.preventDefault(); e.stopPropagation();` then the paste path: read the overlay gate exactly like the right-click handler (:569-581 — `.term-overlay` must be `display:none`) → `void readText().then((t) => { if (t) entry.term.paste(t); });` → `return false`. (Gating before the async read; empty clipboard is a no-op.)
   2. `const cc = controlCharForCode(e);` → if non-null: `e.preventDefault(); e.stopPropagation(); sendBytes(tabID, new TextEncoder().encode(cc)); return false;` (reuses `sendBytes` :75 — WS first, `TerminalService.Write` fallback; key-repeat semantics unchanged: repeated keydowns each send their byte, same as today's Ctrl+C).
   3. Shift+Backspace → `0x17` — keep as-is (its `e.key === "Backspace"` is layout-invariant; may also match `e.code === "Backspace"`).
   4. Everything else → `return true` (xterm handles: plain text via keypress/composition — **layout-aware, untouched**; arrows/Enter/Tab/F-keys via layout-invariant keyvals).
2. `preventDefault()` matters: without it WebKit's native paste accelerator could also fire a DOM `paste` event → double paste.
3. Comment update: the header comment at :488-504 must state the physical-key rule (D1) and that xterm's own Ctrl path is unusable on WebKitGTK (keyCode 0) — which is why we intercept and return `false` (no double-send).
4. Update the file-top summary (:1-21, the "Mouse (plan P003)" block) to mention keyboard paste (plan P008).

### T3 — Global shortcut router keyed on `code` (`frontend/src/ui/shortcuts.ts`)

Rewrite the matching block (:95-172) from `e.key` to `e.code` (same actions, same suppression rules — typing-target + vault-locked early returns unchanged):

| Combo (on `e.code`) | Action (unchanged) |
|---|---|
| Ctrl + `KeyK` / `KeyL` (no Shift/Alt) | `focusSearch()` |
| Ctrl + `KeyT` | `connectOrCreate()` |
| Ctrl + `KeyW` | `closeActiveTab()` |
| Ctrl + `Tab` / Ctrl+Shift + `Tab` | `cycleTab(∓/1)` |
| Ctrl + `Digit1…9` or `Numpad1…9` (no Shift/Alt) | `activateNth(n)` |
| Ctrl + `Comma` | `openSettingsDialog()` |
| no-mod + `F2` / `Delete` | rename / delete (already code-stable; just switch to `e.code` for consistency) |
| Ctrl+Shift + `KeyE` | `void toggleSftp()` |

Delete the now-unused `const key = e.key.toLowerCase();`. Keep `const ctrl = e.ctrlKey || e.metaKey;`.

### T4 — Docs

1. **Master plan** `plans/1787912690309-master-plan.md` **§6 Shortcuts** table (:210-223):
   - add row: `| Ctrl+Shift+V | paste system clipboard into the active terminal (bracketed-paste safe) |`;
   - add one line under the table: "All shortcuts and terminal control keys are keyed to the **physical key position** (US layout) via `KeyboardEvent.code`, so they fire identically under any active keyboard layout; plain text input remains layout-aware."
2. **AGENTS.md**: extend the "Terminal keys" bullet in §6 Conventions with `Ctrl+Shift+V pastes the system clipboard into the active terminal`, and add the physical-key layout note to the Shortcuts bullet.
3. **README.md** **Shortcuts** section (:189-204): same row + same one-line note.

### T5 — QA instrumentation (temporary, removed before commit)

- Temporarily add, behind the existing `window.__dsmDev` flag (`main.ts:321`), a console line for the terminal's custom key handler and the shortcut router that logs `{ type, key, code, keyCode, ctrlKey, shiftKey, altKey }` + action taken. Used only for the §QA checklist below. **Remove before the commit.** (If the inspector shows `code === "Unidentified"` for letter keys on the test machine, stop and re-verify D6/WebKit version — that would invalidate the whole approach on that build.)

## QA checklist (manual — no e2e framework in v1)

Run `make build` + `make run` (host needs X11 + GTK4/WebKitGTK runtimes). In an open terminal run `cat -v` to display raw control bytes. Verify on **US QWERTY** (regression) and **Russian (JCUICSEN)** layout; optionally QWERTZ:

Terminal (via `cat -v` / job control):
- [ ] Ctrl+C → `^C` (and: `sleep 30` + Ctrl+C interrupts, fresh prompt) — **both layouts**
- [ ] Ctrl+Z → `^Z` (`sleep 30` gets suspended: `[1]+ Stopped`) — both layouts
- [ ] Ctrl+A → `^A`, Ctrl+E → `^E`, Ctrl+R → `^R`, Ctrl+V → `^V` — both layouts
- [ ] Ctrl+[ → `^[`, Ctrl+\ → `^\`, Ctrl+] → `^]` — both layouts
- [ ] Ctrl+Space → `^@`, Ctrl+3 → `^[`, Ctrl+6 → `^E`-style (`^N`? no — `^N` is Ctrl+N; verify `ctrl+6 → ^N`? verify `^N`), Ctrl+8 → `^?` — both layouts *(byte-exact expectation from the T1 table; the checklist author re-checks each against the table)*
- [ ] Ctrl+Shift+2 → `^@`, Ctrl+Shift+/ → `^_` — both layouts
- [ ] **Ctrl+Shift+V pastes**: copy `hello 123` to system clipboard, paste into `cat -v` → `hello 123` appears — **both layouts** (this is the headline fix)
- [ ] Shift+Backspace in bash deletes previous word (`^W`) — both layouts
- [ ] Typing `привет мир` (RU layout) echoes `привет мир`; typing `hello` (US) echoes `hello` — no layout regression
- [ ] Right-click paste still works; drag-select still auto-copies (plan P003 regression)

Global shortcuts (focus on the tree, not a form field), **RU layout** + US regression:
- [ ] Ctrl+K / Ctrl+L focus the search box; Esc clears
- [ ] Ctrl+T connects selected session / opens draft; Ctrl+W closes the active tab
- [ ] Ctrl+1 / Ctrl+2 switch tabs; Ctrl+Tab / Ctrl+Shift+Tab cycle
- [ ] `Ctrl+,` opens Settings; Ctrl+Shift+E toggles the SFTP browser panel
- [ ] F2 renames, Delete deletes (confirm dialog) the selected node
- [ ] While typing in the search box / a dialog: shortcuts stay suppressed, Esc still works

Automation gate (container): `make lint` (incl. `tsc --noEmit` — new module must typecheck), `make test`, `make build`. `make test-integration`: not affected (no Go changes), skipped unless cheap.

## Exit criteria

1. All §QA checklist items pass on US + RU (QWERTZ bonus).
2. `make lint` + `make test` + `make build` green on a clean container.
3. Docs (master plan §6, AGENTS.md, README) updated; temporary T5 inspector removed from the final diff.
4. No changes under `internal/`, `cmd/`, `frontend/bindings/`; no new npm/Go dependencies.

## Risks & mitigations

- **Exotic keyboard geometries** (scancode ≠ US position): `code` can read `"Unidentified"` → we simply don't intercept → behavior degrades to today's (broken) xterm path, never a double-send. Standard 104/105-key boards (incl. ISO) are covered, including `IntlBackslash` for ISO.
- **WebKitGTK without `code`**: the dev image pins Debian trixie WebKitGTK 2.4x where `code` is implemented from hardware scancodes (verified in `PlatformKeyboardEventGtk.cpp`); T5 inspector step makes any regression impossible to miss at QA time.
- **WM-level layout-switcher hotkeys** (e.g. Ctrl+Space or Super bound by the desktop env) can be eaten by X11 before the webview sees the key. Out of scope — the app cannot outrank a global DE accelerator; document as a known limitation if the user's Ctrl+Space stops working while switching layouts.
- **IME input (CJK)** is untouched by design: we intercept only Ctrl-held chords and Shift+Backspace; composition text still flows through xterm unchanged.
- **Double-paste** from WebKit's native paste accelerator: prevented by `preventDefault()` in the intercepted handler (T2.2).

## Out of scope

- Go backend, Wails services, event contract, settings schema — unchanged.
- Ctrl+V as paste (stays 0x16), Ctrl+Insert/Shift+Insert, AltGr third-level chords, macOS/Windows code paths (this app runs on WebKitGTK/Linux).
- Rebinding/reshaping existing shortcuts beyond layout-independence.
- Commit: after landing, update `plans/1787912690309-master-plan.md` roadmap/notes if the project keeps a P-index — per project convention, commit after this logical unit.
