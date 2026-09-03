// terminal/xterm.ts — TermPool: one xterm.js instance per terminal tab
// (Phase 4c task 2; master plan §6 Terminal). Created when the tab opens
// so `terminal:data` arriving during "connecting" is never dropped.
//
// Responsibilities:
//   - Renderer: try WebGL → on init error catch + fall back to the default
//     renderer (one console.warn, no retry loop).
//   - Addons: Fit (ResizeObserver → debounced fit → TerminalService.Resize),
//     WebLinks.
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
//     clipboard via the same term.paste() path; all terminal control keys
//     (Ctrl+C/Z/A…, Ctrl+[ \ ], Shift+Backspace) are keyed to the physical
//     code, so they send identical bytes on any keyboard layout.
//   - destroy(tabID) on tab close, destroyAll() on vault lock. All addons
//     are disposed with the terminal instance.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { TerminalService } from "../../bindings/shelve/internal/wailsvc";
import type { TerminalSettings } from "../store";
import { currentThemeTokens } from "../ui/theme";
import { copyText, readText } from "../ui/clipboard";
import { controlCharForCode, isCtrlShiftV } from "../ui/keys";
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

/** Transmit raw terminal input to the backend. Plan P005: keystrokes ride the
 *  terminal WebSocket (a plain macrotask that stays responsive even under
 *  output floods); before the socket is up, fall back to the Wails service
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
    sendBytes(tabID, new TextEncoder().encode(str));
}

/** Guarded read of the renderer's CSS cell metrics (private API; the pinned
 *  @xterm/xterm 5.5 typings do not expose `term.dimensions`). */
