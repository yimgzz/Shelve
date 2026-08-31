# Phase 3a — "Extra Args" strict parser (`sshx/args`)

**Type:** Backend. **Prereq:** Phase 2 (done). **Sub-plan 1/5 of old Phase 3. Next: 3b.**

**Master plan:** `plans/1787912690309-master-plan.md` — read **§2 (D5), §4 (validation rules), §5 (services), §9 (unit tests)** before starting.

**Scope guard (important):** implement only the parser and its tests. NO engine code, NO `internal/sshengine` changes, NO wails changes, NO container/network code.

## Goal
A strict tokenizer + parser for the session "Extra Args" string (master §2 D5), fully unit-tested. The Phase 2 placeholder in `internal/sshx/extraargs.go` is replaced with real behavior via delegation (no `internal/wailsvc` changes needed).

## Tasks
1. New package `internal/sshx/args` (an empty directory already exists — fill it):
   - Types:
     - `Forward{Kind string /* "L" | "D" */, BindAddr string, LocalPort int, DstHost string, DstPort int}` — for `-D`: `DstHost`/`DstPort` empty; bind defaults to `127.0.0.1` in the normalized output.
     - `Options{ServerAliveInterval *int, ServerAliveCountMax *int, ConnectTimeout *int, StrictHostKeyChecking string}` (only values `no`/`ask` allowed for the latter; others → error).
     - `ProxyJump{User, Host string, Port int}` (`Port` 0 = 22).
     - `Parsed{Forwards []Forward, Options Options, ProxyJump *ProxyJump}`
   - `Parse(spec string) (*Parsed, error)`:
     - Tokenizer: shell-style — whitespace-separated, single and double quotes, no backslash escapes; unbalanced quote → error.
     - Supported (and ONLY these):
       - `-L [bind:]localPort:dstHost:dstPort` (dstHost = hostname/IPv4; IPv6 in specs → clear error, v1 limitation, document)
       - `-D [bind:]localPort`
       - `-o Key=Value` for exactly: `ServerAliveInterval`, `ServerAliveCountMax`, `ConnectTimeout`, `StrictHostKeyChecking`; unknown key → error; duplicate key → last wins (documented).
       - `ProxyJump=[user@]host[:port]`
     - Anything else (unknown flags like `-A`/`-R`/`-C`, bare words) → descriptive, user-facing errors: `"unsupported flag -A"`, `"invalid local port in -L spec: \"...\""`, `"unsupported -o key \"...\""`, `"unbalanced quote"`.
   - `Validate(spec string) error` — exported; runs `Parse` + sanity checks (port ranges 1–65535). This is what the UI binds to.
2. Replace `internal/sshx/extraargs.go`: keep `func ValidateExtraArgs(s string) error` but delegate to `args.Validate`; delete the `ErrExtraArgsNotWired` placeholder and its Phase 3 comment. `internal/wailsvc/sessionservice.go` already calls it — extend `wailsvc_test.go` with a couple of real parse cases through the service if not trivially covered.
3. Unit tests (`internal/sshx/args/args_test.go`, table-driven):
   - Accepted: empty string; whitespace-only; each supported form alone and combined; quoted specs (`"-L 127.0.0.1:8080:db:5432"`); `-L 8080:db:5432` (bind omitted); `-D 1080`; each `-o` key; `ProxyJump=bob@jump`, `ProxyJump=jump:2222`, `ProxyJump=jump`; duplicate `-o` last-wins.
   - Rejected: unknown flags (`-A`, `-R`, `-C`, bare word); malformed `-L` (missing part, non-numeric port, port 0, port 70000); malformed `-D`; unknown `-o` key; `-o` without `=`; unbalanced quote (single and double); empty `ProxyJump=`.
   - Assert exact error text for representative cases — these messages surface verbatim in the session editor's inline validation.

## Verification
- `make test` (race) green inside the container.
- `make lint` (gofmt/vet/golangci-lint) clean.

## Exit criteria
`args` package complete: 100% of D5 accepted forms parse into the normalized `Parsed`; 100% of the table rejection cases rejected with actionable messages; `SessionService.ValidateExtraArgs` returns real parser errors (verified headlessly via wailsvc tests).
