// components/sftp-panel.ts — left-panel SFTP browser (Phase 5c; master plan
// §6 SFTP panel). Replaces the session tree when settings.sftpBrowserEnabled
// is on AND the active tab is ready (the decision lives in store.ts via
// sftpPanelVisible; the shell mounts/unmounts by toggling display).
//
// Header: back button + breadcrumb (home = remote $HOME, clickable segments),
// [Upload] [New folder] [Refresh]. List rows: icon / name / size / modified.
// Double-click opens (dir → navigate, text-like → EditRemoteText, else →
// DownloadThenSave). Context menu: Open / Edit as text / Download… / Upload
// to here… / New folder… / Rename… / Delete… (+ Cancel edit while editing).
// Footer carries the transfer progress line fed by the store's sftp:progress
// cache and the "Editing <path> …" status line.

import { SftpService } from "../../bindings/shelve/internal/wailsvc";
import { store, type SftpEntryDTO } from "../store";
import { openContextMenu, type MenuItem } from "./context-menu";
import { confirmDialog } from "./confirm";
import { openDialog } from "../ui/dialog";
import { toast } from "./toasts";

// ----------------------------------------------------------------- module ---

let host: HTMLElement | null = null;
let unsub: (() => void) | null = null;

let tabID = ""; // active ready tab the panel is browsing
let curPath = "~"; // "~"-relative remote cwd (resolve() expands it server-side)
let entries: SftpEntryDTO[] | null = null;
let loading = false;
let errorMsg: string | null = null;
let selected: string | null = null;
let inlineCreate = false;
let inlineRename: { name: string } | null = null;
let editingPath: string | null = null; // full remote path being edited

let backBtn: HTMLButtonElement | null = null;
let breadcrumbEl: HTMLElement | null = null;
let listEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let progressEl: HTMLElement | null = null;

const modFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" });

// ----------------------------------------------------------- path helpers ---

/** Join a remote directory with a child name, keeping the "~" home prefix. */
function joinRemote(base: string, name: string): string {
    if (base === "" || base === "~") {
        return `~/${name}`;
    }
    return `${base}/${name}`;
}

/** Parent of a "~"-relative path ("~" → "~"). */
function parentOf(p: string): string {
    if (!p || p === "~") {
        return "~";
    }
    const s = p.startsWith("~/") ? p.slice(2) : p;
    const i = s.lastIndexOf("/");
    if (i <= 0) {
        return "~";
    }
    return `~/${s.slice(0, i)}`;
}

/** Path segments after the home prefix ([] for home itself). */
function segmentsOf(p: string): string[] {
    if (!p || p === "~") {
        return [];
    }
    const s = p.startsWith("~/") ? p.slice(2) : p;
    return s.split("/").filter(Boolean);
}

/** Rebuild the full "~"-relative path for the segment at `idx`. */
function segmentPath(idx: number): string {
    const segs = segmentsOf(curPath);
    return `~/${segs.slice(0, idx + 1).join("/")}`;
}

// ------------------------------------------------------------ lifecycle ---

/** Mount the panel into `target`. Call once per shell; display is toggled. */
export function renderSftpPanel(target: HTMLElement): void {
    target.textContent = "";
    host = target;
    if (unsub) {
        unsub();
    }
    unsub = store.subscribe(() => refresh());
    buildStatic(target);

    const { activeTabID, tabs } = store.getState();
    const t = tabs.find((x) => x.id === activeTabID);
    tabID = t && t.state === "ready" ? activeTabID! : "";
    curPath = "~";
    selected = null;
    inlineCreate = false;
    inlineRename = null;
    editingPath = null;
    if (tabID) {
        void loadList();
    } else {
        entries = null;
    }
    updateFooter();
    updateStatus();
    renderList();
}

