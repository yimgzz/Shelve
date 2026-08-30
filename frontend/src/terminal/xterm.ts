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
//   - destroy(tabID) on tab close, destroyAll() on vault lock. All addons
//     are disposed with the terminal instance.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { TerminalService } from "../../bindings/dummy-ssh-manager/internal/wailsvc";
import type { TerminalSettings } from "../store";
import type { EffectiveTheme } from "../ui/theme";
import { bytesToB64 } from "../ui/b64";

/** Options for creating a new pooled terminal (read from settings/theme). */
export interface TermCreateOptions {
    settings: TerminalSettings;
}

/** Theme bindings when the CSS variables cannot be resolved. */
const FALLBACK_PALETTE: Record<EffectiveTheme, { background: string; foreground: string }> = {
    light: { background: "#f7f8fa", foreground: "#24292f" },
    dark: { background: "#1e1e2e", foreground: "#cdd6f4" },
};

/** Read the current terminal palette from the active theme's CSS tokens. */
function palette(): { background: string; foreground: string } {
    const cs = getComputedStyle(document.documentElement);
    const bg = cs.getPropertyValue("--terminal-bg").trim();
    const fg = cs.getPropertyValue("--terminal-fg").trim();
    if (bg && fg) {
        return { background: bg, foreground: fg };
    }
    const effective = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
    return FALLBACK_PALETTE[effective];
}

interface Entry {
    term: Terminal;
    fit: FitAddon;
    webgl: WebglAddon | null;
    ro: ResizeObserver | null;
    resizeTimer: number | null;
    raf: number | null;
    inputBuf: string;
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
            theme: { background: pal.background, foreground: pal.foreground },
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
        e.term.dispose();
    },

    /** Tear down every instance (vault lock; replaces the 4a no-op hook). */
    destroyAll(): void {
        for (const id of Array.from(pool.keys())) {
            this.destroy(id);
        }
    },
};