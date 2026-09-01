// components/tree.ts — recursive session tree + live-search results +
// toolbar (Phase 4b tasks 1 & 2; master plan §6 Tree/Search).
//
// Renders store.tree recursively (folders with chevron toggle + descendant
// count, sessions), or a flat Results list when store.searchQ is non-empty.
// Selection lives in the store; double-click connects; context menus
// (Connect/Edit/Duplicate/Move to…/Delete), F2 inline rename, inline new
// folder. Expand/collapse is component-local (not persisted in v1).

import { SessionService } from "../../bindings/shelve/internal/wailsvc";
import { store, type NodeDTO, type SearchResultDTO, type StoreState } from "../store";
import { openContextMenu, type MenuItem } from "./context-menu";
import { openSessionEditor } from "./session-editor";
import { confirmDialog } from "./confirm";
import { toast } from "./toasts";

// Folders the user has collapsed (component-local; not persisted, v1).
const collapsed = new Set<string>();
let inlineCreate: { parentID: string } | null = null;
let inlineRename: { id: string; kind: string; value: string } | null = null;

let bodyHost: HTMLElement | null = null;
let unsub: (() => void) | null = null;

// ------------------------------------------------------------- actions ---

/** Open a new-session draft rooted at parentID ("" = root). */
export function openNewSession(parentID: string): void {
    void openSessionEditor({ mode: "create", parentID }).then((saved) => {
        if (saved) {
            void store.refreshTree();
        }
    });
}

/** Start an inline "new folder" input row under parentID. */
export function openNewFolderAt(parentID: string): void {
    inlineCreate = { parentID };
    rerender();
}

/** Connect a stored session (double-click or menu). */
export function connectSession(sessionID: string): void {
    void store.connectSession(sessionID);
}

/** Find a node by id across the whole tree (Phase 4d shortcuts helper). */
export function findTreeNode(nodes: NodeDTO[], id: string): NodeDTO | null {
    for (const n of nodes) {
        if (n.id === id) {
            return n;
        }
        const hit = findTreeNode(n.children, id);
        if (hit) {
            return hit;
        }
    }
    return null;
}

/** F2 (shortcuts router): start inline rename for the selected node. */
export function renameSelectedNode(): void {
    const { tree, selectedID } = store.getState();
    if (!selectedID) {
        return;
    }
    const n = findTreeNode(tree, selectedID);
    if (n) {
        startRename(n.id, n.kind, n.name);
    }
}

/** Delete (shortcuts router): confirm (A8) then delete the selected node. */
export function deleteSelectedNode(): void {
    const { tree, selectedID } = store.getState();
    if (!selectedID) {
        return;
    }
    const n = findTreeNode(tree, selectedID);
    if (n) {
        void deleteNode(n);
    }
}

// ------------------------------------------------------------ rendering ---

/** Render the tree/search body into host (re-renders on relevant store changes). */
export function renderTreeBody(host: HTMLElement): void {
    bodyHost = host;
    if (unsub) {
        unsub();
    }
    // The tree renders exactly three store fields (tree, searchQ,
    // selectedID). Filter out updates to unrelated state — e.g. the 2 s
    // monitor:metrics snapshots (plan P004), tab status, sftp progress —
    // so they cannot trigger a full DOM rebuild that destroys a focused
    // inline create/rename input and loses the typed text.
    let prev = pickTreeState(store.getState());
    unsub = store.subscribe((s) => {
        const next = pickTreeState(s);
        if (next.tree !== prev.tree || next.searchQ !== prev.searchQ || next.selectedID !== prev.selectedID) {
            prev = next;
            rerender();
        }
    });
    rerender();
}

/** Subset of the store the tree actually renders. */
function pickTreeState(s: StoreState): Pick<StoreState, "tree" | "searchQ" | "selectedID"> {
    return { tree: s.tree, searchQ: s.searchQ, selectedID: s.selectedID };
}