/** React to store changes: tab switch, transfer cache, other churn. */
function refresh(): void {
    if (!host) {
        return;
    }
    const { activeTabID, tabs } = store.getState();
    const t = tabs.find((x) => x.id === activeTabID);
    const ready = t && t.state === "ready" ? activeTabID! : "";
    if (ready !== tabID) {
        // Active tab changed → reset navigation and any live edit.
        tabID = ready;
        curPath = "~";
        selected = null;
        inlineCreate = false;
        inlineRename = null;
        editingPath = null;
        if (ready) {
            void loadList();
        } else {
            entries = null;
        }
    }
    updateFooter();
    // Don't clobber an in-progress inline input during unrelated churn.
    if (isTypingInPanel()) {
        renderBreadcrumb();
        return;
    }
    renderBreadcrumb();
    renderList();
}

/** True when the user is typing in an inline input inside the panel. */
function isTypingInPanel(): boolean {
    const el = document.activeElement;
    if (!host || !(el instanceof HTMLElement) || !host.contains(el)) {
        return false;
    }
    return el.tagName === "INPUT" || el.tagName === "TEXTAREA";
}

// ---------------------------------------------------------- static chrome ---

function mkBtn(label: string, onClick: () => void): HTMLButtonElement {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn small";
    b.textContent = label;
    b.addEventListener("click", onClick);
    return b;
}

function buildStatic(target: HTMLElement): void {
    const header = document.createElement("div");
    header.className = "sftp-header";
    backBtn = document.createElement("button");
    backBtn.type = "button";
    backBtn.className = "btn small icon-btn";
    backBtn.textContent = "←";
    backBtn.title = "Go up one level";
    backBtn.setAttribute("aria-label", "Up one level");
    backBtn.addEventListener("click", () => {
        const parent = parentOf(curPath);
        if (parent !== curPath) {
            curPath = parent;
            void loadList();
        }
    });
    breadcrumbEl = document.createElement("nav");
    breadcrumbEl.className = "sftp-breadcrumb";
    header.append(backBtn, breadcrumbEl);

    const toolbar = document.createElement("div");
    toolbar.className = "toolbar sftp-toolbar";
    toolbar.append(
        mkBtn("Upload", () => void doUpload()),
        mkBtn("New folder", () => {
            inlineCreate = true;
            renderList();
        }),
        mkBtn("Refresh", () => void loadList()),
    );

    listEl = document.createElement("div");
    listEl.className = "sftp-list";

    const footer = document.createElement("div");
    footer.className = "sftp-footer";
    progressEl = document.createElement("div");
    progressEl.className = "sftp-progress";
    statusEl = document.createElement("div");
    statusEl.className = "sftp-status";
    footer.append(progressEl, statusEl);

    target.append(header, toolbar, listEl, footer);
}

// ---------------------------------------------------------------- listing ---

async function loadList(): Promise<void> {
    if (!tabID) {
        entries = null;
        errorMsg = null;
        loading = false;
        renderList();
        return;
    }
    loading = true;
    errorMsg = null;
    renderList();
    try {
        const res = (await SftpService.List(tabID, curPath)) as unknown as SftpEntryDTO[];
        if (store.getState().activeTabID !== tabID) {
            return; // tab switched while awaiting
        }
        entries = res;
        selected = null;
    } catch (err) {
        entries = null;
        errorMsg = String(err);
    } finally {
        loading = false;
        renderList();
    }
}

function renderList(): void {
    if (!listEl) {
        return;
    }
    listEl.textContent = "";
    if (errorMsg) {
        listEl.appendChild(mkCentered(errorMsg));
        return;
    }
    if (loading && entries === null) {
        listEl.appendChild(mkCentered("Loading…"));
        return;
    }
    if (entries === null) {
        return;
    }

    if (inlineCreate) {
        listEl.appendChild(makeInlineCreateRow());
    }
    if (entries.length === 0) {
        const empty = document.createElement("div");
        empty.className = "empty-state tree-empty";
        const title = document.createElement("div");
        title.textContent = "This folder is empty";
        const hint = document.createElement("div");
        hint.className = "hint";
        hint.textContent = "Use [Upload] or [New folder] to add files.";
        empty.append(title, hint);
        listEl.appendChild(empty);
        return;
    }
    for (const e of entries) {
        listEl.appendChild(makeRow(e));
    }
}

