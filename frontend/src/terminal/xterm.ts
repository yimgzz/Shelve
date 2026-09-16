// terminal/xterm.ts — TermPool: one xterm.js instance per terminal tab
// (Phase 4c task 2; master plan §6 Terminal). Created when the tab opens
// so `terminal:data` arriving during "connecting" is never dropped.
//
// Responsibilities:
//   - Renderer: prefer the WebGL addon (the Chromium GPU path); on init error
//     or context loss, dispose it once and fall back to the default renderer
//     (one console.warn, no retry loop). Software-GL stacks skip WebGL.
//   - Addons: Fit (ResizeObserver → debounced fit → TerminalService.Resize),
//     WebLinks.
//   - DPI (phase E4): on a devicePixelRatio / display change, re-fit (debounced
//     150 ms) and force a full `term.refresh` when the renderer has not
//     re-measured, then notify the pty of the final geometry. Chromium's own
//     per-monitor scale handling is trusted — no window reload, no renderer
//     poking, no private xterm API.
//   - term.onData → buffer chunks → flush immediately (no timer/rAF) →
//     the terminal WebSocket (plan P005), falling back to
//     TerminalService.Write before the socket connects. Per-keystroke
//     latency is bounded by the socket alone; coalescing a fast typist's
//     keys would only add an extra hop before the echo.
//   - `write(tabID, bytes)` from `terminal:data` → term.write, even when
//     the pane is hidden (xterm retains scrollback).
//   - Mouse (plan P003): a left-drag selection copies to the system
//     clipboard when the gesture completes; right-click pastes via
//     term.paste() (same onData → Write path, bracketed paste honored).
//   - Keyboard (plan P008): Ctrl+Shift+V (physical) pastes the system
//     clipboard via the same term.paste() path; Ctrl+Shift+C (physical) copies
//     the current selection to the system clipboard (no-op when empty); all
//     terminal control keys (Ctrl+C/Z/A…, Ctrl+[ \ ], Shift+Backspace) are
//     keyed to the physical code, so they send identical bytes on any keyboard
//     layout.
//   - destroy(tabID) on tab close, destroyAll() on vault lock. All addons
//     are disposed with the terminal instance.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { TerminalService } from "../rpc";
import { store, type TerminalSettings } from "../store";
import { currentThemeTokens } from "../ui/theme";
import { copyText, readText } from "../ui/clipboard";
import { controlCharForCode, isCtrlShiftC, isCtrlShiftV } from "../ui/keys";
import { bytesToB64 } from "../ui/b64";
import { sendInput } from "./ws";

/** Options for creating a new pooled terminal (read from settings/theme). */
export interface TermCreateOptions {
    settings: TerminalSettings;
}

/** Read the current terminal palette from the active variant's CSS tokens.
 *  Includes cursor/cursorAccent/selectionBackground so the cursor and the
 *  selection highlight stay visible in every theme (plan P003 T1). */
function palette() {
    return currentThemeTokens();
}

interface Entry {
    term: Terminal;
    fit: FitAddon;
    webgl: WebglAddon | null;
    ro: ResizeObserver | null;
    resizeTimer: number | null;
    inputBuf: string;
    /** True while the user is deliberately reading scrollback (viewport
     *  scrolled above the buffer bottom). Suppresses scroll-to-bottom after
     *  fits so a resize never yanks the user out of history. */
    userScrolledUp: boolean;
    // plan P003 (mouse copy/paste): the .term-xterm parent plus listener
    // bookkeeping so destroy() tears everything down leak-free.
    el: HTMLElement;
    selecting: boolean;
    onDocMouseUp: ((e: MouseEvent) => void) | null;
    onPaneMouseDown: ((e: MouseEvent) => void) | null;
    onPaneContextMenu: ((e: MouseEvent) => void) | null;
}

const pool = new Map<string, Entry>();

// Module-level codec: user input is encoded on every keystroke, so the
// encoder is allocated once per app session rather than per event.
const textEncoder = new TextEncoder();

/** Transmit raw terminal input to the backend. Plan P005: keystrokes ride the
 *  terminal WebSocket (a plain macrotask that stays responsive even under
 *  output floods); before the socket is up, fall back to the rpc service
 *  call. Shared by typed input and the custom key handler so Ctrl+C and
 *  Shift+Backspace take exactly the same path as ordinary keystrokes. */
