# Phase 5a — SFTP core operations (per-tab client, browse ops)

**Type:** Backend. **Prereq:** Phase 4 done (4d). **Sub-plan 1/3 of old Phase 5. Next: 5b.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (D4, A5), §4 (`tmp/` file), §5 (SftpService, failure modes), §8 (items 7–8), §9 (unit tests)** before starting.

**Scope guard (important):** browse operations + infrastructure only. Upload/Download/edit = 5b. Frontend = none (5c).

## Goal
`internal/sftp.Manager`: one lazily-created SFTP client per active tab, remote path resolution, `List/Mkdir/Rename/Remove` with `TextLike` classification, engine lifecycle hook (client closed whenever the tab dies), and full unit tests against pkg/sftp's in-memory server — no Docker needed.

## Tasks
1. Small engine extensions (`internal/sshengine`, keep its tests green):
   - `Manager.SSHClient(tabID string) (*ssh.Client, error)` — exposes the FINAL-hop client (SFTP needs it); typed errors for unknown/dead tabs.
   - `Manager.OnTabClosed(f func(tabID string))` — nil-safe hook invoked after the client is torn down (Disconnect / remote exit / Shutdown). The SFTP manager registers its closer here; the engine stays SFTP-agnostic.
2. Dependency: add + pin `github.com/pkg/sftp` in `go.mod` inside the container (master §3: maintenance-mode but standard, pin it).
3. `internal/sftp/manager.go`:
   - `type TabProvider interface { SSHClient(tabID string) (*ssh.Client, error) }` (defined in the sftp package — the engine satisfies it structurally; sftp must not import the engine).
   - `type Manager struct{ mu sync.Mutex; clients map[string]*sftp.Client; homes map[string]string; tmpDir string; emit emit-like }` — `New(tmpDir string, emit Emitter)`; `Attach(p TabProvider)`; register `p`'s `OnTabClosed` closer in the app wiring (task 5).
   - `ClientFor(tabID) (*sftp.Client, error)`: lazy `sftp.NewClient(sshClient)`; cache; remote home captured via `Getwd()` on first use (for `~`).
   - `resolve(tabID, userPath string) (string, error)`: `""` and `~` → remote home; leading `~` honored only at position 0; otherwise `path.Clean` (POSIX, remote semantics); `..` traversal ALLOWED (the remote OS user's permissions govern — master D4 note; document). The frontend only ever receives entries through these calls (A5).
   - `List(tabID, path) ([]Entry, error)`: `Entry{Name string; IsDir bool; Size int64; ModTime time.Time; TextLike bool}`; dirs first, then case-insensitive name sort; skip self/parent.
   - `Mkdir(tabID, path)`, `Rename(tabID, from, to)` (server semantics: overwriting an existing target depends on the remote — document), `Remove(tabID, path)`: file or EMPTY dir; non-empty → map to typed `ErrDirNotEmpty`.
   - `TextLike(name string, size int64) bool`: extension whitelist `{txt, md, sh, yml, yaml, json, toml, ini, conf, cfg, env, js, ts, css, html, go, py, c, h, cpp, hpp}` (case-insensitive) AND `0 ≤ size ≤ 2 MiB`; no extension → false.
   - `CloseAll()` (idempotent) — closes every cached client.
4. `internal/wailsvc/sftp_service.go` (`SftpService`, registered in `main.go`):
   - Real in THIS phase: `IsActive(tabID string) bool` (tab ready + client available), `List(tabID, path string) ([]SftpEntryDTO, error)`, `Mkdir`, `Rename`, `Remove`.
   - Stubs with typed `ErrSftpNotImplemented` (replaced in 5b): `PickLocalFiles`, `Upload`, `Download`, `DownloadThenSave`, `EditRemoteText`, `CancelEdit`.
   - Every method: vault locked → `vault.ErrLocked` (service holds the vault reference); unknown tab → typed error; DTOs in `dto.go`.
   - `tmp/` dir: under the config dir, created 0700, files 0600 (master §4, §8.8) — reuse the Phase 1 `internal/config` file helpers.
5. `internal/app` wiring: construct the manager, `Attach(engine)`, register `engine.OnTabClosed(sftpMgr.HandleTabClosed)`.
6. Unit tests (pkg/sftp in-memory server — follow the pattern from `pkg/sftp`'s own test suite and adapt to the pinned API: mem server on a `127.0.0.1:0` listener, dial a real `ssh.Client` to it, then `sftp.NewClient`):
   - List sorting (dirs first, case-insensitive) + `TextLike` flags; path resolution (`""`, `~`, `~/a`, `a/../b`, absolute); Mkdir + nested-parent failure; Rename file/dir; Remove file / empty dir / non-empty → `ErrDirNotEmpty`.
   - Tab-close hook: after `OnTabClosed` fires the client is closed (next `List` → typed error); `CloseAll` idempotent.
   - `TextLike` boundary tests (2 MiB exactly, + 1 byte).

## Verification
- `make test` (race) green (new package + engine tests still green); `make lint` clean.
- `make build` succeeds; `frontend/bindings/` includes `SftpService` (content check only).

## Exit criteria
Browse ops complete and unit-tested against the mem server; engine hooks in place and SFTP-agnostic; `SftpService` registered with the real browse methods; ready for 5b.
