// ui/clipboard.ts — system clipboard access (plan P003 T2).
//
// Primary path is the Wails runtime Clipboard module: it talks to the Go
// backend's system clipboard, so it works under WebKitGTK regardless of
// secure-context or permission restrictions on the web Clipboard API. The
// web APIs are fallbacks so plain-browser dev runs (`wails3 dev`) still work.
//
// Security (master plan §8.3): clipboard payloads are transient in memory,
// never persisted, never sent anywhere except the SSH channel / OS clipboard,
// and never logged — failures surface only as generic results.

import { Clipboard } from "@wailsio/runtime";

/** Copy text to the system clipboard. Resolves true when any path succeeded. */
export async function copyText(text: string): Promise<boolean> {
    try {
        await Clipboard.SetText(text);
        return true;
    } catch {
        /* fall through to the web API */
    }
    if (navigator.clipboard) {
        try {
            await navigator.clipboard.writeText(text);
            return true;
        } catch {
            /* fall through to the legacy path */
        }
    }
    return legacyCopy(text);
}

/** Read text from the system clipboard. Resolves "" when unavailable. */
export async function readText(): Promise<string> {
    try {
        return await Clipboard.Text();
    } catch {
        /* fall through to the web API */
    }
    if (navigator.clipboard) {
        try {
            return await navigator.clipboard.readText();
        } catch {
            /* fall through to "" */
        }
    }
    return "";
}

/** Last-resort execCommand copy via a hidden textarea. */
function legacyCopy(text: string): boolean {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
        ok = document.execCommand("copy");
    } catch {
        ok = false;
    }
    ta.remove();
    return ok;
}