function sendBytes(tabID: string, bytes: Uint8Array): void {
    if (!sendInput(tabID, bytes)) {
        void TerminalService.Write(tabID, bytesToB64(bytes)).catch(() => {
            // Write failures (e.g. tab already closed server-side) are
            // surfaced through terminal:status; nothing actionable here.
        });
    }
}

/** Flush buffered user input to the backend (called immediately from onData). */
function flushInput(tabID: string, e: Entry): void {
    if (!e.inputBuf) {
        return;
    }
    const str = e.inputBuf;
    e.inputBuf = "";
    sendBytes(tabID, textEncoder.encode(str));
}

/** The grid's CSS cell height, read from the public DOM. Both the default and
 *  the WebGL renderer set `.xterm-screen`'s inline height to exactly
 *  `rows * cell.height` px, so dividing recovers the cell height without
 *  reaching into xterm internals. Returns null while the renderer has not
 *  measured yet (mid-DPR transition) — callers retry on a later pass. */
function cellHeightPx(e: Entry): number | null {
    const rows = e.term.rows;
    if (rows <= 0) {
        return null;
    }
    const screen = e.el.querySelector<HTMLElement>(".xterm-screen");
    const height = screen ? parseFloat(screen.style.height) : NaN;
    if (!Number.isFinite(height) || height <= 0) {
        return null;
    }
    return height / rows;
}

/** Keep the prompt visible after a fit unless the user is deliberately
 *  reading scrollback (onScroll sets userScrolledUp while the viewport is
 *  above the buffer bottom). Mirrors xterm's own write()-time behavior:
 *  output/geometry changes scroll to the bottom only when the user was
 *  already there. */
function ensureCursorVisible(e: Entry): void {
    if (e.userScrolledUp) {
        return;
    }
    try {
        e.term.scrollToBottom();
    } catch {
        /* ignore */
    }
}

/**
 * Undo the FitAddon's container-padding over-count. FitAddon measures the
 * parent (.term-xterm) computed height and subtracts only the xterm ELEMENT's
 * padding — it never sees the 8px padding on .term-xterm itself. When the
 * arithmetic leaves a fractional remainder, it emits one row too many; that
 * last row (usually the shell prompt) overflows the content box and is
 * clipped by overflow:hidden directly above the 30px monitor band.
 *
 * Fix: clamp rows to the true content-box height (clientHeight minus the
 * container's vertical padding). The clamp may only REMOVE a row — `term.rows`
 * (the fitted count) is the ceiling — and a sub-pixel tolerance keeps the trim
 * working when `cellHeightPx` is off by the renderer's own rounding (the WebGL
 * renderer rounds its grid height but reports an unrounded cell height).
 */
const CLAMP_TOLERANCE_PX = 0.5;

function clampRowsToContainer(e: Entry): void {
    try {
        if (e.el.offsetParent === null) {
            return; // hidden pane — zero-size container
        }
        const cellHeight = cellHeightPx(e);
        if (cellHeight === null) {
            return; // renderer unmeasured (mid-DPR transition); retried later
        }
        const style = window.getComputedStyle(e.el);
        const padV = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
        const avail = e.el.clientHeight - padV;
        if (avail < cellHeight) {
            return; // degenerate; leave the fit result for activation to fix
        }
        const rows = Math.max(1, Math.min(e.term.rows, Math.floor((avail + CLAMP_TOLERANCE_PX) / cellHeight)));
        if (rows !== e.term.rows) {
            e.term.resize(e.term.cols, rows);
        }
    } catch {
        /* ignore */
    }
}

/** One fit cycle: recompute cols/rows (padding-corrected), keep the prompt
 *  visible, and notify the pty of the final geometry. FitAddon no-ops while
 *  the renderer reports no measurable cell metrics (unmeasured — e.g. mid
 *  DPR transition); nudge a repaint and retry once. Every caller (the
 *  ResizeObserver, the DPI path, activate()) runs again later, so a miss here
 *  is always recoverable. */
