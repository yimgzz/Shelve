// ui/clipboard.ts — system clipboard access (plan P003 T2; phase E3).
//
// Primary path is the Electron main process clipboard through the reviewed
// preload bridge (`window.shelve.clipboard`, electron/preload.ts): it works
// regardless of secure-context or permission restrictions on the web
// Clipboard API. The web APIs stay as fallbacks so a plain-browser dev run
// still works.
//
// Security (master plan §8.3): clipboard payloads are transient in memory,
// never persisted, never sent anywhere except the SSH channel / OS clipboard,
// and never logged — failures surface only as generic results.

/** Copy text to the system clipboard. Resolves true when any path succeeded. */
export async function copyText(text: string): Promise<boolean> {
    try {
        if (window.shelve && window.shelve.clipboard) {
            await window.shelve.clipboard.writeText(text);
            return true;
        }
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
        if (window.shelve && window.shelve.clipboard) {
            return await window.shelve.clipboard.readText();
        }
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
