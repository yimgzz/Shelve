# SFTP browser: native file dialogs for Upload / Download

## Goal

- **Upload** (toolbar `[Upload]` and context menu `Upload to here…`) must open a
  native file browser where **one or more files** can be selected, then upload
  them into the current remote directory.
- **Download** (context menu `Download…`) must open a native directory browser
  where the user picks a **destination directory**; the remote file is streamed
  directly there as `<destDir>/<remoteBasename>`.
- Still no file bytes over the loopback transport — only selected **paths**
  cross the renderer↔backend surface (master plan A5).

## Current behavior (why this is a change)

- `internal/sftp/transfer.go:365` `PickLocalFiles` is a stub returning
  `ErrSftpDialogUnsupported`; `sftp-panel.ts:492` catches it and shows a manual
  multi-path `textarea` (`promptLocalPaths`, `sftp-panel.ts:520`).
- `internal/sftp/transfer.go:353` `DownloadThenSave` downloads to a hidden
  `tmp/<ULID><ext>` (0600) and only toasts the path; the user never chooses a
  destination.
- Electron already has a reviewed native-dialog surface (`electron/main.ts:774`
  onward) with only `openFile`; there is no multi-file or `openDirectory`
  picker. This plan supersedes the E3 note that deferred native SFTP dialogs
  (`plans/1789467400000-electron-e3-frontend-transport-native.md:129`).

## Locked decisions

1. **Pickers live in Electron main**, exposed through the preload API
   (`window.shelve`), matching `pickFile`/`pickSaveFile`/`pickOpenFile`. The Go
   backend has no access to Electron dialogs, so `SftpService.PickLocalFiles` is
   removed rather than reimplemented.
2. **Upload** picks paths in the renderer, then calls the unchanged
   `SftpService.Upload(tabID, paths, remoteDir)`.
3. **Download** adds `SftpService.DownloadTo(tabID, remotePath, destDir,
   overwrite)`; the backend streams directly into `destDir`.
4. **Overwrite:** if `<destDir>/<basename>` exists, the backend returns a typed
   error; the renderer shows a confirm dialog ("Replace existing file?") and
   retries with `overwrite=true`. Directories are never overwritten.
5. **Download stays single-file**; the context-menu item is disabled for
   directory rows (the backend also still rejects directories).
6. The manual upload-path `textarea` fallback and `ErrSftpDialogUnsupported` are
   deleted.

## Tasks (ordered)

### 1. Native dialog surface — Electron main + preload