function rerender(): void {
    if (!bodyHost) {
        return;
    }
    const { tree, searchQ } = store.getState();
    bodyHost.textContent = "";
    if (searchQ.trim()) {
        renderResults(bodyHost, searchQ.trim());
    } else if (tree.length === 0 && !inlineCreate) {
        renderEmptyState(bodyHost);
    } else {
        renderNodes(bodyHost, tree, 0, "");
    }
}

function renderEmptyState(host: HTMLElement): void {
    const empty = document.createElement("div");
    empty.className = "empty-state tree-empty";
    const title = document.createElement("div");
    title.textContent = "No sessions yet";
    const hint = document.createElement("div");
    hint.className = "hint";
    hint.textContent = "Press [+ Session] to create one.";
    empty.append(title, hint);
    host.appendChild(empty);
}

function renderNodes(host: HTMLElement, nodes: NodeDTO[], depth: number, parentID: string): void {
    // Inline new-folder row for this level.
    if (inlineCreate && inlineCreate.parentID === parentID) {
        host.appendChild(makeInlineCreateRow(depth));
    }
    for (const node of nodes) {
        if (node.kind === "folder") {
            host.appendChild(makeFolderRow(node, depth));
        } else {
            host.appendChild(makeSessionRow(node, depth));
        }
    }
    // Inline new-folder row directly under a specific parent folder is
    // rendered within that folder's expanded children block.
}

function makeFolderRow(node: NodeDTO, depth: number): HTMLElement {
    // A folder is a block wrapper holding a clickable label row plus an
    // indented children block (so children are NOT flex items of the row).
    const wrap = document.createElement("div");
    wrap.className = "tree-folder";
    wrap.dataset.id = node.id;
    wrap.dataset.kind = "folder";

    const isCollapsed = collapsed.has(node.id);

    if (inlineRename && inlineRename.id === node.id && inlineRename.kind === "folder") {
        wrap.appendChild(makeInlineRenameInput(node.id, "folder", node.name));
        return wrap;
    }

    const row = document.createElement("div");
    row.className = "tree-row folder";
    row.style.paddingLeft = `${8 + depth * 16}px`;

    const chevron = document.createElement("span");
    chevron.className = "chevron";
    chevron.textContent = isCollapsed ? "▶" : "▼";
    const icon = document.createElement("span");
    icon.className = "tree-icon";
    icon.textContent = "📁";
    const name = document.createElement("span");
    name.className = "tree-name";
    name.textContent = node.name;
    name.title = node.name;
    const count = document.createElement("span");
    count.className = "badge";
    const n = countChildren(node);
    count.textContent = String(n);
    count.title = `${n} item${n === 1 ? "" : "s"}`;
    row.append(chevron, icon, name, count);

    row.classList.toggle("selected", store.getState().selectedID === node.id);
    row.addEventListener("click", () => store.selectNode(node.id));
    row.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        store.selectNode(node.id);
        openFolderContext(e.clientX, e.clientY, node, parentPathOf(node));
    });

    chevron.addEventListener("click", (e) => {
        e.stopPropagation();
        if (collapsed.has(node.id)) {
            collapsed.delete(node.id);
        } else {
            collapsed.add(node.id);
        }
        rerender();
    });
    row.addEventListener("dblclick", () => {
        if (collapsed.has(node.id)) {
            collapsed.delete(node.id);
        } else {
            collapsed.add(node.id);
        }
        rerender();
    });

    wrap.appendChild(row);

    if (!isCollapsed) {
        const children = document.createElement("div");
        children.className = "tree-children";
        if (inlineCreate && inlineCreate.parentID === node.id) {
            children.appendChild(makeInlineCreateRow(depth + 1));
        }
        renderNodesInner(children, node.children, depth + 1);
        wrap.appendChild(children);
    }
    return wrap;
}

