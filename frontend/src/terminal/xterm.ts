// terminal/xterm.ts — TermPool: one xterm.js instance per terminal tab
// (Phase 4c task 2; master plan §6 Terminal). Created when the tab opens
// so `terminal:data` arriving during "connecting" is never dropped.
//
// Responsibilities:
//   - Renderer: try WebGL → on init error catch + fall back to the default
//     renderer (one console.warn, no retry loop).
//   - Addons: Fit (ResizeObserver → debounced fit → TerminalService.Resize),
//     WebLinks.
//   - term.onData → buffer chunks → flush on requestAnimationFrame →
//     TerminalService.Write(tabID, bytesToB64(data)) (coalesces keystrokes).
//   - `write(tabID, bytes)` from `terminal:data` → term.write, even when
//     the pane is hidden (xterm retains scrollback).
//   - Mouse (plan P003): a left-drag selection copies to the system
//     clipboard when the gesture completes; right-click pastes via
//     term.paste() (same onData → Write path, bracketed paste honored).
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
import { bytesToB64 } from "../ui/b64";

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
    raf: number | null;
    inputBuf: string;
    // plan P003 (mouse copy/paste): the .term-xterm parent plus listener
    // bookkeeping so destroy() tears everything down leak-free.
    el: HTMLElement;
    selecting: boolean;
    onDocMouseUp: ((e: MouseEvent) => void) | null;
    onPaneMouseDown: ((e: MouseEvent) => void) | null;
    onPaneContextMenu: ((e: MouseEvent) => void) | null;
}

const pool = new Map<string, Entry>();

/** Flush buffered user input to the backend (called once per rAF). */
function flushInput(tabID: string, e: Entry): void {
    if (!e.inputBuf) {
        return;
    }
    const str = e.inputBuf;
    e.inputBuf = "";
    const bytes = new TextEncoder().encode(str);
    void TerminalService.Write(tabID, bytesToB64(bytes)).catch(() => {
        // Write failures (e.g. tab already closed server-side) are
        // surfaced through terminal:status; nothing actionable here.
    });
}

function scheduleFlush(tabID: string, e: Entry): void {
    if (e.raf !== null) {
        return;
    }
    e.raf = requestAnimationFrame(() => {
        e.raf = null;
        flushInput(tabID, e);
    });
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
        try {
            webgl = new WebglAddon();
            term.loadAddon(webgl);
        } catch (err) {
            console.warn("[xterm] WebGL unavailable, using the default renderer:", err);
            webgl = null;
        }

        const entry: Entry = {
            term,
            fit,
            webgl,
            ro: null,
            resizeTimer: null,
            raf: null,
            inputBuf: "",
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
        try {
            fit.fit();
        } catch {
            /* zero-sized container; fit again on activation */
        }

        // Debounced fit + pty resize on container size changes.
        const ro = new ResizeObserver(() => {
            if (entry.resizeTimer !== null) {
                window.clearTimeout(entry.resizeTimer);
            }
            entry.resizeTimer = window.setTimeout(() => {
                entry.resizeTimer = null;
                if (pool.get(tabID) !== entry) {
                    return;
                }
                try {
                    entry.fit.fit();
                } catch {
                    return;
                }
                void TerminalService.Resize(tabID, entry.term.cols, entry.term.rows).catch(() => {
                    /* ignore: tab teardown race */
                });
            }, 150);
        });
        ro.observe(parent);
        entry.ro = ro;

        term.onData((data) => {
            entry.inputBuf += data;
            scheduleFlush(tabID, entry);
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
        try {
            e.fit.fit();
        } catch {
            /* ignored */
        }
        e.term.focus();
    },

    /** Tear down a single tab's terminal instance. */
    destroy(tabID: string): void {
        const e = pool.get(tabID);
        if (!e) {
            return;
        }
        pool.delete(tabID);
        if (e.raf !== null) {
            cancelAnimationFrame(e.raf);
        }
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

    /**
     * Apply updated terminal settings to every live instance (Settings Save,
     * Phase 4d). `term.options.*` re-applies live; a fit keeps geometry sane.
     */
    applySettings(settings: TerminalSettings): void {
        for (const e of pool.values()) {
            e.term.options.fontFamily = settings.fontFamily;
            e.term.options.fontSize = settings.fontSize;
            e.term.options.scrollback = settings.scrollback;
            try {
                e.fit.fit();
            } catch {
                /* zero-sized container; fit again on activation */
            }
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