function mkCentered(text: string): HTMLElement {
    const div = document.createElement("div");
    div.className = "empty-state tree-empty";
    div.textContent = text;
    return div;
}

function makeRow(entry: SftpEntryDTO): HTMLElement {
    const full = joinRemote(curPath, entry.name);
    if (inlineRename && inlineRename.name === entry.name) {
        return makeInlineRenameRow(entry);
    }

    const row = document.createElement("div");
    row.className = "sftp-row";
    row.dataset.name = entry.name;
    if (editingPath === full) {
        row.classList.add("editing");
    }

    const icon = document.createElement("span");
    icon.className = "sftp-icon";
    icon.textContent = entry.isDir ? "📁" : entry.textLike ? "📄" : "📦";

    const name = document.createElement("span");
    name.className = "sftp-name";
    name.textContent = entry.isDir ? entry.name + "/" : entry.name;
    name.title = full;

    const size = document.createElement("span");
    size.className = "sftp-size";
    size.textContent = entry.isDir ? "—" : humanSize(entry.size);

    const mod = document.createElement("span");
    mod.className = "sftp-modified";
    let modText = "—";
    if (!entry.isDir && entry.modTime) {
        const d = new Date(entry.modTime);
        if (!Number.isNaN(d.getTime())) {
            modText = modFmt.format(d);
        }
    }
    mod.textContent = modText;

    if (editingPath === full) {
        const tag = document.createElement("span");
        tag.className = "sftp-editing-tag";
        tag.textContent = "editing…";
        tag.title = "Open in your text editor; auto-saves when it stops writing for 3 s.";
        name.append(" ", tag);
    }

    row.append(icon, name, size, mod);
    row.classList.toggle("selected", selected === entry.name);
    row.addEventListener("click", () => {
        selected = entry.name;
        renderList();
    });
    row.addEventListener("dblclick", () => openEntry(entry));
    row.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        selected = entry.name;
        openPanelContext(e.clientX, e.clientY, entry);
        renderList();
    });
    return row;
}

// ------------------------------------------------------------- operations ---

function openEntry(entry: SftpEntryDTO): void {
    if (entry.isDir) {
        curPath = joinRemote(curPath, entry.name);
        selected = null;
        void loadList();
        return;
    }
    if (entry.textLike) {
        void startEdit(entry);
        return;
    }
    void downloadSave(entry);
}

async function startEdit(entry: SftpEntryDTO): Promise<void> {
    const full = joinRemote(curPath, entry.name);
    try {
        await SftpService.EditRemoteText(tabID, full);
        editingPath = full;
        renderList();
        updateStatus();
    } catch (err) {
        toast("error", String(err));
    }
}

async function cancelEdit(): Promise<void> {
    try {
        await SftpService.CancelEdit(tabID);
        editingPath = null;
        renderList();
        updateStatus();
    } catch (err) {
        toast("error", String(err));
    }
}

async function downloadSave(entry: SftpEntryDTO): Promise<void> {
    try {
        // Backend DownloadThenSave emits an app:toast with the saved path.
        await SftpService.DownloadThenSave(tabID, joinRemote(curPath, entry.name));
    } catch (err) {
        toast("error", String(err));
    }
}

async function removeEntry(entry: SftpEntryDTO): Promise<void> {
    const ok = await confirmDialog({
        title: `Delete ${entry.name}?`,
        message: entry.isDir
            ? "Only empty folders can be deleted. Files inside must be removed first."
            : "This will permanently remove the file on the remote host.",
        confirmLabel: "Delete",
        danger: true,
    });
    if (!ok) {
        return;
    }
    try {
        await SftpService.Remove(tabID, joinRemote(curPath, entry.name));
        void loadList();
        toast("info", "Deleted");
    } catch (err) {
        toast("error", String(err));
    }
}

