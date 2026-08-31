# Phase 5c — SFTP panel UI, integration tests, final QA

**Type:** Frontend + test infra. **Prereq:** 5b. **Sub-plan 3/3 of old Phase 5 — FINAL GATE of the roadmap (master §12 end-to-end item).**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (D4, A5), §5 (SftpService, `sftp:progress`), §6 (SFTP panel, shortcut Ctrl+Shift+E), §8 (item 8), §9 (integration), §12 (global acceptance)** before starting.

**Scope guard (important):** the panel + integration tests + final QA. No new Go PRODUCT code (test-only additions allowed).

## Goal
The left-panel SFTP browser per master §6, the full container-based integration suite for SFTP, and the manual QA pass that closes the project.

## Tasks
1. `components/sftp-panel.ts` + CSS (reuse 4a primitives):
   - Visibility rule in `store.ts` (single source of truth): `settings.sftpBrowserEnabled && activeTab && activeTab.state === "ready"` → panel replaces the tree; otherwise the tree (enabled but no active ready tab → tree + hint line "Connect to a session to open the SFTP browser").
   - Header: breadcrumb (home = remote `$HOME` label + path segments, clickable, `←` back button), [Upload] [New folder] [Refresh].
   - List rows: dir/file icon, name (ellipsis + `title`), size (humanized; dirs show `—`), modified (local tz via `Intl.DateTimeFormat`).
   - Selection (single) → context menu: Open (double-click behavior), Edit as text, Download…, Upload to here…, New folder…, Rename… (inline input row, same pattern as the tree), Delete… (`confirmDialog`, A8).
   - Double-click: dir → `List` (navigate); file with `TextLike` → `EditRemoteText` (row state "editing…", status line "Editing <path> — auto-saves when your editor stops writing for 3 s", context menu gains [Cancel edit]); other files → `DownloadThenSave` + toast with the saved path.
   - [Upload] → `PickLocalFiles(true)` (5b fallback: manual multi-path prompt input when `ErrSftpDialogUnsupported`) → `Upload`; footer progress line fed by `sftp:progress` — "Uploading db.sql — 1.2/3.4 MiB (1/2)" from per-tab transfer cache in the store; failures via `app:toast`.
   - [New folder] → inline row → `Mkdir`; [Rename] → inline row → `Rename`; [Delete] → confirm → `Remove` (non-empty dir → toast the backend `ErrDirNotEmpty` message).
   - Empty dir state ("This folder is empty"); when the tab leaves `ready` the panel unmounts via the visibility rule (no extra code); lock while open → destroyed with the rest of the UI (4a gate) — verify an in-flight edit is killed by the 5b cleanup.
2. Shortcut integration: verify Ctrl+Shift+E (4d) and the SFTP checkbox (4d settings dialog) toggle the panel visibility two-way (both write `sftpBrowserEnabled` via `SaveSettings`; store applies).
3. Integration tests — `internal/sftp/integration_test.go` (build tag `integration`; reuse the 3e helper `startSshd(t)`):
   - List on `$HOME` + a pre-created subtree (ordering asserted); `Mkdir` + remove-empty-dir; remove non-empty → `ErrDirNotEmpty`.
   - `Upload` 3 files incl. a generated ~12 MiB → progress monotonic, terminal done==total, remote content matches (download back + compare).
   - `Download` bytes-identical; `DownloadThenSave` fallback branch (temp path returned).
   - `Rename` a file; `EditRemoteText` with a fake editor = script file baked into the container (path configured via settings) that appends a line → assert remote content changed + temp cleaned; `EditRemoteText` on a 3 MiB file → `ErrTooLarge`; on a binary `.bin` → `ErrNotText`.
   - Tab closed mid-transfer → 5a hook fires → transfer fails cleanly (no hang, no leaked goroutines).
4. README (Development section): `tmp/` location + `edit-*.log` for debugging; text-editor command note (bare executable + args, last arg = file); SFTP note: "drag & drop uploads are out of scope for v1" (D4); keep the 4d seed/dev notes.
5. FINAL project QA (host-run binary, real sshd; annotate every item in the commit message):
   - E2E (master §12 #4): fresh config dir → create vault → unlock → folders → create session (password + key + jump-host variants) → connect → terminal works.
   - SFTP: enable via checkbox AND via Ctrl+Shift+E; connect → panel appears; browse `/etc` (read-only) + `$HOME`; mkdir; upload 3 files (one > 10 MiB, progress visible); download one (toast path); rename; delete file; delete empty dir OK; delete non-empty blocked with message.
   - Edit: text file in the container's `$HOME` with a real editor (`textEditorCommand=nano` or `xdg-open` fallback) → save → auto re-upload verified by remote mtime + content; cancel-edit path (fresh file → cancel → remote unchanged).
   - Lock mid-browse → clean unlock screen; NO `tmp/` leftovers after lock and after app exit (ls check); restart → stale sweep clean.
   - Code-review assertion: NO file bytes cross IPC — `Upload`/`Download` JS payloads are paths only (grep the binding usages).
   - Global gates from a clean container state (fresh `shelve-dev` rebuild): `make build`, `make test`, `make lint`, `make test-integration` — ALL green.
   - `rg` sweep: no hardcoded test credentials outside `docker/sshd` and testdata fixtures.

## Verification
- All four make gates green from a clean container.
- QA list items executed and annotated (incl. master §12 v1.1 items 1–4; §12 #5 as amended = `make build/test/lint` only; #6 README complete for the v1.1 scope).

## Exit criteria
Master §12 (v1.1) acceptance met: the end-to-end chain (folder → session → connect → terminal → SFTP browse/upload/download/edit → lock → unusable without password) passes against a real sshd; no temp leftovers; no bytes over IPC; roadmap complete — project is shippable as a Docker-built binary with docs; packaging remains available ad-hoc.