function cellMetrics(e: Entry): { width: number; height: number } | null {
    try {
        const dims = (
            e.term as unknown as {
                _core?: { _renderService?: { dimensions?: { css?: { cell?: { width?: number; height?: number } } } } };
            }
        )._core?._renderService?.dimensions;
        const w = dims?.css?.cell?.width;
        const h = dims?.css?.cell?.height;
        if (w && h) {
            return { width: w, height: h };
        }
    } catch {
        /* ignore */
    }
    return null;
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
 * container's vertical padding).
 */
function clampRowsToContainer(e: Entry): void {
    try {
        if (e.el.offsetParent === null) {
            return; // hidden pane — zero-size container
        }
        const m = cellMetrics(e);
        if (!m) {
            return; // renderer unmeasured (mid-DPR transition); retried later
        }
        const style = window.getComputedStyle(e.el);
        const padV = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
        const avail = e.el.clientHeight - padV;
        if (avail < m.height) {
            return; // degenerate; leave the fit result for activation to fix
        }
        const rows = Math.max(1, Math.floor(avail / m.height));
        if (rows !== e.term.rows) {
            e.term.resize(e.term.cols, rows);
        }
    } catch {
        /* ignore */
    }
}

/** True when the xterm renderer is stuck: the IntersectionObserver render
 *  pause is active, or cell metrics are unmeasured. WebKitGTK can leave the
 *  terminal screen reported as non-intersecting after a window resize/move
 *  (no follow-up observation fires) — xterm then keeps parsing output into
 *  the buffer but never paints it: streaming and input look frozen. */
function rendererStuck(e: Entry): boolean {
    try {
        const rs = (
            e.term as unknown as { _core?: { _renderService?: { _isPaused?: boolean } } }
        )._core?._renderService;
        if (rs && rs._isPaused === true) {
            return true;
        }
    } catch {
        /* ignore */
    }
    return cellMetrics(e) === null;
}

/** Force the xterm renderer out of a stuck state after a resize/monitor
 *  move: clear the IntersectionObserver render pause, re-measure chars and
 *  repaint at the current devicePixelRatio (WebKitGTK does not reliably fire
 *  the matchMedia resolution events xterm relies on for DPR changes), then
 *  nudge WebKit to re-composite the canvas (visibility toggle, no layout
 *  shift). Guarded: only touches private API inside try/catch. */
function recoverRenderer(e: Entry): void {
    try {
        const rs = (
            e.term as unknown as {
                _core?: {
                    _renderService?: {
                        _isPaused?: boolean;
                        _needsFullRefresh?: boolean;
                        handleDevicePixelRatioChange?: () => void;
                        handleCharSizeChanged?: () => void;
                        refreshRows?: (start: number, end: number) => void;
                        clearTextureAtlas?: () => void;
                    };
                };
            }
        )._core?._renderService;
        if (!rs) {
            return;
        }
        if (rs._isPaused === true) {
            rs._isPaused = false;
            rs._needsFullRefresh = false;
        }
        if (typeof rs.handleDevicePixelRatioChange === "function") {
            rs.handleDevicePixelRatioChange();
        } else if (typeof rs.handleCharSizeChanged === "function") {
            rs.handleCharSizeChanged();
        }
        if (typeof rs.refreshRows === "function") {
            rs.refreshRows(0, e.term.rows - 1);
        }
        if (typeof rs.clearTextureAtlas === "function") {
            rs.clearTextureAtlas();
        }
        e.term.refresh(0, e.term.rows - 1);

        const xel = e.term.element as HTMLElement | null;
        if (xel) {
            // Focus-preserving visibility nudge: hiding the element makes its
            // helper textarea unfocusable — a focus() call while hidden is a
            // silent no-op, and an already-focused terminal gets blurred.
            // Capture the focus owner up front and restore it once the element
            // is visible again on the next frame.
            const wasFocused = xel.contains(document.activeElement);
            xel.style.visibility = "hidden";
            void xel.offsetHeight; // force a synchronous reflow
            requestAnimationFrame(() => {
                xel.style.visibility = "";
                if (wasFocused) {
                    try {
                        e.term.focus();
                    } catch {
                        /* ignore */
                    }
                }
            });
        }
    } catch {
        /* ignore */
    }
}

/** One fit cycle: recompute cols/rows (padding-corrected), keep the prompt
 *  visible, and notify the pty of the final geometry. FitAddon no-ops while
 *  the renderer reports zero cell metrics (unmeasured — e.g. mid resize), so
 *  when the renderer looks stuck we recover it first and re-fit. */
function doFit(tabID: string, e: Entry): void {
    try {
        e.fit.fit();
    } catch {
        return;
    }
    if (rendererStuck(e)) {
        recoverRenderer(e);
        try {
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
 * Window-level resize net. Moving a window between monitors with different
 * scale factors triggers a multi-step WM resize (size change, then a
 * devicePixelRatio change that WebKitGTK does not reliably report through
 * matchMedia). The per-container ResizeObserver debounce can win that race,
 * leaving the terminal sized for an intermediate geometry that no later RO
 * event corrects. Re-fit every live terminal 250 ms after the window
 * settles. Installed once per app lifetime.
 */
let winResizeTimer: number | null = null;
let winResizeHooked = false;

// recoverAll throttle (P007): recoverAll forces a full renderer recovery
// pass over every pooled terminal. It is called from focus-recovery paths
// that can fire on EVERY stray keydown while focus sits on <body>
// (main.ts restoreTerminalFocus), so the heavy pass must be rate-limited.
const RECOVER_ALL_THROTTLE_MS = 300;
let lastRecoverAllAt = 0;

function onWindowResize(): void {
    if (winResizeTimer !== null) {
        window.clearTimeout(winResizeTimer);
    }
    winResizeTimer = window.setTimeout(() => {
        winResizeTimer = null;
        for (const [id, e] of pool) {
            // Maximize/monitor-move on HiDPI screens can leave the xterm
            // renderer paused or WebKit's canvas composite stale; recover
            // first, then settle the geometry.
            recoverRenderer(e);
            doFit(id, e);
        }
        // WebKitGTK can take longer than 250 ms to finish the surface
        // reconfiguration after a big resize (maximize on a HiDPI monitor);
        // one delayed second pass is cheap insurance against the renderer
        // ending up paused / canvas composite going stale after the fact.
        winResizeTimer = window.setTimeout(() => {
            winResizeTimer = null;
            for (const [, e] of pool) {
                recoverRenderer(e);
            }
        }, 1000);
    }, 250);
}
function hookWindowResize(): void {
    if (winResizeHooked) {
        return;
    }
    winResizeHooked = true;
    window.addEventListener("resize", onWindowResize);
}

/** True when WebGL would run on a software rasterizer (llvmpipe, SwiftShader…). */
function isSoftwareWebGL(): boolean {
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

/**
 * True when the xterm canvas renderer should be used instead of the WebGL
 * addon. On Linux/WebKitGTK the canvas renderer is the reliable choice:
 * the WebGL addon freezes input/streaming whenever the window moves between
 * monitors with different scale factors (verified on multi-monitor X11), and
 * on software GL (llvmpipe/SwiftShader) its full-grid repaints cost more
 * than the incremental canvas renderer. Elsewhere (macOS/Windows on
 * accelerated stacks) the software-GL probe decides.
 */
function useCanvasRenderer(): boolean {
    if (/Linux/i.test(navigator.userAgent)) {
        return true; // WebKitGTK: WebGL addon breaks on monitor moves
    }
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
            cursorBlink: true,
            allowTransparency: true,
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
        // On software-rendered WebKitGTK (Linux default, dmabuf disabled) the
        // canvas renderer beats the WebGL addon (software GL); keep WebGL only
        // on accelerated GL stacks.
        if (!useCanvasRenderer()) {
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
        // falls back to the canvas renderer without "context lost" spam.
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

        // One window-level resize listener for the whole pool (settled re-fit).
        hookWindowResize();

        term.onData((data) => {
            entry.inputBuf += data;
            flushInput(tabID, entry);
        });

        // Custom key handling — plan P008 (layout-independent hotkeys).
        //
        // Physical-key rule (D1): every command chord is identified by
        // KeyboardEvent.code (the US physical position), which WebKitGTK
        // keeps stable under any keyboard layout. xterm's own Ctrl path
        // reads the layout-mapped keyCode — 0 on Cyrillic layouts — so it is
        // unusable there; we intercept the chords, send the bytes ourselves,
        // and return false so xterm neither double-sends nor no-ops. Plain
        // text (layout-aware — keypress/composition path), arrows, Enter,
        // Tab, Shift+Tab, and the F-keys pass through untouched.
        //
        // Chords sent (bytes per ui/keys, xterm-5.5 English parity, D4):
        //
        // Ctrl+letter (Ctrl+Shift+C too — Shift ignored, D3): the control
        // byte, including Ctrl+C. xterm copies the selection on Ctrl+C with
        // text selected, so the interrupt silently never fires (drag-select
        // makes selections common); always emitting ETX here lets the remote
        // tty (ISIG/VINTR) discard the input line and print a fresh prompt.
        // Copy is not lost: drag-selection already auto-copies (P003).
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
        // Shift+Backspace: xterm maps it to a single-character erase byte.
        // Emit ^W (VWERASE 0x17) instead — the remote line editor deletes the
        // previous word (canonical-mode werase with IEXTEN, bash readline,
        // zsh, fish, and vim insert mode all honor it).
        //
        // Returning false stops xterm from processing the event, so onData
        // never fires again for these keys (no double-send).
        term.attachCustomKeyEventHandler((e) => {
            if (e.type !== "keydown") {
                return true;
            }
            if (isCtrlShiftV(e)) {
                // preventDefault: WebKit's native paste accelerator could
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
            const cc = controlCharForCode(e);
            if (cc !== null) {
                e.preventDefault();
                e.stopPropagation();
                sendBytes(tabID, new TextEncoder().encode(cc));
                return false;
            }
            // e.code is layout-invariant (Backspace is Backspace on every
            // layout); same semantics as the pre-P008 e.key check.
            if (e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey && e.code === "Backspace") {
                sendBytes(tabID, new TextEncoder().encode("\x17"));
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

        // Right-click paste: swallow the WebKit context menu, then paste the
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
        // Focus FIRST while the pane is visible: recoverRenderer()'s recovery
        // nudge below hides the xterm element until the next frame, and
        // focusing an element inside a visibility:hidden subtree is a silent
        // no-op — the helper textarea never receives keydowns, so typing
        // would require a click. recoverRenderer() preserves this focus
        // across its visibility toggle.
        e.term.focus();
        // WebKitGTK keeps compositing the pane's pre-hide surface after a tab
        // switch (display:none → flex): xterm's renderer does not repaint the
        // re-shown terminal on its own (the stale canvas layer shows the
        // previous frame stretched until the first interaction forces a
        // repaint). Force the recovery path — unpause + refresh + WebKit
        // re-composite nudge — exactly like the window-resize path
        // (onWindowResize), then repeat on the next frame to cover WebKit's
        // late-layout timing.
        recoverRenderer(e);
        requestAnimationFrame(() => {
            if (pool.get(tabID) === e) {
                recoverRenderer(e);
                // The nudge above hid the element again; re-assert focus on
                // the NEXT frame, after its deferred visibility restore, so
                // the textarea is focusable. This also covers WebKitGTK
                // dropping a same-frame focus() right after the
                // display:none→flex toggle (deferred layout).
                requestAnimationFrame(() => {
                    if (pool.get(tabID) === e) {
                        e.term.focus();
                    }
                });
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

    /** Run the renderer recovery on every live instance (window focus after
     *  a maximize/resize is a good late signal; harmless when healthy).
     *  Rate-limited: focus-recovery paths may call this on every stray
     *  keydown while focus sits on <body> (P007). */
    recoverAll(): void {
        const now = Date.now();
        if (now - lastRecoverAllAt < RECOVER_ALL_THROTTLE_MS) {
            return;
        }
        lastRecoverAllAt = now;
        for (const [, e] of pool) {
            recoverRenderer(e);
        }
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