async function doUpload(): Promise<void> {
    let paths: string[] = [];
    try {
        const picked = (await SftpService.PickLocalFiles(tabID, true)) as unknown as
            | string[]
            | null;
        paths = picked ?? [];
    } catch (err) {
        const msg = String(err);
        if (/unsupported|no native/i.test(msg)) {
            // 5b fallback: manual multi-path prompt when the dialog is out.
            paths = await promptLocalPaths();
        } else {
            toast("error", msg);
            return;
        }
    }
    if (paths.length === 0) {
        return;
    }
    try {
        await SftpService.Upload(tabID, paths, curPath);
        void loadList();
        toast("info", "Upload complete");
    } catch (err) {
        toast("error", String(err));
    }
}

/** Manual multi-path upload prompt (5b fallback; ErrSftpDialogUnsupported). */
function promptLocalPaths(): Promise<string[]> {
    return new Promise((resolve) => {
        const body = document.createElement("div");
        body.style.display = "flex";
        body.style.flexDirection = "column";
        body.style.gap = "10px";
        const intro = document.createElement("p");
        intro.style.margin = "0";
        intro.textContent = "Enter the absolute path of each file to upload, one per line:";
        const ta = document.createElement("textarea");
        ta.className = "input mono";
        ta.rows = 6;
        ta.placeholder = "/home/me/report.pdf\n/home/me/notes.txt";
        ta.autocomplete = "off";
        ta.spellcheck = false;
        body.append(intro, ta);

        const footer = document.createElement("div");
        const cancel = document.createElement("button");
        cancel.type = "button";
        cancel.className = "btn";
        cancel.textContent = "Cancel";
        const upload = document.createElement("button");
        upload.type = "button";
        upload.className = "btn primary";
        upload.textContent = "Upload";

        const finish = (paths: string[]) => {
            dlg.close(paths);
            if (dlg.el.parentNode) {
                dlg.el.remove();
            }
        };
        cancel.addEventListener("click", () => finish([]));
        const submit = () => {
            const parts = ta.value
                .split(/[\n,]/)
                .map((s) => s.trim())
                .filter((s) => s.length > 0);
            finish(parts);
        };
        upload.addEventListener("click", submit);
        ta.addEventListener("keydown", (e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
                e.preventDefault();
                submit();
            }
        });
        footer.append(cancel, upload);

        const dlg = openDialog<string[]>({
            title: "Upload files",
            body,
            footer,
            backdropClose: true,
            width: 460,
        });
        requestAnimationFrame(() => ta.focus());
        void dlg.done.then((p) => resolve(p ?? []));
    });
}

// ----------------------------------------------------------- inline rows ---

function makeInlineCreateRow(): HTMLElement {
    const row = document.createElement("div");
    row.className = "sftp-row inline-row";
    const input = document.createElement("input");
    input.type = "text";
    input.className = "input inline-input";
    input.placeholder = "Folder name…";
    const commit = async () => {
        const v = input.value.trim();
        inlineCreate = false;
        if (!v) {
            renderList();
            return;
        }
        try {
            await SftpService.Mkdir(tabID, joinRemote(curPath, v));
            void loadList();
        } catch (err) {
            toast("error", String(err));
            renderList();
        }
    };
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
            e.preventDefault();
            void commit();
        } else if (e.key === "Escape") {
            inlineCreate = false;
            renderList();
        }
    });
    input.addEventListener("blur", () => {
        if (inlineCreate) {
            void commit();
        }
    });
    row.appendChild(input);
    requestAnimationFrame(() => input.focus());
    return row;
}

function makeInlineRenameRow(entry: SftpEntryDTO): HTMLElement {
    const row = document.createElement("div");
    row.className = "sftp-row inline-row";
    const input = document.createElement("input");
    input.type = "text";
    input.className = "input inline-input";
    input.value = entry.name;
    const from = joinRemote(curPath, entry.name);
    const commit = async () => {
        const v = input.value.trim();
        inlineRename = null;
        if (!v || v === entry.name) {
            renderList();
            return;
        }
        try {
            await SftpService.Rename(tabID, from, joinRemote(curPath, v));
            void loadList();
        } catch (err) {
            toast("error", String(err));
            renderList();
        }
    };
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
            e.preventDefault();
            void commit();
        } else if (e.key === "Escape") {
            inlineRename = null;
            renderList();
        }
    });
    input.addEventListener("blur", () => {
        if (inlineRename) {
            void commit();
        }
    });
    row.appendChild(input);
    requestAnimationFrame(() => {
        input.focus();
        input.select();
    });
    return row;
}