/** Renders a folder's children (helpers extracted for reuse). */
function renderNodesInner(host: HTMLElement, nodes: NodeDTO[], depth: number): void {
    for (const node of nodes) {
        if (node.kind === "folder") {
            host.appendChild(makeFolderRow(node, depth));
        } else {
            host.appendChild(makeSessionRow(node, depth));
        }
    }
}

function makeSessionRow(node: NodeDTO, depth: number): HTMLElement {
    const row = document.createElement("div");
    row.className = "tree-row session";
    row.style.paddingLeft = `${8 + depth * 16}px`;
    row.dataset.id = node.id;
    row.dataset.kind = "session";

    const icon = document.createElement("span");
    icon.className = "tree-icon";
    icon.textContent = "🖥";
    const name = document.createElement("span");
    name.className = "tree-name";
    name.textContent = node.name;
    name.title = node.name;
    row.append(icon, name);

    row.classList.toggle("selected", store.getState().selectedID === node.id);
    row.addEventListener("click", () => store.selectNode(node.id));
    row.addEventListener("dblclick", () => connectSession(node.id));
    row.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        store.selectNode(node.id);
        openSessionContext(e.clientX, e.clientY, node);
    });

    if (inlineRename && inlineRename.id === node.id && inlineRename.kind === "session") {
        row.replaceChildren(makeInlineRenameInput(node.id, "session", node.name));
    }
    return row;
}

function makeInlineCreateRow(depth: number): HTMLElement {
    const row = document.createElement("div");
    row.className = "tree-row inline-row";
    row.style.paddingLeft = `${8 + depth * 16}px`;
    const input = document.createElement("input");
    input.type = "text";
    input.className = "input inline-input";
    input.placeholder = "Folder name…";
    const commit = async () => {
        const v = input.value.trim();
        const target = inlineCreate ? inlineCreate.parentID : "";
        inlineCreate = null;
        if (!v) {
            rerender();
            return;
        }
        try {
            await SessionService.CreateFolder(target, v);
            void store.refreshTree();
        } catch (err) {
            toast("error", String(err));
            rerender();
        }
    };
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
            e.preventDefault();
            void commit();
        } else if (e.key === "Escape") {
            inlineCreate = null;
            rerender();
        }
    });
    input.addEventListener("blur", () => {
        // Commit on blur only if still creating (Enter path already cleared).
        if (inlineCreate) {
            void commit();
        }
    });
    row.appendChild(input);
    requestAnimationFrame(() => input.focus());
    return row;
}

function makeInlineRenameInput(id: string, kind: string, value: string): HTMLElement {
    const wrap = document.createElement("div");
    wrap.className = "tree-row inline-row";
    const input = document.createElement("input");
    input.type = "text";
    input.className = "input inline-input";
    input.value = value;
    const commit = async () => {
        const v = input.value.trim();
        inlineRename = null;
        if (!v || v === value) {
            rerender();
            return;
        }
        try {
            if (kind === "folder") {
                await SessionService.RenameFolder(id, v);
            } else {
                await renameSession(id, v);
            }
            void store.refreshTree();
        } catch (err) {
            toast("error", String(err));
            rerender();
        }
    };
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
            e.preventDefault();
            void commit();
        } else if (e.key === "Escape") {
            inlineRename = null;
            rerender();
        }
    });
    input.addEventListener("blur", () => {
        if (inlineRename) {
            void commit();
        }
    });
    wrap.appendChild(input);
    requestAnimationFrame(() => {
        input.focus();
        input.select();
    });
    return wrap;
}

async function renameSession(id: string, newName: string): Promise<void> {
    const dto = (await SessionService.Session(id)) as unknown as import("../store").SessionDTO;
    await SessionService.UpdateSession({
        id: dto.id,
        folderId: dto.folderId,
        name: newName,
        host: dto.host,
        port: dto.port,
        user: dto.user,
        authType: dto.authType,
        password: "",
        keyPath: dto.keyPath,
        jumpHosts: dto.jumpHosts.map((j) => ({
            host: j.host,
            port: j.port,
            user: j.user,
            authType: j.authType,
            password: "",
            keyPath: j.keyPath,
        })),
        extraArgs: dto.extraArgs,
    });
}

