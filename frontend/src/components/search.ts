// components/search.ts — left-panel live-search header (Phase 4b task 2;
// master plan §6 Search). 100 ms debounce, × clear button, Esc clears.
// The query is written to store.searchQ; the tree body (tree.ts) swaps to
// a flat Results list whenever searchQ is non-empty. focusSearch() is the
// hook for the 4d Ctrl+K/Ctrl+L shortcut.

import { store } from "../store";

const DEBOUNCE_MS = 100;

let searchInput: HTMLInputElement | null = null;

/** Focus the search box (used by the Ctrl+K/Ctrl+L shortcut in 4d). */
export function focusSearch(): void {
    searchInput?.focus();
    searchInput?.select();
}

/** Render the search header into host. Call once per shell mount. */
export function renderSearch(host: HTMLElement): void {
    host.textContent = "";

    const wrap = document.createElement("div");
    wrap.className = "search-wrap";

    const input = document.createElement("input");
    input.type = "text";
    input.className = "input search-input";
    input.placeholder = "Search sessions…  Ctrl K";
    input.autocomplete = "off";
    input.spellcheck = false;
    searchInput = input;

    const clear = document.createElement("button");
    clear.type = "button";
    clear.className = "search-clear";
    clear.textContent = "×";
    clear.title = "Clear search";
    clear.style.display = "none";

    let timer: number | null = null;

    const syncClear = () => {
        clear.style.display = input.value ? "" : "none";
    };
    const clearSearch = () => {
        if (timer !== null) {
            window.clearTimeout(timer);
            timer = null;
        }
        input.value = "";
        clear.style.display = "none";
        store.setSearchQ("");
    };

    input.addEventListener("input", () => {
        syncClear();
        if (timer !== null) {
            window.clearTimeout(timer);
        }
        timer = window.setTimeout(() => store.setSearchQ(input.value), DEBOUNCE_MS);
    });
    input.addEventListener("keydown", (e) => {
        if (e.key === "Escape") {
            e.preventDefault();
            clearSearch();
        }
    });
    clear.addEventListener("click", () => {
        clearSearch();
        input.focus();
    });

    // Reflect external resets (e.g. Esc elsewhere or lock) back to the box.
    const unsub = store.subscribe((s) => {
        if (s.searchQ === "") {
            if (input.value !== "") {
                input.value = "";
                clear.style.display = "none";
            }
        } else {
            syncClear();
        }
    });

    wrap.append(input, clear);
    host.appendChild(wrap);

    // Keep the subscription alive for the shell's lifetime. The host is
    // torn down on lock/unlock remount; release there to avoid leaks.
    (wrap as unknown as { __unsub?: () => void }).__unsub = unsub;
}