// -------------------------------------------------------------- breadcrumb ---

function renderBreadcrumb(): void {
    const bc = breadcrumbEl;
    if (!bc) {
        return;
    }
    bc.textContent = "";
    const home = document.createElement("button");
    home.type = "button";
    home.className = "crumb";
    home.textContent = "~";
    home.title = "Remote home ($HOME)";
    home.addEventListener("click", () => {
        if (curPath !== "~") {
            curPath = "~";
            void loadList();
        }
    });
    bc.appendChild(home);

    const segs = segmentsOf(curPath);
    segs.forEach((seg, i) => {
        const sep = document.createElement("span");
        sep.className = "crumb-sep";
        sep.textContent = "/";
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "crumb";
        btn.textContent = seg;
        btn.title = segmentPath(i);
        const last = i === segs.length - 1;
        btn.classList.toggle("current", last);
        if (!last) {
            btn.addEventListener("click", () => {
                curPath = segmentPath(i);
                void loadList();
            });
        }
        bc.append(sep, btn);
    });

    if (backBtn) {
        backBtn.disabled = curPath === "~";
    }
}

// ---------------------------------------------------------------- footer ---

function updateFooter(): void {
    if (!progressEl) {
        return;
    }
    const list = store.getState().sftpTransfers[tabID] || [];
    const active = [...list].reverse().find((t) => !t.finished);
    if (!active || active.error) {
        progressEl.textContent = "";
        return;
    }
    const action = active.direction === "up" ? "Uploading" : "Downloading";
    let line = `${action} ${active.fileName} — ${humanSize(active.doneBytes)}/${humanSize(active.totalBytes)}`;
    if (list.length > 1) {
        const idx = list.findIndex((t) => t.transferID === active.transferID);
        line += ` (${idx + 1}/${list.length})`;
    }
    progressEl.textContent = line;
}

function updateStatus(): void {
    if (!statusEl) {
        return;
    }
    statusEl.textContent = editingPath
        ? `Editing ${editingPath} — auto-saves when your editor stops writing for 3 s`
        : "";
}

// ------------------------------------------------------------- context menu ---

function openPanelContext(x: number, y: number, entry: SftpEntryDTO): void {
    const full = joinRemote(curPath, entry.name);
    const editingThis = editingPath === full;
    const items: MenuItem[] = [];
    if (editingThis) {
        items.push({ label: "Cancel edit", action: () => void cancelEdit() });
    }
    items.push(
        { label: "Open", action: () => openEntry(entry) },
        { label: "Edit as text", disabled: !entry.textLike, action: () => void startEdit(entry) },
        { label: "Download…", action: () => void downloadSave(entry) },
        { label: "Upload to here…", action: () => void doUpload() },
        {
            label: "New folder…",
            action: () => {
                inlineCreate = true;
                renderList();
            },
        },
        {
            label: "Rename…",
            action: () => {
                inlineRename = { name: entry.name };
                renderList();
            },
        },
        { label: "Delete…", danger: true, action: () => void removeEntry(entry) },
    );
    openContextMenu(x, y, items);
}

// ---------------------------------------------------------------- helpers ---

function humanSize(n: number): string {
    if (!Number.isFinite(n) || n <= 0) {
        return "0 B";
    }
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    let u = 0;
    let v = n;
    while (v >= 1024 && u < units.length - 1) {
        v /= 1024;
        u++;
    }
    const digits = v >= 100 ? 0 : v >= 10 ? 1 : 2;
    return `${v.toFixed(digits)} ${units[u]}`;
}