function doFit(tabID: string, e: Entry): void {
    try {
        e.fit.fit();
    } catch {
        return;
    }
    if (!e.fit.proposeDimensions()) {
        try {
            e.term.refresh(0, e.term.rows - 1);
            e.fit.fit();
        } catch {
            /* ignore */
        }
    }
    clampRowsToContainer(e);
    ensureCursorVisible(e);
    void TerminalService.Resize(tabID, e.term.cols, e.term.rows).catch(() => {
        /* ignore: tab teardown race */
    });
}

/**
 * Re-fit every VISIBLE terminal after a devicePixelRatio / display change
 * (phase E4). Debounced: a monitor move fires several DPI signals in a row, so
 * one debounced pass per burst avoids a refit storm. Hidden panes are skipped
 * (their container has no box); activate() fits them when they are shown —
 * Chromium's own per-monitor scale handling plus the per-container
 * ResizeObserver covers everything else. Installed once per app lifetime.
 */
const DPI_REFIT_DEBOUNCE_MS = 150;
let dpiRefitTimer: number | null = null;

function refitForDpiChange(): void {
    if (dpiRefitTimer !== null) {
        window.clearTimeout(dpiRefitTimer);
    }
    dpiRefitTimer = window.setTimeout(() => {
        dpiRefitTimer = null;
        for (const [id, e] of pool) {
            if (e.el.offsetParent === null) {
                continue; // hidden pane — zero-size container
            }
            doFit(id, e);
        }
    }, DPI_REFIT_DEBOUNCE_MS);
}

/** True when WebGL would run on a software rasterizer (llvmpipe, SwiftShader…). */
function probeSoftwareWebGL(): boolean {
    try {
        const c = document.createElement("canvas");
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const gl = (c.getContext("webgl2") || c.getContext("webgl")) as any;
        if (!gl) {
            return true; // no GL at all → the addon would fail anyway
        }
        const ext = gl.getExtension("WEBGL_debug_renderer_info");
        if (!ext) {
            return false; // unknown → assume accelerated
        }
        const r = String(gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) || "").toLowerCase();
        return r.includes("llvmpipe") || r.includes("softpipe") || r.includes("swiftshader") || r.includes("software");
    } catch {
        return true;
    }
}

/** Memoized result of probeSoftwareWebGL. The GPU stack does not change
 *  mid-session, so the canvas + WebGL context are created once per app
 *  session instead of once per tab (create() runs for every new tab). */
let softwareWebGL: boolean | null = null;

/** True when WebGL would run on a software rasterizer (memoized). */
function isSoftwareWebGL(): boolean {
    if (softwareWebGL === null) {
        softwareWebGL = probeSoftwareWebGL();
    }
    return softwareWebGL;
}

/**
 * True when the default renderer should be used instead of the WebGL addon:
 * only on software GL (llvmpipe/SwiftShader), where the addon's full-grid
 * repaints cost more than the default renderer's incremental paint. Every
 * accelerated stack takes the WebGL path; a missing context or a context loss
 * falls back once, without a retry loop.
 */
function useDefaultRenderer(): boolean {
    return isSoftwareWebGL();
}

/**
 * The terminal pool. Instances live across store renders; only
 * destroy(tabID) / destroyAll() tear them down. Use activate() when a tab
 * becomes the active pane (fit + focus) and clear() before a Reconnect.
 */
