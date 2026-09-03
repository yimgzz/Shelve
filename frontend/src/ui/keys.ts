// ui/keys.ts — layout-independent physical-key helpers (plan P008, D1/D4).
//
// WebKitGTK computes KeyboardEvent fields from the LAYOUT-MAPPED GDK keyval:
// under a Cyrillic layout `key` is the mapped letter ("с") and `keyCode`
// falls to 0, while `code` stays the fixed US physical position (a scancode
// table inside GTK). xterm.js 5.5 derives Ctrl+letter from the legacy
// keyCode, so its own Ctrl path is unusable off English layouts.
//
// Physical-key principle (D1): every COMMAND chord (terminal control bytes,
// app shortcuts) is identified by `KeyboardEvent.code`; all TEXT input keeps
// the layout and flows through xterm's keypress/composition path untouched.
//
// Pure module: no DOM side effects, no wails/bindings imports — usable from
// both ui/ and terminal/.

/** Per-code Shift rule for Ctrl chords (D3/D4):
 *  "any" — Shift allowed and ignored (letters); "required" — only with
 *  Shift (Digit2, Slash); "forbidden" — only without Shift (the rest). */
type ShiftRule = "any" | "required" | "forbidden";

/**
 * Physical code → control byte. Parity table for xterm.js 5.5 on an English
 * layout (D4) — what a US-layout user gets today is what any layout gets
 * now, including the quirk combos (Ctrl+3 → ESC, Ctrl+8 → 0x7F, …).
 */
export const CTRL_CODE_CHAR: ReadonlyMap<string, string> = (() => {
    const m = new Map<string, string>();
    // KeyA…KeyZ → 0x01…0x1A
    for (let i = 0; i < 26; i++) {
        m.set(`Key${String.fromCharCode(65 + i)}`, String.fromCharCode(1 + i));
    }
    m.set("Space", "\x00");
    m.set("Digit2", "\x00"); // Ctrl+Shift+2 = Ctrl+@
    m.set("Digit3", "\x1b");
    m.set("Digit4", "\x1c");
    m.set("Digit5", "\x1d");
    m.set("Digit6", "\x1e");
    m.set("Digit7", "\x1f");
    m.set("Digit8", "\x7f");
    m.set("BracketLeft", "\x1b");
    m.set("Backslash", "\x1c");
    m.set("IntlBackslash", "\x1c"); // ISO boards: backslash next to Enter
    m.set("BracketRight", "\x1d");
    m.set("Slash", "\x1f"); // Ctrl+Shift+/ = Ctrl+_
    return m;
})();

/** The modifier rule per Ctrl code (D3): letters accept Shift and ignore it;
 *  "required" entries fire only when Shift is held; "forbidden" entries only
 *  when it is not. */
const CTRL_SHIFT_RULE: ReadonlyMap<string, ShiftRule> = (() => {
    const m = new Map<string, ShiftRule>();
    for (let i = 0; i < 26; i++) {
        m.set(`Key${String.fromCharCode(65 + i)}`, "any");
    }
    m.set("Space", "forbidden");
    m.set("Digit2", "required");
    m.set("Digit3", "forbidden");
    m.set("Digit4", "forbidden");
    m.set("Digit5", "forbidden");
    m.set("Digit6", "forbidden");
    m.set("Digit7", "forbidden");
    m.set("Digit8", "forbidden");
    m.set("BracketLeft", "forbidden");
    m.set("Backslash", "forbidden");
    m.set("IntlBackslash", "forbidden");
    m.set("BracketRight", "forbidden");
    m.set("Slash", "required");
    return m;
})();

/** Ctrl+Shift+V (physical) = paste the system clipboard into the terminal
 *  (D2). Checked BEFORE the control-char dispatch so the KeyV row never
 *  swallows the paste chord. */
export function isCtrlShiftV(e: KeyboardEvent): boolean {
    return (e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.code === "KeyV";
}

/**
 * The control byte to send for this keydown, or null when the app must not
 * intercept the chord (xterm handles it: plain text via keypress/composition
 * — layout-aware; arrows/Enter/Tab/F-keys via layout-invariant keyvals;
 * Alt chords). Side-effect free.
 */
export function controlCharForCode(e: KeyboardEvent): string | null {
    // Ctrl (or Meta) held, Alt not held.
    if (!((e.ctrlKey || e.metaKey) && !e.altKey)) {
        return null;
    }
    // Reserved for paste (D2) — never a literal 0x16 quote-insert.
    if (e.code === "KeyV" && e.shiftKey) {
        return null;
    }
    const byte = CTRL_CODE_CHAR.get(e.code);
    if (byte === undefined) {
        return null;
    }
    const rule = CTRL_SHIFT_RULE.get(e.code) ?? "forbidden";
    if (rule === "required" && !e.shiftKey) {
        return null;
    }
    if (rule === "forbidden" && e.shiftKey) {
        return null;
    }
    return byte;
}