- `electron/main.ts`: after the `dialog:pickOpenFile` handler (`:827`), add two
  `ipcMain.handle` handlers, both gated by `trusted(event)` and returning an
  empty value when `!win || win.isDestroyed()`:
  - `dialog:pickFiles` → `dialog.showOpenDialog(win, { title: "Select files to
    upload", properties: ["openFile", "multiSelections"] })` → return
    `result.filePaths` (`[]` on cancel). No filters.
  - `dialog:pickDirectory` → `dialog.showOpenDialog(win, { title: "Select
    download destination", properties: ["openDirectory", "createDirectory"] })
    → return `result.filePaths[0] ?? ""` (`""` on cancel).
- `electron/preload.ts`: after `pickOpenFile` (`:40`) add
  - `pickFiles: (): Promise<string[]> => ipcRenderer.invoke("dialog:pickFiles")`
  - `pickDirectory: (): Promise<string> => ipcRenderer.invoke("dialog:pickDirectory")`
  - Extend the file header comment to mention multi-file + directory pickers.
- `frontend/src/rpc/types.ts`: extend `ShelveNative` (`:223`) with
  `pickFiles(): Promise<string[]>` and `pickDirectory(): Promise<string>`,
  documented as resolving `[]`/`""` on cancel. No new type in
  `frontend/src/rpc/ipc.ts` is required (plain array/string payloads).

### 2. Backend transfer manager — `internal/sftp/transfer.go`

- Remove `ErrSftpDialogUnsupported` (`:52`) and `(*Manager).PickLocalFiles`
  (`:365`).
- Add sentinels `var ErrDestExists = errors.New("sftp: destination exists")`
  and `var ErrDestIsDir = errors.New("sftp: destination is a directory")`.
- Extend `transferJob` (`:64`) with `destDir string` and `overwrite bool`.
- `transferWorker` (`:195`): dispatch `destDir != ""` to a new
  `doDownloadTo`, otherwise keep `doDownload` (tmp primitive).
- Add `func (m *Manager) DownloadTo(tabID, remotePath, destDir string,
  overwrite bool) (string, error)` mirroring `Upload`'s enqueue/await shape.
- Add `doDownloadTo(tabID, remotePath, destDir string, overwrite bool) (string,
  error)`:
  1. `ClientFor`, `resolve`, `Stat`; reject directories (reuse `doDownload`
     checks).
  2. `os.Stat(destDir)` must exist and be a directory.
  3. `target := filepath.Join(destDir, path.Base(abs))`.
4. If `os.Stat(target)` succeeds: if it is a directory, return
   `fmt.Errorf("%w: %s", ErrDestIsDir, target)`; else if `!overwrite`, return
   `fmt.Errorf("%w: %s", ErrDestExists, target)`. Any other `Stat` error that
   is not `os.IsNotExist` is returned.
  5. Write to a sibling temp file `filepath.Join(destDir, "."+path.Base(abs)+
     ".shelve-"+newULID())` (0600), streaming with `streamCopy` and the existing
     throttled `progressSink` (direction `"down"`, `fileName` = basename, total
     = remote size).
  6. On success commit atomically: `os.Rename(tmp, target)` when `overwrite`,
     otherwise a no-replace `os.Link(tmp, target)` + `os.Remove(tmp)` so a file
     created during the transfer yields `ErrDestExists` instead of being
     silently replaced; a `Close` error gates the commit. On any failure
     `os.Remove(tmp)` and return the wrapped error, emitting the terminal error
     progress event + `app:toast` exactly like `doDownload` (the `ErrDestExists`
     race path emits only the terminal progress event, letting the renderer
     confirm).
  7. On success emit `app:toast("info", "Downloaded to "+target)` and return
     `target`.
- Keep `(*Manager).Download` (tmp primitive) and its tests; `DownloadThenSave`
  is removed (`:349`).
- Remove the now-unused `path/filepath` usage only if it becomes unused — it is
  still used, so no import change.

### 3. API service + bridge registration

- `internal/api/sftp_service.go`: delete `PickLocalFiles` (`:85`) and
  `DownloadThenSave` (`:111`); add
  `DownloadTo(tabID, remotePath, destDir string, overwrite bool) (string, error)`
  gated by `requireUnlocked()` and delegating to `s.mgr.DownloadTo`. Keep
  `Upload` and `Download` unchanged; drop the stale fallback comments and
  update the package/method comments to describe the native-dialog flow.
- `internal/bridge/server.go:51` `blockingMethods`: remove
  `"SftpService.DownloadThenSave"`, add `"SftpService.DownloadTo"`; keep
  `SftpService.Upload` and `SftpService.Download`.

### 4. Frontend RPC proxy + SFTP panel

- `frontend/src/rpc/index.ts` `SftpServiceApi` (`:87`): remove `PickLocalFiles`
  and `DownloadThenSave`; add
  `DownloadTo(tabID: string, remotePath: string, destDir: string, overwrite: boolean): Promise<string>`.
  Keep `Download`.
- `frontend/src/components/sftp-panel.ts`:
  - `doUpload()` (`:492`): replace the `SftpService.PickLocalFiles` + fallback
    logic with `const paths = await window.shelve.pickFiles();` — return when
    empty, otherwise `await SftpService.Upload(tabID, paths, curPath)`, then
    `void loadList()` and the existing "Upload complete" toast. Wrap the
    picker+upload in `try/catch` (toast the error), as today.
  - Delete `promptLocalPaths()` (`:520`) and the now-unused `openDialog` import
    (`:27`).
  - `downloadSave(entry)` (`:462`): call `window.shelve.pickDirectory()`; return
    when `""`. Call `SftpService.DownloadTo(tabID, joinRemote(curPath,
    entry.name), destDir, false)`. On rejection whose message includes the
    stable substring `"sftp: destination exists"`, show
    `confirmDialog({ title: "Replace existing file?", message: "<name> already
    exists in the selected folder. Replace it?", confirmLabel: "Replace",
    danger: true })` and, if confirmed, retry with `overwrite = true`. Any other
    error toasts. Success feedback comes from the backend `app:toast`.
  - `openPanelContext` (`:732`): set `disabled: entry.isDir` on the
    `Download…` item.
  - Update the file header comment block (Upload/Download descriptions) to
    reflect native dialogs.

### 5. Docs (implementation sub-task)

- `AGENTS.md` §5 service surface: reflect that `SftpService` no longer has
  `PickLocalFiles`/`DownloadThenSave` but has `DownloadTo`, and that
  `window.shelve` has `pickFiles`/`pickDirectory`.
- `plans/1789467100000-master-plan.md` D6 (`:77`) and §5 dialog-fallback bullets
  (`:367`): replace the "manual multi-path prompt / temp-file fallback" text
  with the native multi-file + directory-picker behavior and the overwrite
  confirm.
- `plans/1789467400000-electron-e3-frontend-transport-native.md:129`: revise the
  "Do not add native dialogs" note to point at this plan as the follow-up that
  now enables them.
- If a new `plans/<ts>-*.md` phase entry is expected by the project's planning
  system, add a short phase file referencing this plan; otherwise keep the
  changes in the master plan.

## Edge cases / failure modes

- **Cancel** at either dialog → `[]` / `""` → no RPC call, no toast.
- **Destination not writable / missing / not a dir** → `os.Stat`/temp-create
  error surfaces as a toast; no partial file left (temp removed).
- **Existing target is a directory** → always `ErrDestIsDir`, even with
  `overwrite=true` (rename over a dir must not be attempted); the renderer
  toasts it instead of offering the Replace confirm.
- **Atomicity** → writes go to a hidden sibling temp then commit atomically
  (`os.Rename` when overwriting, no-replace `os.Link` otherwise); a failed
  transfer never truncates a pre-existing destination file, and a file created
  during the transfer is not silently replaced.
- **Multiple upload files** → unchanged backend `doUpload` batch semantics
  (first failure stops the batch with a toast).
- **No `window.shelve`** (defensive) → `doUpload`/`downloadSave` catch and toast.
- **Concurrency** → `DownloadTo` rides the existing per-tab FIFO transfer worker;
  `blockingMethods` keeps the `/rpc` read loop responsive.
- **Permissions** → downloaded files are created 0600 (matching the existing
  transfer primitive); replacing an existing file via temp+rename resets its
  mode to 0600. Accepted for this change.

## Validation

- `make lint` (gofmt + `go vet` + renderer/Electron `tsc --noEmit`) — confirms
  the new preload/`ShelveNative`/`SftpServiceApi` types line up.
- `make test` — updated unit tests:
  - `internal/bridge/surface_test.go:36`: new `SftpService` list
    `{IsActive, List, Mkdir, Rename, Remove, Upload, Download, DownloadTo,
    EditRemoteText, CancelEdit}`.
  - `internal/api/sftp_service_test.go`: drop `PickLocalFiles`/
    `DownloadThenSave` cases; assert `DownloadTo` is `ErrLocked` while locked and
    `ErrNoProvider` when no tab provider exists (`:80` region).
  - `internal/sftp/transfer_test.go`: remove
    `TestPickLocalFilesUnsupported` (`:267`) and
    `TestDownloadThenSaveFallback` (`:232`); add `DownloadTo` success (file lands
    in `destDir`), collision (`ErrDestExists` with `overwrite=false`), and
    overwrite (`overwrite=true` replaces) cases. Keep the tmp `Download` test
    (`:200`).
- `make test-integration` (touches SSH/SFTP networking):
  `internal/sftp/integration_test.go:272` — replace the `DownloadThenSave`
  block with `DownloadTo(tab, remote, dir, true)` and assert the file exists at
  `dir/<basename>`.
- Manual (E7 matrix, Linux): `make build && make run`
  - `[Upload]` and `Upload to here…` open a multi-select picker; selecting 1 and
    N files uploads all; cancel does nothing.
  - `Download…` on a file opens a directory picker; the file appears in the
    chosen directory; re-downloading the same file prompts to replace, and both
    Replace and Cancel behave correctly.
  - `Download…` is greyed out on directory rows.
  - Progress footer still shows the `down` transfer.

## Out of scope

- Recursive directory upload/download (files only, as requested).
- Renaming on download (the picker selects a directory; the name is the remote
  basename).
- Changing `Upload to here…` to target the right-clicked directory row instead
  of the current directory (`curPath` semantics unchanged).
- Drag-and-drop upload, preserving remote file mode/mtime on download, and
  resumable transfers.