export const TermPool = {
    /** Create a terminal for a tab inside `parent` (a .term-xterm node). */
    create(tabID: string, parent: HTMLElement, opts: TermCreateOptions): void {
        if (pool.has(tabID)) {
            return;
        }
        const pal = palette();
        const term = new Terminal({
            fontFamily: opts.settings.fontFamily,
            fontSize: opts.settings.fontSize,
            scrollback: opts.settings.scrollback,
            cursorBlink: false,
            // allowTransparency stays false (the default): it forces an alpha
            // texture atlas and a per-frame blend pass for no benefit — the
            // theme backgrounds are fully opaque by design. Keep xterm's
            // opaque fast paths; only set it if --terminal-bg ever gains an
            // alpha channel.
            theme: {
                background: pal.background,
                foreground: pal.foreground,
                cursor: pal.cursor,
                cursorAccent: pal.cursorAccent,
                selectionBackground: pal.selectionBackground,
            },
        });
        const fit = new FitAddon();
        term.loadAddon(fit);
        term.loadAddon(new WebLinksAddon());

        let webgl: WebglAddon | null = null;
        // Prefer the Chromium GPU path; skip it only on software GL, where the
        // addon's full-grid repaints cost more than the default renderer.
        if (!useDefaultRenderer()) {
            try {
                webgl = new WebglAddon();
                term.loadAddon(webgl);
            } catch (err) {
                console.warn("[xterm] WebGL unavailable, using the default renderer:", err);
                webgl = null;
            }
        }

        const entry: Entry = {
            term,
            fit,
            webgl,
            ro: null,
            resizeTimer: null,
            inputBuf: "",
            userScrolledUp: false,
            el: parent,
            selecting: false,
            onDocMouseUp: null,
            onPaneMouseDown: null,
            onPaneContextMenu: null,
        };
        pool.set(tabID, entry);

        // A single context-loss handler: dispose the WebGL addon so xterm
        // falls back to the default renderer without "context lost" spam.
        // Chromium restarts a lost GPU process itself; there is no retry loop.
        if (webgl) {
            webgl.onContextLoss(() => {
                const current = pool.get(tabID);
                if (current && current.webgl === webgl) {
                    try {
                        webgl.dispose();
                    } catch {
                        /* already disposed */
                    }
                    current.webgl = null;
                }
            });
        }

        term.open(parent);
        doFit(tabID, entry);

        // Debounced fit + pty resize on container size changes. A second pass
        // on the next frame catches the layout settling after a resize storm
        // (the renderer may report unmeasured cell metrics on the first pass,
        // making FitAddon no-op).
        const ro = new ResizeObserver(() => {
            if (entry.resizeTimer !== null) {
                window.clearTimeout(entry.resizeTimer);
            }
            entry.resizeTimer = window.setTimeout(() => {
                entry.resizeTimer = null;
                if (pool.get(tabID) !== entry) {
                    return;
                }
                doFit(tabID, entry);
                requestAnimationFrame(() => {
                    if (pool.get(tabID) !== entry) {
                        return;
                    }
                    doFit(tabID, entry);
                });
            }, 150);
        });
        ro.observe(parent);
        entry.ro = ro;

        // Track deliberate scroll-up so fits don't yank the user out of
        // scrollback history (see ensureCursorVisible).
        term.onScroll(() => {
            try {
                const buf = term.buffer.active;
                entry.userScrolledUp = buf.viewportY < buf.baseY;
            } catch {
                /* ignore */
            }
        });

        term.onData((data) => {
            entry.inputBuf += data;
            flushInput(tabID, entry);
        });

        // Custom key handling — plan P008 (layout-independent hotkeys).
        //
        // Physical-key rule (D1): every command chord is identified by
        // KeyboardEvent.code (the US physical position), which Chromium keeps
        // stable under any keyboard layout. xterm's own Ctrl path reads the
        // layout-mapped legacy keyCode — 0 on Cyrillic layouts — so it is
        // unusable there; we intercept the chords, send the bytes ourselves,
        // and return false so xterm neither double-sends nor no-ops. Plain
        // text (layout-aware — keypress/composition path), arrows, Enter,
        // Tab, Shift+Tab, and the F-keys pass through untouched.
        //
        // Chords sent (bytes per ui/keys, xterm-5.5 English parity, D4):
        //
        // Ctrl+letter (plain Ctrl+C included — the reserved chords below take
        // precedence over the KeyV/KeyC rows): the control byte. xterm copies
        // the selection on Ctrl+C with text selected, so the interrupt silently
        // never fires (drag-select makes selections common); always emitting
        // ETX here lets the remote tty (ISIG/VINTR) discard the input line and
        // print a fresh prompt. Copy is not lost: drag-selection already
        // auto-copies (P003).
        //
        // Symbol control combos: Ctrl+Space, Ctrl+2 (with Shift → ^@),
        // Ctrl+3…7, Ctrl+8, Ctrl+[ \ ], Ctrl+/ (with Shift) — exact parity
        // with what US users get on xterm 5.5, including the quirk combos.
        //
        // Ctrl+Shift+V (physical): paste the system clipboard via
        // term.paste() — the same onData → Write path as right-click (P003),
        // bracketed-paste handling honored. Gated on the state overlay being
        // hidden (state "ready"), exactly like the right-click handler; an
        // empty clipboard is a no-op. Plain Ctrl+V stays 0x16 (D3).
        //
        // Ctrl+Shift+C (physical): copy the current selection to the system
        // clipboard (no-op when the selection is empty). Consumed in every
        // case — including when it is empty or the pane is not "ready" — so
        // it never falls through to the KeyC row and never emits ETX. Ctrl+C
        // (no Shift) still sends the interrupt, even with a selection.
        //
        // Shift+Backspace: xterm maps it to a single-character erase byte.
        // Emit ^W (VWERASE 0x17) instead — the remote line editor deletes the
        // previous word (canonical-mode werase with IEXTEN, bash readline,
        // zsh, fish, and vim insert mode all honor it).
        //
        // Ctrl+\ / Ctrl+Shift+\: split this tab into a terminal group to the
        // right / left (master plan §6, VS Code editor-group chords). Handled
        // here as well as in the global router because xterm captures keydown
        // while the terminal has focus; both dispatch through the store's
        // `splitActiveTab` so the target (the focused group's active tab —
        // which is always the terminal owning DOM focus) cannot diverge.
        // Accepted tradeoff: Ctrl+\ no longer forwards 0x1c (SIGQUIT).
        //
        // Returning false stops xterm from processing the event, so onData
        // never fires again for these keys (no double-send).
        term.attachCustomKeyEventHandler((e) => {
            if (e.type !== "keydown") {
                return true;
            }
            if (
                (e.ctrlKey || e.metaKey) &&
                !e.altKey &&
                (e.code === "Backslash" || e.code === "IntlBackslash")
            ) {
                e.preventDefault();
                e.stopPropagation();
                store.splitActiveTab(e.shiftKey ? "left" : "right");
                return false;
            }
            if (isCtrlShiftV(e)) {
                // preventDefault: the browser's own paste accelerator could
                // otherwise fire a DOM `paste` event → double paste.
                e.preventDefault();
                e.stopPropagation();
                const overlay = parent.closest(".term-pane")?.querySelector<HTMLElement>(".term-overlay");
                if (overlay && overlay.style.display !== "none") {
                    return false; // not "ready" — same gate as right-click
                }
                void readText().then((text) => {
                    if (text) {
                        entry.term.paste(text);
                    }
                });
                return false;
            }
            if (isCtrlShiftC(e)) {
                // preventDefault: the browser's own copy accelerator could
                // otherwise fire a DOM `copy` event on the selection.
                e.preventDefault();
                e.stopPropagation();
                const overlay = parent.closest(".term-pane")?.querySelector<HTMLElement>(".term-overlay");
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
            const cc = controlCharForCode(e);
            if (cc !== null) {
                e.preventDefault();
                e.stopPropagation();
                sendBytes(tabID, textEncoder.encode(cc));
                return false;
            }
            // e.code is layout-invariant (Backspace is Backspace on every
            // layout); same semantics as the pre-P008 e.key check.
            if (e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey && e.code === "Backspace") {
                sendBytes(tabID, textEncoder.encode("\x17"));
                return false;
            }
            return true;
        });

        // ---- Mouse copy/paste (plan P003 T3/T4) ---------------------------
        entry.onPaneMouseDown = (e: MouseEvent) => {
            if (e.button === 0) {
                // Left button: flag a drag-selection gesture. xterm handles
                // the selection itself; we copy on the completing mouseup.
                entry.selecting = true;
                return;
            }
            if (e.button === 2) {
                // Never let the right button reach xterm: it would be
                // forwarded to mouse-tracking TUIs (vim/htop) as button 2.
                // Paste is handled on the contextmenu event below.
                e.preventDefault();
                e.stopPropagation();
            }
        };
        // `!`: assigned just above; non-null lets tsc pick the typed
        // DocumentEventMap overload instead of the strict EventListener one.
        parent.addEventListener("mousedown", entry.onPaneMouseDown!, true);

        // Copy when the selection gesture completes (Behavior A, approved).
        // Document-level + capture so drags ending outside the pane still
        // register; hidden tabs never clobber the clipboard with stale
        // selections (offsetParent is null while the pane is display:none).
        entry.onDocMouseUp = (e: MouseEvent) => {
            if (!entry.selecting || e.button !== 0) {
                return;
            }
            entry.selecting = false;
            if (!entry.el.offsetParent) {
                return;
            }
            if (!entry.term.hasSelection()) {
                return;
            }
            const text = entry.term.getSelection();
            if (text) {
                void copyText(text);
            }
        };
        document.addEventListener("mouseup", entry.onDocMouseUp!, true);

        // Right-click paste: swallow the browser context menu, then paste the
        // system clipboard via term.paste() — the same onData → Write path as
        // typing, with xterm's bracketed-paste handling. Gated on the state
        // overlay being hidden (state "ready"); the overlay covers the pane
        // in connecting/error/closed states and would capture the event
        // anyway — this check makes it explicit.
        entry.onPaneContextMenu = (e: MouseEvent) => {
            e.preventDefault();
            const overlay = parent.closest(".term-pane")?.querySelector<HTMLElement>(".term-overlay");
            if (overlay && overlay.style.display !== "none") {
                return;
            }
            void readText().then((text) => {
                if (text) {
                    entry.term.paste(text);
                }
            });
        };
        parent.addEventListener("contextmenu", entry.onPaneContextMenu!, true);
    },

    /** Write decoded backend output to a tab's terminal (hidden-safe). */
    write(tabID: string, data: Uint8Array): void {
        const e = pool.get(tabID);
        if (e) {
            e.term.write(data);
        }
    },

    /** Clear a tab's scrollback (used before a Reconnect). */
    clear(tabID: string): void {
        const e = pool.get(tabID);
        if (e) {
            e.term.clear();
        }
    },

    /** Fit + focus the active tab's terminal (on activation). */
    activate(tabID: string): void {
        const e = pool.get(tabID);
        if (!e) {
            return;
        }
        doFit(tabID, e);
        e.term.focus();
        // Re-assert focus on the next frame: a display:none → flex toggle can
        // drop the first focus() call while layout settles.
        requestAnimationFrame(() => {
            if (pool.get(tabID) === e) {
                e.term.focus();
            }
        });
    },

    /** Tear down a single tab's terminal instance. */
    destroy(tabID: string): void {
        const e = pool.get(tabID);
        if (!e) {
            return;
        }
        pool.delete(tabID);
        if (e.resizeTimer !== null) {
            window.clearTimeout(e.resizeTimer);
        }
        if (e.ro) {
            e.ro.disconnect();
        }
        // plan P003: remove the mouse copy/paste listeners (leak-free teardown).
        if (e.onDocMouseUp) {
            document.removeEventListener("mouseup", e.onDocMouseUp, true);
        }
        if (e.onPaneMouseDown) {
            e.el.removeEventListener("mousedown", e.onPaneMouseDown, true);
        }
        if (e.onPaneContextMenu) {
            e.el.removeEventListener("contextmenu", e.onPaneContextMenu, true);
        }
        e.term.dispose();
    },

    /** Tear down every instance (vault lock; replaces the 4a no-op hook). */
    destroyAll(): void {
        for (const id of Array.from(pool.keys())) {
            this.destroy(id);
        }
    },

    /** Re-fit every visible terminal after a devicePixelRatio / display change
     *  (phase E4; registered as a `ui/dpi` listener from main.ts). Debounced
     *  inside, so a burst of DPI signals costs one refit pass. */
    applyDpiChange(): void {
        refitForDpiChange();
    },

    /**
     * Apply updated terminal settings to every live instance (Settings Save,
     * Phase 4d). `term.options.*` re-applies live; a fit keeps geometry sane.
     */
    applySettings(settings: TerminalSettings): void {
        for (const [id, e] of pool) {
            e.term.options.fontFamily = settings.fontFamily;
            e.term.options.fontSize = settings.fontSize;
            e.term.options.scrollback = settings.scrollback;
            doFit(id, e);
        }
    },

    /** Re-apply the active variant's palette to every live instance
     *  (plan P001 §5; extended for cursor/selection in plan P003 T1). Called
     *  on theme/variant/OS-theme change via the onThemeApplied hook in
     *  main.ts. */
    applyTheme(): void {
        const pal = palette();
        for (const e of pool.values()) {
            e.term.options.theme = {
                background: pal.background,
                foreground: pal.foreground,
                cursor: pal.cursor,
                cursorAccent: pal.cursorAccent,
                selectionBackground: pal.selectionBackground,
            };
        }
    },
};