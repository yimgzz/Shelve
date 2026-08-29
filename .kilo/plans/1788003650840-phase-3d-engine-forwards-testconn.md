# Phase 3d — Port forwards, TestConnection, in-process test rig

**Type:** Backend. **Prereq:** 3c. **Sub-plan 4/5 of old Phase 3. Next: 3e.**

**Master plan:** `.kilo/plans/1787912690309-master-plan.md` — read **§2 (D5, A6), §5 (`ssh:forward` event, failure modes), §9 (unit tests)** before starting.

**Scope guard (important):** engine features + in-process tests. No containers (3e owns those), no frontend. Wails changes: only wiring `SessionService.TestConnection` to the real engine.

## Goal
Local port forwards (`-L` with full dial-through, `-D` as minimal SOCKS5 connect-only), `ssh:forward` lifecycle events, a real `TestConnection`, and an in-process sshd test rig proving connect/prompt/lifecycle end-to-end without Docker.

## Tasks
1. Forwards — `internal/sshengine/forward.go`:
   - After shell-ready, for each `Parsed.Forwards` of the tab's session:
     - `-L`: `client.Listen("tcp", bind:localPort)` (bind defaults `127.0.0.1`); accept loop: each accepted conn is already a bidirectional pipe to `dst` (x/crypto/ssh `Client.Listen` opens a `direct-tcpip` channel per connection — just keep accepts open and close on tab teardown); emit `ssh:forward {tabID, spec, state:"listening", localAddr}` on successful bind.
     - `-D`: same listener; implement a MINIMAL SOCKS5 proxy (connect verb only, no auth): greeting `05 01` → expect `05 00`, request `05 01 00 atyp …` → open `client.Dial("tcp", target)` → bidirectional `io.Copy` pairs. Budget ~150 lines; no UDP/relay/fail verbs (documented).
     - Bind failure → emit `ssh:forward {state:"failed", error}` + `app:toast` (level `error` with the forward spec); connection PROCEEDS (non-fatal, master §5).
     - Tab teardown (Disconnect/exit/Shutdown): close listeners + accepted conns; emit `ssh:forward {state:"closed"}` per forward.
2. `TestConnection`:
   - `Manager.TestConnection(sess *model.Session) error`: full chain dial (same auth / host-key / prompt paths, reusing the connID-keyed prompt machinery — ephemeral ULID connID, no tab record), NO pty, NO forwards, close immediately; nil on success.
   - Wire `wailsvc.SessionService.TestConnection(s SessionDTO) error` (replaces the `ErrEngineNotWired` placeholder): DTO → `model.Session` (add a small `dtoToModel` in wailsvc; run `model.Validate`); `SessionService` constructor gains the engine reference.
3. In-process sshd rig — `internal/sshengine/testutil_test.go` (package `sshengine`, shared with 3e's integration suite):
   - `NewTestSSHServer(t, opts)`: in-process `ssh.Server` on a `127.0.0.1:0` listener; generated ed25519 host key; password callback (configurable user/pass); pubkey callback (configurable keys, incl. a passphrase-protected key variant); channel handlers: `pty-req` → accept (remember window; `window-change-req` → accept), `shell` → line-echo loop: each line `X` → reply `echo: X`; `exit` → `session.Exit(0)`.
   - `NewCapturingEmitter() (*Events, ...)` — ordered event log with per-name waiters (timeout-based, no sleeps).
4. In-process tests (fake emitter + rig):
   - Password connect: `connecting` → `ready` (order asserted); PTY echo: `Write("echo hi\n")` → data containing `echo: hi`; `Resize` → no error.
   - Remote `exit` → `terminal:exit` → `closed`; `Disconnect` → `closed` event; second `Disconnect` → `ErrUnknownTab`; `Write` after close → typed error.
   - Unknown host key: fresh known_hosts → `vault:hostkey-prompt` emitted, Connect suspended → `ApproveHostKey` → `ready`; known_hosts file now contains the entry; `Reconnect` → NO new prompt (cached).
   - Reject path: `RejectHostKey` → `terminal:status error` with a clear message.
   - Key-passphrase: pubkey requiring passphrase → `vault:key-prompt` → correct `SubmitKeyPassphrase` → `ready`; wrong → `error` (typed message); timeout path with `PromptTimeout` set to ~500 ms → `error` (timed out).
   - Forwards: `-L` to a plain in-test TCP listener (accept + close) → dial `127.0.0.1:port` → connection works until the listener closes; bind failure (pre-bind same port) → `ssh:forward failed` + `ready` still reached; `-D`: SOCKS5 client handshake + CONNECT through to the in-test listener; forward teardown emits `closed` on Disconnect.
   - `TestConnection`: success → nil and no tab record; auth failure → error naming the hop; unknown host key + approve → nil.
   - `Shutdown` with 3 live conns → everything closed, goroutine count stable, `-race` clean; second `Shutdown` no-op.
   - Perf smoke (in-process, no containers): 300-session `model` fixture pointed at the rig, 3 concurrent + 10 sequential `Connect` — log timings, no hard assert beyond < 2 s each (localhost).
5. Hop error attribution (3c's format) asserted in the auth-failure tests: wrong password → message contains the hop index/label.

## Verification
- `make test` (race) green — the full in-process suite above.
- `make lint` clean; `make build` OK.

## Exit criteria
Forwards (`-L` full dial-through, `-D` SOCKS5 connect) + `ssh:forward` lifecycle proven in-process; `SessionService.TestConnection` bound (placeholder gone); engine lifecycle/leak gate passed in-process. 3e (real sshd containers) is the final gate of the SSH engine.
