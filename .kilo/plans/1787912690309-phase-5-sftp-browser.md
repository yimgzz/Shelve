# Phase 5 — SFTP Browser & Remote Text-File Editing

**Type:** Backend (small) + Frontend. **Prereq: Phase 4.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§1, §2 (D4, A5), §5 (SftpService, sftp:progress, failure modes), §6 (SFTP panel), §8.8** before starting.

## Goal
`SftpService` (Go) streaming all bytes server-side + the left-panel SFTP UI replacing the tree when the setting is on and a session is active; upload/download/mkdir/rename/remove; “Edit remote text file with system editor” round-trip.

## Tasks
1. `internal/sftp` (Go):
   - `New(client *ssh.Client)` via `github.com/pkg/sftp`; one SFTP client per active tab, created lazily on first `List`, closed with the tab (engine `Disconnect` also closes it — wire via engine hook).
   - Ops (all resolve `~` and `..`, resolve to absolute remote path; never expose remote FS to IPC): `List(tabID, path) -> [Entry{Name,IsDir,Size,ModTime}]` (dir entries first, then name-sorted, case-insensitive), `Mkdir`, `Rename`, `Remove` (file or empty dir; non-empty dir → error “directory not empty”).
   - `PickLocalFiles(multi)` → Wails native open-file dialog (verify v3 dialog API at pinned version; fallback: plain path input) → returns absolute local paths.
   - `Upload(tabID, []localPath, remoteDir)`: per file stream `io.Copy` (put with `sftp.File` writer), progress events `sftp:progress {transferID:"up", done,total}` every ≥ 256 KB or 250 ms; `Download(tabID, remotePath) -> localTempPath` into config dir `tmp/` (0600); `DownloadThenSave`: temp + native save dialog (v3 dialog API; fallback: return temp path and toast the path).
   - `EditRemoteText(tabID, remotePath)`: size probe (≤ 2 MiB), extension whitelist (txt, md, sh, yml, yaml, json, toml, ini, conf, cfg, env, js, ts, css, html, go, py, c, h, cpp, hpp) — finalized as the `TextLike` set in code, else error “not a text file / too large”. Flow: download to `tmp/<id>.<ext>` → `exec.Command` the configured `textEditorCommand` split via `strings.Fields`, stdin/out/err → log file in `tmp/edit-<id>.log`, detached goroutine; mtime poll 1 s; stable ≥ 3 s → re-upload (atomic: write `path.tmp.<id>` then `Rename`) → toast “Saved to <remote>” → cleanup temp. Editor exits with non-zero before any save → toast error, keep temp (user can retry). Editor killed → temp kept, toast.
   - `Cleanup()` on lock/shutdown: kill running editors (SIGTERM), delete `tmp/` contents (best effort), also invoked on app start (stale sweep).
   - Wire into engine: `Disconnect`/`Shutdown` callback closes sftp client + kills editors.
2. `internal/wailsvc/SftpService`: DTOs `SftpEntry`, transfer errors typed (`ErrDirNotEmpty`, ErrNotText, ErrTooLarge) — register the service; all methods no-op-safe when vault locked (typed error `ErrLocked`).
3. Frontend `components/sftp-panel.ts` + CSS:
   - Visibility rule (single source of truth in store.ts): `settings.sftpBrowserEnabled && activeTab && tab.state==='ready'` → panel, else tree; Ctrl+Shift+E toggles the setting (and settings dialog checkbox — two-way sync).
   - Header: breadcrumb (clickable segments: home → …), path display, buttons [Upload] [New folder] [Refresh]; footer: transfer line “Uploading db.sql — 1.2/3.4 MiB (n/2)” fed by `sftp:progress`.
   - List rows (icon, name with ellipsis + title, size, mtime localized to local tz); dir double-click → cd; file double-click → text-like (per backend classification returned in Entry `TextLike bool`) && size ≤ 2 MiB → `EditRemoteText` (row shows “editing…” state), else `DownloadThenSave` + toast with saved path; selection (single) for context menu: Open (same as dblclick), Edit as text, Download…, Upload to here…, New folder…, Rename…, Delete… (confirm).
   - Uploading: file picker → `Upload` with progress; drag&drop files onto the panel = NO in v1 (out of scope, note in README).
   - Empty dir state; “home = $HOME (remote)” label; navigation back-button in breadcrumb.
   - Lock while SFTP open → panel destroys with the rest of the UI (Phase 4 gate).
4. Editor UX detail: while editing, the row is locked (double-click no-op) and status line shows “Editing /path — auto-saves when your editor stops writing for 3 s”; Esc does nothing (editor is external). Provide [Cancel edit] in the context menu (kill editor, delete temp, no upload).
5. Tests:
   - Go: unit for `TextLike` classification, path resolution (the remote user may access anything their OS user can access — `..` traversal allowed, `~` expanded server-side; documented), edit-state machine with a fake editor binary (shell script that sleeps then writes), cleanup-on-shutdown.
   - Integration (extend Phase 3 suite, tag `integration`): list/mkdir/upload/download/rename/remove against sshd container; edit flow with `/bin/sh` as the fake editor (script appends a line) → assert remote file changed; non-empty dir delete error; > 2 MiB file → ErrTooLarge; binary file → ErrNotText.
6. `make lint` must stay green; README “Development” section gets the SFTP debug notes (config dir `tmp/` location for logs).

## Verification
- `make test` + `make test-integration` green (clean container).
- `make dev` manual checklist: enable checkbox in fresh session dir; connect; panel appears; browse into `/etc` (read-only) and `$HOME`; mkdir/upload 3 files (one > 10 MiB streaming progress visible)/download one to a picked path; rename; delete file; new folder delete (empty ok, non-empty blocked with message); edit a real config file (e.g. a test file in sshd container $HOME) with `xdg-open`/`nano` — save → auto re-upload verified by remote mtime+content; cancel-edit path; lock mid-browse → clean unlock screen; setting toggle via Ctrl+Shift+E and dialog both work; panel hidden when tab closed; no file transfer through JS memory (verify: `Upload` JS call payload = paths only, size-independent — code review assertion).

## Exit criteria
All D4 operations + editor round-trip pass integration + manual checks against the container sshd; `sftp:progress` smooth at ≥ 10 MB/s (no UI jank — spot check); §8.8 temp hygiene verified (no leftover temp after lock/exit tests).
