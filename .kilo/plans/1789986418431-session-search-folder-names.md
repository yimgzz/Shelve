# Session search: match folder names in addition to Name/Host/User

## Goal

The left-panel live search bar (master plan §2 A9) currently matches session
**Name, Host, User** only (case-insensitive substring; `Store.Search`,
`internal/store/store.go:849`). Extend it to also match **folder names** —
concretely, the slash-joined folder path containing the session (i.e. any
ancestor folder). Typing a folder name finds every session nested under it.
The folder path is already shown per result row as display context
(`SearchResultDTO.folderPath`, `internal/api/sessionservice.go:15`), so no
transport/DTO change is needed.

Clarification with the user: "catalog names" = **folder names** (the session
tree hierarchy). Named Credentials / saved JumpHosts are explicitly **out of
scope**.

## Decisions (resolved)

1. **Match scope:** a session matches q if q is a case-insensitive substring
   of its Name, Host, User, **or its full slash-joined folder path**
   (all ancestor folder names, as `Store.folderPathLocked` already joins
   them, `internal/store/store.go:877`). Matching the whole joined path
   (not just the immediate parent) is deliberate: it also supports
   cross-segment queries like `prod/db`. A session is returned at most once
   (single OR-check), result order stays ID-sorted (stable, unchanged).
2. **Implementation: on-demand, not indexed.** `folderPathLocked` is an
   O(depth) walk over `s.folders`/ParentID and always reflects current state,
   so there is nothing to keep in sync. The existing pre-lowercased index
   (`lowerSess`) already short-circuits sessions that match on Name/Host/
   User; the folder-path check runs only for the rest, so the 10 ms perf
   budget (300 nodes) is unaffected in the common case and the worst case
   (300 × short walk) is still trivial. Deliberately avoids new sync points in
   `MoveNode` (subtree moves), `RenameFolder`, `DeleteNode`, `Load`,
   `merge.go` (which rebuilds the index at `merge.go:239`).
3. **UI:** the result row already renders `host — folderPath`; additionally
   highlight the query match inside the displayed folder path (mirrors the
   existing name highlight), so a folder-name-only hit is visibly explained.
   Update the input placeholder to make the new scope discoverable.
4. **No changes to:** `SearchResultDTO` (golden surface test unaffected),
   preload API, events, model validation, vault/persistence, `internal/api`
   method signature.
5. **Docs:** update master plan §2 A9, §6 Search row, §10 workstream index,
   and AGENTS.md §7 Search bullet (project rule: new work extends the master
   plan; a change is done only with `make lint` + `make test` passing).

## Tasks

### 1. Backend — `internal/store/store.go`

In `Search` (line 849-873): keep the existing `q` trim/lower and the
`searchIdx` scan; extend the per-session predicate to:

- match = `strings.Contains(l.name, q) || strings.Contains(l.host, q) ||
  strings.Contains(l.user, q) || strings.Contains(strings.ToLower(s.folderPathLocked(sess.FolderID)), q)`
- Order the checks so the folder-path computation (the only one needing a
  walk) happens last; short-circuit on the first three.
- Update the `Search` doc comment (line 845-848) to state folder-path
  matching (A9 superset).

### 2. Backend — tests

`internal/store/store_test.go`:
- New `TestSearchMatchesFolderPath` (place next to `TestSearchIndexSync`,
  line 1074):
  - Folder `Production` with one session whose Name/Host/User contain no
    substring of the folder name → `Search("pro")` (case-insensitive) = 1 hit;
    a root-level session is unaffected (empty path never matches a
    non-empty q).
  - Nested folders `A/B`: a query matching the joined path (e.g. `"a/b"`)
    finds the session; a query matching only the middle segment finds it too.
  - `MoveNode` the session into another folder → old folder name no longer
    matches, new one does.
  - `RenameFolder` → old name stops matching, new name matches.
  - `DeleteNode` of a folder stops matches for its subtree's sessions
    (sessions themselves are deleted too — assert via a folder rename instead
    if simpler; the key assertions are move + rename above).
- Extend `TestStore300SessionsSearchBudget` (line 461) with a second
  measured query that matches **only via folder path** against the
  `GenerateFixture` tree (names/hosts/users do not contain it), e.g.
  `"Folder-05"` (folders are `Folder-01`…`Folder-10`, subfolders
  `Folder-NN/c`; sessions live in subfolders, so this is a folder-path-only
  hit). Same best-of-20 / 10 ms gate, logged.

`internal/api/api_test.go` (`TestSessionServiceSearch`, line 253):
- The test already creates a `Production` folder; add an assertion that
  `Search("rod")`-style folder-name fragment (that is not a substring of any
  Name/Host/User in the fixtures — pick e.g. a fragment of `Production` not
  appearing in name/host/user, verify against the three sessions first)
  returns the sessions inside `Production`. Keep existing assertions
  unchanged (they are Name/Host/User-scoped).

### 3. Frontend — `frontend/src/components/tree.ts`

- `renderResultList` (line 694-727): build the meta span as DOM instead of
  plain text — host as text node; when `r.folderPath` is non-empty append
  `" — "` then `highlight(r.folderPath, q)` (existing helper at line 650),
  so folder-path matches get the same `<mark>` treatment as name matches.
- `frontend/src/components/search.ts` (line 39): placeholder
  `"Search sessions…  Ctrl K"` → `"Search sessions & folders…  Ctrl K"`.

### 4. Docs (required by project closing rule)

- `.kilo/plans/1789467100000-master-plan.md`:
  - §2 A9 (line 101-102): "Search matches Name, Host, User (case-insensitive
    substring); folder path shown in results." → add that it also matches
    the session's folder path (any ancestor folder name, slash-joined).
  - §6 Search row (line 500-501): note the query also matches the folder
    path and the result view highlights the match in the path.
  - §10 workstream index (line 745+): add row
    `| Session search: folder-name matching | Done | §2 A9, §6 |` (mark
    `Done` in the same commit; flip to `Open` if split, and back once merged).
- `AGENTS.md` (line 373): Search bullet — add folder names to the matched
  fields, mirroring the A9 wording.

## Failure modes / edge cases

- Root-level session: `FolderID == ""` → `folderPathLocked` returns `""`;
  `strings.Contains("", q)` is false for any non-empty q (q is already
  trimmed, empty q returns nil early) — no false positives, no panic.
- Query containing `/`: matches across folder segments in the joined path —
  intended (documented in the §2 A9 wording).
- Case: path is lowercased per query in `Search` (consistent with A9).
- No duplicates, no order change: single OR predicate, same ID sort.
- No secret exposure: folder names are not credentials (A9 DTO contract
  unchanged).
- Locking: `folderPathLocked` requires `s.mu` — `Search` already holds
  `RLock` for the whole scan; do not add a second lock.

## Validation

1. `make test` (container, runs with `-race`): new/extended store + api tests
   green; existing search/budget/surface-golden tests unchanged and green.
2. `make lint`: `gofmt` + `go vet` + renderer `tsc --noEmit`.
3. `make test-integration`: **not required** (no SSH/SFTP/transport change).
4. Manual (optional, mirrors existing QA list): unlock a seeded vault
   (`make seed`), type a folder name → rows for all nested sessions appear
   with the path segment highlighted; move/rename the folder in the tree and
   re-run the query.

## Out of scope

- Matching Credential / saved-JumpHost names (user-confirmed out of scope).
- Searching by hostname of jump hops, Extra Args, or SFTP initial path.
- Grouping/filtering results by folder, or dedicated "search folders only"
  mode.
- Any change to the RPC surface, preload API, or persistence format.