// ---------------------------------------------------------- context menu ---

function openFolderContext(x: number, y: number, node: NodeDTO, _path: string): void {
    const items: MenuItem[] = [
        { label: "New Session", action: () => openNewSession(node.id) },
        { label: "New Folder", action: () => openNewFolderAt(node.id) },
        { label: "Move to…", action: () => openMoveMenu(x, y, node.id) },
        { label: "Rename", action: () => startRename(node.id, "folder", node.name) },
        { label: "Delete", danger: true, action: () => deleteNode(node) },
    ];
    openContextMenu(x, y, items);
}

function openSessionContext(x: number, y: number, node: NodeDTO): void {
    const items: MenuItem[] = [
        { label: "Connect", action: () => connectSession(node.id) },
        { label: "Edit", action: () => editSession(node.id) },
        { label: "Duplicate", action: () => duplicateSession(node.id) },
        { label: "Move to…", action: () => openMoveMenu(x, y, node.id) },
        { label: "Rename", action: () => startRename(node.id, "session", node.name) },
        { label: "Delete", danger: true, action: () => deleteNode(node) },
    ];
    openContextMenu(x, y, items);
}

function startRename(id: string, kind: string, value: string): void {
    inlineRename = { id, kind, value };
    rerender();
}

async function editSession(id: string): Promise<void> {
    const dto = (await SessionService.Session(id)) as unknown as import("../store").SessionDTO;
    await openSessionEditor({ mode: "edit", parentID: dto.folderId, initial: dto });
}

async function duplicateSession(id: string): Promise<void> {
    try {
        await SessionService.DuplicateSession(id);
        void store.refreshTree();
        toast("info", "Session duplicated");
    } catch (err) {
        toast("error", String(err));
    }
}

/** Build the Move-to… destination menu (excludes the node + descendants). */
function openMoveMenu(x: number, y: number, nodeID: string): void {
    const { tree } = store.getState();
    const exclude = new Set<string>();
    const collect = (ns: NodeDTO[]) => {
        for (const n of ns) {
            if (n.id === nodeID) {
                collectSubtree(n, exclude);
            } else {
                collect(n.children);
            }
        }
    };
    const collectSubtree = (n: NodeDTO, acc: Set<string>) => {
        acc.add(n.id);
        for (const c of n.children) {
            collectSubtree(c, acc);
        }
    };
    collect(tree);

    const dests: Array<{ id: string; path: string }> = [{ id: "", path: "Root" }];
    const walk = (ns: NodeDTO[], prefix: string) => {
        for (const n of ns) {
            if (n.kind !== "folder" || exclude.has(n.id)) {
                continue;
            }
            const path = prefix ? `${prefix}/${n.name}` : n.name;
            dests.push({ id: n.id, path });
            walk(n.children, path);
        }
    };
    walk(tree, "");

    const items: MenuItem[] = dests.map((d) => ({
        label: d.path,
        action: () => void doMove(nodeID, d.id),
    }));
    openContextMenu(x, y, items);
}

async function doMove(nodeID: string, newParentID: string): Promise<void> {
    try {
        await SessionService.MoveNode(nodeID, newParentID, -1);
        void store.refreshTree();
        toast("info", "Moved");
    } catch (err) {
        toast("error", String(err));
    }
}

/** Confirm (A8) then delete a node, using the loaded tree for the count. */
async function deleteNode(node: NodeDTO): Promise<void> {
    const counts = subtreeCounts(node);
    const parts: string[] = [];
    if (counts.sessions > 0) {
        parts.push(`${counts.sessions} session${counts.sessions === 1 ? "" : "s"}`);
    }
    if (counts.folders > 0) {
        parts.push(`${counts.folders} folder${counts.folders === 1 ? "" : "s"}`);
    }
    const what = parts.length ? parts.join(" and ") : "1 item";
    const ok = await confirmDialog({
        title: `Delete ${node.name}?`,
        message: `This will permanently remove ${what}.`,
        confirmLabel: "Delete",
        danger: true,
    });
    if (!ok) {
        return;
    }
    try {
        await SessionService.DeleteNode(node.id);
        void store.refreshTree();
        store.selectNode(null);
        toast("info", "Deleted");
    } catch (err) {
        toast("error", String(err));
    }
}

