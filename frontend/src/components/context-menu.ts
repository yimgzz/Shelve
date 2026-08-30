// components/context-menu.ts — lightweight context menu (master plan §6).
// Fixed positioning, viewport-clamped, closes on Esc / outside click /
// scroll. Shared by the tree (4b), tabs (4c) and SFTP panel (5c).

export interface MenuItem {
    label: string;
    action: () => void;
    danger?: boolean;
    disabled?: boolean;
}

/** Open a context menu at the given screen coordinates. */
export function openContextMenu(x: number, y: number, items: MenuItem[]): void {
    closeContextMenu();

    const menu = document.createElement("div");
    menu.className = "context-menu";
    menu.setAttribute("role", "menu");

    for (const item of items) {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = `context-menu-item${item.danger ? " danger" : ""}`;
        btn.setAttribute("role", "menuitem");
        btn.textContent = item.label;
        btn.disabled = item.disabled ?? false;
        if (!btn.disabled) {
            btn.addEventListener("click", () => {
                closeContextMenu();
                item.action();
            });
        }
        menu.appendChild(btn);
    }

    document.body.appendChild(menu);

    // Position then clamp to viewport.
    const margin = 4;
    const rect = menu.getBoundingClientRect();
    const left = Math.min(Math.max(margin, x), window.innerWidth - rect.width - margin);
    const top = Math.min(Math.max(margin, y), window.innerHeight - rect.height - margin);
    menu.style.left = `${left}px`;
    menu.style.top = `${top}px`;

    const onOutside = (e: MouseEvent) => {
        if (!menu.contains(e.target as Node)) {
            closeContextMenu();
        }
    };
    const onKey = (e: KeyboardEvent) => {
        if (e.key === "Escape") {
            closeContextMenu();
        }
    };
    const onScroll = () => closeContextMenu();
    const onBlur = () => window.setTimeout(closeContextMenu, 0);

    document.addEventListener("mousedown", onOutside, true);
    document.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("blur", onBlur);

    (menu as unknown as { __cleanup: () => void }).__cleanup = () => {
        document.removeEventListener("mousedown", onOutside, true);
        document.removeEventListener("keydown", onKey);
        window.removeEventListener("scroll", onScroll, true);
        window.removeEventListener("blur", onBlur);
    };
}

/** Remove any open context menu. */
export function closeContextMenu(): void {
    const existing = document.querySelector<HTMLElement>(".context-menu");
    if (existing) {
        (existing as unknown as { __cleanup?: () => void }).__cleanup?.();
        existing.remove();
    }
}