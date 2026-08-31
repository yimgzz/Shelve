# Phase 5b — SFTP transfers, remote text-file editing, cleanup

**Type:** Backend. **Prereq:** 5a. **Sub-plan 2/3 of old Phase 5. Next: 5c.**

**Master plan:** `plans/1787912690309-master-plan.md` — read **§2 (D4, A5), §4 (`tmp/`), §5 (SftpService, `sftp:progress`), §6 (SFTP panel — backend implications), §8 (items 7–9), §9 (unit tests)** before starting.

**Scope guard (important):** transfers + edit flow + finishing the service. Frontend = none (5c). Container-based tests = 5c.

## Goal
Streaming upload/download with progress events (NO bytes cross IPC — A5), the "edit remote text file with the system editor" state machine, temp-file hygiene on lock/exit/start, and all remaining `SftpService` methods made real.

## Tasks
1. Transfers — `internal/sftp/transfer.go`:
   - `Upload(tabID, localPaths []string, remoteDir string)`: one in-flight transfer per tab (FIFO queue for the rest); per file: local `os.Open` → `client.Creat(Join(remoteDir, base))` → `io.Copy` through a progress reader; emit `sftp:progress {transferID: <ULID>, tabID, direction:"up", fileName, doneBytes, totalBytes, error?}` every ≥ 256 KB OR ≥ 250 ms, plus a final event (done == total; on failure: non-empty `error` field — documented extension of the master payload) + `app:toast`.
   - On file failure: stop the remaining queue, toast the first error.
   - `Download(tabID, remotePath) (string, error)` → `tmp/<ULID><ext>` (0600), streamed with `direction:"down"` progress.
   - `DownloadThenSave(tabID, remotePath) (string, error)`: download to temp, then — IF a native save dialog exists in the pinned Wails v3 runtime (verify once, record the decision): prefill suggested name = remote basename, user picks folder → copy temp there → return final path; on cancel → keep temp, return it + `app:toast` with the path (documented fallback). If no dialog API: return temp path + toast (same fallback).
   - `PickLocalFiles(multi bool) ([]string, error)` — native open-file dialog (same verification); unsupported fallback: typed `ErrSftpDialogUnsupported` — the 5c frontend then shows a manual multi-path prompt input. Record which path is used in this sub-plan's commit message.
   - No size cap (master: streams; 10 MiB+ handled by progress, QA in 5c).
2. Remote text editing — `internal/sftp/edit.go`:
   - `EditRemoteText(tabID, remotePath)`: probe `stat` → `ErrTooLarge` (> 2 MiB) / `ErrNotText` (not `TextLike`); download to `tmp/edit-<ULID>.<ext>` (0600).
   - Launch editor: `strings.Fields(settings.TextEditorCommand)` + `[tempPath]`; `exec.Command`; `SysProcAttr{Setpgid: true}` (own process group → clean cancel); stdout/err → `tmp/edit-<ULID>.log` (0600); `cmd.Start()`. Track `{cmd, pgid, tempPath, remotePath, state, lastMtime}`; ONE edit per tab — a second attempt → typed `ErrAlreadyEditing`.
   - Watch goroutine (1 s poll): mtime stable ≥ 3 s → SAVE: upload to `remotePath + ".tmp.<ULID>"` (same remote dir) → `client.Rename` over the original (atomic) → toast `"Saved to <remote>"` → delete temp. After ONE successful save the watcher stops (documented; user re-enters Edit to continue).
   - Editor exit (wait goroutine): non-zero with no save yet → toast error, KEEP temp (user can retry); zero/killed → keep temp + toast (killed case).
   - `CancelEdit(tabID)`: SIGTERM the process group (escalate SIGKILL after 3 s), delete temp + log, NO upload, reset state.
   - `Cleanup()`: SIGTERM all live editor groups; delete `tmp/*` (best effort); idempotent. Call sites in `internal/app`: (1) Lock flow — same place the engine Shutdown is called (3c); (2) `App.Shutdown`; (3) app start — stale sweep: delete ALL `tmp/*` on boot (single-instance app, master A7 → safe).
3. `internal/wailsvc`: replace the 5a stubs with real methods — `PickLocalFiles`, `Upload`, `Download`, `DownloadThenSave`, `EditRemoteText`, `CancelEdit`; progress payload struct (with the `error` extension) defined in `internal/sftp`; all locked-vault → `vault.ErrLocked`.
4. Unit tests:
   - Transfers (mem server + disk fixtures + capturing emitter): 5 MiB upload → progress events monotonic, terminal event done==total; two-file upload sequential; missing local file → typed error + `app:toast`, queue stops (documented).
   - Edit state machine with a FAKE editor = shell script in `t.TempDir()` (e.g. `sleep 1; echo line >> "$1"; exit 0`): assert remote content changed (read back through the mem sftp), temp cleaned, `ErrAlreadyEditing` guard, cancel path (script `sleep 30` → cancel → no upload, group dead, temp gone), non-zero exit before save → error toast + temp kept, `ErrTooLarge`/`ErrNotText` via probes.
   - `Cleanup` on shutdown kills editors and empties `tmp/`.
5. Lint hygiene: `exec` handling reviewed — the configured command is run verbatim + the temp path appended last (documented in method docs); `errcheck`/`gosec` findings on `exec.Command` either fixed or justified in code comments.

## Verification
- `make test` (race) green — the full suite above; `make lint` clean; `make build` OK.

## Exit criteria
The D4 Go surface is complete: streaming upload/download with progress, atomic edit round-trip with full editor lifecycle + cancel, temp hygiene on lock/shutdown/start — all unit-tested. 5c (panel UI + container integration) is the last leg of Phase 5.