// --------------------------------------------------------------- helpers ---

function countChildren(node: NodeDTO): number {
    let n = 0;
    const walk = (ns: NodeDTO[]) => {
        for (const x of ns) {
            n++;
            walk(x.children);
        }
    };
    walk(node.children);
    return n;
}

function subtreeCounts(node: NodeDTO): { sessions: number; folders: number } {
    const acc = { sessions: 0, folders: 0 };
    const walk = (ns: NodeDTO[]) => {
        for (const x of ns) {
            if (x.kind === "folder") {
                acc.folders++;
                walk(x.children);
            } else {
                acc.sessions++;
            }
        }
    };
    walk(node.children);
    return acc;
}

// Used by folder context menu path display (kept minimal for now).
function parentPathOf(_node: NodeDTO): string {
    return "";
}

// ------------------------------------------------------------- results ---

function highlight(text: string, q: string): HTMLElement {
    const span = document.createElement("span");
    const lower = text.toLowerCase();
    const idx = lower.indexOf(q.toLowerCase());
    if (idx === -1) {
        span.textContent = text;
        return span;
    }
    const mark = document.createElement("mark");
    mark.textContent = text.slice(idx, idx + q.length);
    span.append(text.slice(0, idx), mark, text.slice(idx + q.length));
    return span;
}

function renderResults(host: HTMLElement, q: string): void {
    host.textContent = "";
    void queryResults(host, q);
}

async function queryResults(host: HTMLElement, q: string): Promise<void> {
    if (window.__dsmDev) {
        console.time(`search(${q})`);
    }
    try {
        const results = (await SessionService.Search(q)) as unknown as SearchResultDTO[];
        if (store.getState().searchQ.trim() !== q) {
            return; // query changed while awaiting
        }
        renderResultList(host, q, results);
    } catch (err) {
        if (window.__dsmDev) {
            console.timeEnd(`search(${q})`);
        }
        host.textContent = "";
        const empty = document.createElement("div");
        empty.className = "empty-state tree-empty";
        empty.textContent = String(err);
        host.appendChild(empty);
    }
    if (window.__dsmDev) {
        console.timeEnd(`search(${q})`);
    }
}

function renderResultList(host: HTMLElement, q: string, results: SearchResultDTO[]): void {
    host.textContent = "";
    if (results.length === 0) {
        const empty = document.createElement("div");
        empty.className = "empty-state tree-empty";
        const t = document.createElement("div");
        t.textContent = `No results for "${q}"`;
        empty.appendChild(t);
        host.appendChild(empty);
        return;
    }
    for (const r of results) {
        const row = document.createElement("div");
        row.className = "tree-row session result";
        row.dataset.id = r.id;
        row.dataset.kind = "session";

        const name = highlight(r.name, q);
        name.className = "tree-name";
        const meta = document.createElement("span");
        meta.className = "result-meta";
        meta.textContent = `${r.host}${r.folderPath ? ` — ${r.folderPath}` : ""}`;

        row.append(name, meta);
        row.classList.toggle("selected", store.getState().selectedID === r.id);
        row.addEventListener("click", () => store.selectNode(r.id));
        row.addEventListener("dblclick", () => connectSession(r.id));
        row.addEventListener("contextmenu", (e) => {
            e.preventDefault();
            store.selectNode(r.id);
            openSessionContext(e.clientX, e.clientY, { kind: "session", id: r.id, name: r.name, children: [] });
        });
        host.appendChild(row);
    }
}