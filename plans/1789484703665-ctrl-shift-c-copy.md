# Ctrl+Shift+C copies the terminal selection

## Goal

Bind **Ctrl+Shift+C** (physical `KeyC`, Ctrl/Meta + Shift) to copy the current
terminal selection to the system clipboard. This restores the familiar
terminal copy chord without changing `Ctrl+C`, which must keep sending the
interrupt (ETX) even with a selection (master plan §6).

## Decisions (confirmed)

1. **Scope:** terminal pane only, handled in the xterm
   `attachCustomKeyEventHandler` in `frontend/src/terminal/xterm.ts` — mirrors
   how `Ctrl+Shift+V` paste is done there. No document-level/app-wide handler;
   dialogs, search fields, and other selectable text keep the browser's native
   copy behavior.
2. **No selection:** consume the chord and do nothing (no ETX fallback).
   `Ctrl+C` remains the interrupt; `Ctrl+C` with a selection also still copies
   via drag-select-on-release (`P003`) and never loses the interrupt.
3. **Overlay gate:** copy only when the tab is in the `ready` state (state
   overlay hidden), exactly like `Ctrl+Shift+V` and right-click paste.
4. **Selection is not cleared** after copy (standard terminal behavior).

## Constraints / invariants

- No new backend, RPC/DTO, event, or preload surface: reuse `copyText()` from
  `frontend/src/ui/clipboard.ts` (already exposed via the reviewed
  `window.shelve.clipboard` bridge).
- Chord matching stays on `KeyboardEvent.code` (physical key, layout-stable)
  per the P008 rule, using `ctrlKey || metaKey` like `isCtrlShiftV`.
- No functional change to any other chord or byte.

## Implementation tasks

1. **`frontend/src/ui/keys.ts`**
   - Add `isCtrlShiftC(e)` next to `isCtrlShiftV` (line ~75):
     `(e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.code === "KeyC"`.
   - Update `controlCharForCode` (line ~91) to reserve the chord exactly like
     `KeyV`: if `e.code === "KeyC" && e.shiftKey`, return `null` (defensive —
     keeps the pure helper self-consistent and never emits `0x03` for it).
   - Update the header/doc comments to document Ctrl+Shift+C.

2. **`frontend/src/terminal/xterm.ts`**
   - Import `isCtrlShiftC`.
   - In the custom key handler, add the branch **before** the
     `controlCharForCode(e)` dispatch and after the `isCtrlShiftV` branch
     (line ~445):
     ```ts
     if (isCtrlShiftC(e)) {
         e.preventDefault();
         e.stopPropagation();
         const overlay = parent.closest(".term-pane")
             ?.querySelector<HTMLElement>(".term-overlay");
         if (overlay && overlay.style.display !== "none") {
             return false; // not "ready" — same gate as paste
         }
         if (entry.term.hasSelection()) {
             const text = entry.term.getSelection();
             if (text) {
                 void copyText(text);
             }
         }
         return false;
     }
     ```
     `return false` in all cases so xterm never double-handles the event and
     never emits ETX for Ctrl+Shift+C.
   - Extend the P008 chord comment block (lines ~404–440) to describe the new
     chord: Ctrl+Shift+C copies the selection, no-op when empty, does not
     change `Ctrl+C` (ETX) behavior.

## Documentation updates (same change)

- `plans/1789467100000-master-plan.md`
  - §6 Shortcuts table (line ~476): add row
    `| Ctrl+Shift+C | copy the terminal selection (no-op when empty) |`.
  - Terminal bullet (line ~420): mention `Ctrl+Shift+C` copies the selection.
- `AGENTS.md` §7 (lines ~323–325): add `Ctrl+Shift+C` copies the selection to
  the terminal-keys sentence.
- `README.md` shortcuts table (line ~229): add the Ctrl+Shift+C row.
- Do **not** rewrite the E3/E6/E7 phase plans (historical); optionally note the
  new chord in the E7 manual checklist only if it is being re-run.

## Risks / edge cases to verify

- `Ctrl+Shift+C` must not reach `controlCharForCode` or send `0x03`; the
  reservation in `keys.ts` is the safety net.
- `Ctrl+C` (no Shift) and `Ctrl+Shift+C` must not both fire (handler returns
  `false`).
- Copy with a hidden tab is impossible (only the focused terminal receives
  keydown); no clipboard clobber from background tabs.
- Empty/whitespace-only selection: treat as no copy (guard on non-empty text),
  still consume the chord.
- Overlay states (`connecting`/`error`/`closed`): chord consumed, no copy.
- `copyText()` failure path is already silent; no toast required.

## Validation

- `make lint` — must pass (`tsc --noEmit` for renderer + electron; this is the
  only frontend gate — there is no frontend test framework).
- `make test` — expected unchanged, run to confirm no regressions.
- Manual QA (in `make run`):
  1. Select text in a terminal, press Ctrl+Shift+C, paste into a local editor
     → selection text appears.
  2. Press Ctrl+Shift+C with no selection → nothing copied, no ETX sent
     (remote prompt unchanged).
  3. Press Ctrl+C with a selection → remote shell still receives SIGINT/
     interrupt (prompt reprinted); drag-select-on-release still copies.
  4. Repeat 1 under a non-English (e.g. Cyrillic) keyboard layout and with a
     non-US physical layout → still copies (physical-code matching).
  5. Trigger during the connecting/error overlay → no copy, no crash.

## Out of scope

- App-wide Ctrl+Shift+C for non-terminal text selection.
- Whole-buffer / scrollback copy, Select All, or copy-on-empty-selection of the
  prompt line.
- Ctrl+Insert / other copy chords, and any change to Ctrl+Shift+V.
- Any backend, DTO, event, or preload change.
