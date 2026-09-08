# Plan P009 — Bastion-style jump hosts (username-embedded target + keyboard-interactive auth)

**Status: FINAL — implementation-ready.** Branch selected by Phase A diagnostics
(`1-bastion.log`, `2-bastion.log`): this bastion is a **target-embedding,
channel-relay bastion**. The client supplies the target via the SSH **username**
(`user@target`); auth is **keyboard-interactive only, multi-round**; after auth
the bastion transparently relays the session channel (pty/shell) to the target.
`direct-tcpip` / ProxyJump is NOT used — that is why the current chain
(`dialHopVia`, direct-tcpip) fails at hop 1 with the bastion's own
`DISCONNECT(11, "Bastion can't connect to target server")`.

Bastion software name is unknown (vendor PAM bastion; LDAP at corp.tele2.ru).
No changes to the bastion side are assumed.

Reads: master plan §2 D5, §4 (model/validation), §5 (sshengine + event
contract), §6 (UI), §8 (security); `P006-jump-host-manager.md` (saved jump
hosts parity); phase 3c engine (prompt slots, A1/A2 flows).

---

## 1. Evidence (Phase A, verified against logs + x/crypto v0.53.0 source)

- `ssh -vv user@bastion` (no target): disconnect **right after
  `service_accept: ssh-userauth`, before any auth**: `reason 11: Bastion can't
  connect to target server` → a client-supplied target is mandatory.
- `ssh -vv user@target@bastion` (admin-documented): works end to end.
  Username = `supp.bercut228@10.246.220.151`; server offers
  **keyboard-interactive only**; kbdint runs **two rounds** (round 1 LDAP,
  "partial success"; round 2 second `Password:` — target-side relay auth or
  2FA); then a normal pty+shell session which is already the **target's**
  shell (`Last login` banner from the target).
- x/crypto v0.53.0 (pinned): client `ssh.KeyboardInteractive(challenge)` —
  `KeyboardInteractiveChallenge func(name, instruction string, questions
  []string, echos []bool) ([]string, error)` — is invoked **per
  INFO_REQUEST round** inside the method attempt; a final zero-question round
  (RFC 4256 §3.3) must be answered with empty answers. Server-side
  `KeyboardInteractiveCallback` exists in the new `ssh.Server` API (used by
  unit-test rig only).

## 2. Decisions (resolved)

1. **Bastion is a per-jump-host mode** (checkbox on the jump-host row, plus
   P006 saved-jump-host parity). Semantics: that hop is the **last SSH
   handshake** of the chain; its effective username is
   `<JumpHost.User>@<Session.Host>`; the session's Host/Port is the **target
   reached through the bastion** (no second hop, no duplicate target data).
   The pty session runs directly on the bastion connection (the relay makes it
   the target shell).
2. **Target port = 22 only in v1** (admin format `user@host` shows no port;
   `:port` support unknown — open question Q1). IPv6 targets rejected in
   bastion mode (unparseable inside a username).
3. **keyboard-interactive support, bastion-scoped in v1**: the bastion hop
   offers `[publickey (if AuthKey)] + [keyboard-interactive]` (NOT plain
   `password`). Each kbdint round renders a prompt modal; the user submits
   answers; Cancel aborts the dial.
4. **Prefill from vault (user-approved exception to §8)**: when the bastion
   hop's `Auth.Type == AuthPassword`, the stored password **prefills every
   password-style question (echo=false) of that connection's kbdint rounds**
   in the modal (user can edit before Enter; round 2 e.g. may need a different
   answer — the user overwrites). The prefill value crosses IPC **only**
   inside the `vault:kbdint-prompt` payload; it is never persisted, never
   logged, never stored in frontend state beyond the dialog's lifetime. This
   is the single documented exception to "no plaintext credentials over IPC"
   (AGENTS.md §5 + master plan §8 updated accordingly). kbdint answers are
   **never** cached in memory (no A2-style cache) and never stored.
5. Session (target) card in bastion mode: `Session.Auth` becomes **optional**
   (empty allowed; if present it is unused at connect time — target auth
   happens inside the bastion relay). `Session.User` stays required (display
   only; target login is the bastion's login in this format — documented).
6. **SFTP and P004 monitor** ride the bastion connection (subsystem/exec
   channels) and depend on the bastion relaying non-pty channels to the target.
   No code changes; verified in the manual QA checklist; if the bastion
   refuses them, the existing graceful failure (error toast / monitor error)
   is acceptable.

## 3. Model (`internal/model/model.go`, `validate.go`)

- `JumpHost` gains `Bastion bool \`json:"bastion,omitempty"\``.
  `SavedJumpHost` (P006) gains the identical field.
- New validation (joined into existing `validateFields` + `Session.validateFields`,
  user-facing messages, same style as the `args` parser):
  - at most one bastion hop in `Session.JumpHosts`;
  - the bastion hop must be the **last** jump host in the list;
  - if any bastion hop: Extra Args must NOT contain `ProxyJump=`;
  - if any bastion hop: `Session.Port == 22` (message: "bastion target requires port 22 (v1 limitation)");
  - if any bastion hop: `Session.Host` must be hostname or IPv4 (no IPv6 in bastion mode);
  - bastion hop `User` must NOT contain `@` (unambiguous username construction);
  - `Session.Auth`: if any bastion hop, the "exactly one of password/keyPath
    must be set" rule is relaxed to "may be empty; if set must still be a
    consistent XOR" (change `checkAuth` call site for the session only).
- `cmd/seed` fixtures: add one session + one saved jump host using bastion
  mode (exercises save/UI paths; not dial-able in tests).

## 4. Engine (`internal/sshengine`)

- `hop` struct (`live.go:80`) gains `bastion bool` (+ nothing else: the
  effective username is computed once).
- `buildSessionChain` (`live.go:279`):
  - `h.bastion = j.Bastion` for each jump host;
  - when `j.Bastion`: `h.user = j.User + "@" + sess.Host` (sess.Host is
    already validated hostname/IPv4 — no brackets possible);
  - the final target hop is marked `skipDial = true` when a bastion hop is
    present (guard re-check; model validation is the primary gate).
- Dial loops — `live.go dial()` (142–190) and `testconn.go dialHopChain`
  (53–101), the ONE shared pattern:
  - `for i := range hops { if h.skipDial { break } ... }` — the bastion
    client becomes the **final** client; pty/`NewSession` (live) or chain
    return (test) use it unchanged. `TestConnection` success = bastion relay
    reachable (no pty needed).
  - error attribution: skipped target hop never produces `hopErrorAt`;
    failures carry the bastion hop's position ("jump host N/M (…)") — the
    bastion's own disconnect text (e.g. "Bastion can't connect to target
    server") surfaces verbatim in the message, which is the actionable
    diagnostic (wrong target → that message).
- **keyboard-interactive** (new file `internal/sshengine/kbdint.go` + tiny
  change in `sshx/auth.go`):
  - `sshx.BastionAuthMethods(auth model.Auth, passphrase *string)
    []ssh.AuthMethod` — returns `[publickey (AuthKey case, same ErrKeyPassphrase
    Required prompt path as today)] + [ssh.KeyboardInteractive(challenge)]`.
    `challenge` is supplied by the engine per conn (closure, like
    `connApprover`).
  - `authenticateHop` (`live.go:364`): when `h.bastion`, use
    `sshx.BastionAuthMethods`; otherwise the existing `sshx.AuthMethods` —
    zero behavior change for non-bastion chains.
  - Prompt plumbing reuses the existing connID-keyed prompt-slot machinery
    (`manager.go`): new `promptKind` `promptKbdint`; `promptSlot.chAnswers
    chan kbdintResolution` (`{answers []string; aborted bool}`);
    `beginKbdintPrompt(connID, prefill, name, instruction, questions, echos)`
    emits `EventVaultKbdintPrompt = "vault:kbdint-prompt"` and returns the
    resolve channel; `SubmitKbdintResponse(connID, answers)` /
    `CancelKbdint(connID)` resolve it; `abortConnPrompt` and the chain-end
    cleanup drop the slot exactly like the host-key slot (drain →
    aborted/ctx.Err so the handshake goroutine unblocks).
  - Challenge implementation (called from the x/crypto handshake goroutine —
    blocking is the established pattern, same as the host-key callback):
    - `len(questions) == 0` → `return nil, nil` (completion round, no UI);
    - emit prompt → `select { case r := <-ch: (aborted → error
      "keyboard-interactive authentication cancelled"; else r.answers)
      case <-time.After(PromptTimeout): discard slot, error "keyboard-interactive
      prompt timed out" case <-ctx.Done(): discard slot, ctx.Err() }`;
    - **prefill**: closure captures the hop's auth —
      `prefill = hop.auth.Password` when `Type == AuthPassword`, else `""`.
    - guard: max 10 rounds per connection → error "too many
      keyboard-interactive rounds" (defense against a broken server).
  - `Manager.SubmitKbdintResponse/CancelKbdint` typed errors: same
    `ErrNoPendingPrompt` as the existing resolvers.
- `KbdintPromptPayload` (`manager.go` next to `KeyPromptPayload`):
  `{connID string \`json:"connID"\`; name string \`json:"name,omitempty"\`;
  instruction string \`json:"instruction,omitempty"\`; questions []string
  \`json:"questions"\`; echo []bool \`json:"echo"\`; prefill string
  \`json:"prefill,omitempty"\`}` — everything except `prefill` is
  non-sensitive server text; `prefill` is the single §8 exception (decision 4).

## 5. Service layer (`internal/wailsvc`)

- `VaultService` gains `SubmitKbdintResponse(connID string, answers []string)
  error` and `CancelKbdint(connID string) error` — delegate to the manager,
  same locked-vault guard as `SubmitKeyPassphrase` (`vaultservice.go:151`);
  answers never logged.
- DTOs (`dto.go`): `JumpHostDTO` / `SavedJumpHostDTO` gain `Bastion bool`
  (no secrets). `applyJumpHostSnapshot` / editor snapshot merge
  (`sessionservice.go:118,174`) carry the flag.
- Bindings (`frontend/bindings/`): regenerated by the wails build/dev step —
  **never hand-edited** (AGENTS.md); verify `tsc --noEmit` passes after.

## 6. Frontend (`frontend/src`)

- `components/prompts.ts`: `showKbdintPrompt(payload)` (mirror of
  `showKeyPrompt`, 99–178): title "SSH authentication"; `instruction` as
  subtitle (e.g. "LDAP authentication: server corp.tele2.ru, user
  supp.bercut228."); one input per question, label = question text,
  `type = echo[i] ? "text" : "password"`, `value = prefill` (single value
  applied to all password-style inputs; the user may edit), `autocomplete="off"`;
  Enter submits all answers → `VaultService.SubmitKbdintResponse(connID,
  answers)`; Cancel → `VaultService.CancelKbdint(connID)` then close (fail-fast
  abort, no 120 s wait).
- `main.ts`: new `EV.KbdintPrompt = "vault:kbdint-prompt"` + subscription +
  route in `handleEvent` (alongside `EV.KeyPrompt`, 183–184/284–285).
- `components/session-editor.ts` (jump rows, 322–441):
  - per jump row: checkbox **"Bastion"** (tooltip: "route this session's
    target through this bastion as login user@target");
  - when checked: helper line under the row: "Session Host is reached through
    this bastion; target login = bastion login; target port must be 22
    (v1).";
  - live inline validation (same `jumpHosts[i].…` field-error routing,
    649–651): only-one-bastion / must-be-last / port-22 / no-ProxyJump /
    no-`@`-in-user — messages come from the Go validator (shared with save).
  - when any bastion row is active: the target **Auth** section is replaced
    by a static note: "Bastion handles target authentication — you will be
    prompted when connecting (your saved bastion password prefills the
    prompt)." (existing saved value is preserved, merely unused);
  - session Port field: helper "must be 22 while a bastion jump host is
    active".
- `components/jump-host-dialog.ts` (P006 saved jump hosts): identical
  checkbox + same validation wiring.
- `store.ts`: `JumpHostDTO`/`SavedJumpHostDTO` types gain `bastion?: boolean`.
- No new theme tokens; reuse existing dialog/form CSS.

## 7. Tests

Unit (Go, in-process rig `internal/sshengine/testutil_test.go`; `make test`):
- model: every rule in §3 (position/last, single bastion, port-22, no
  ProxyJump, `@` in user, session-auth-optional-only-in-bastion-mode, saved
  jump host parity, old payloads without the flag ≙ direct).
- `buildSessionChain`: effective username `juser@targethost`; `skipDial` set
  only on the target when a bastion is present; non-bastion chains byte-
  identical.
- kbdint flow against a **new in-process server** built on the current
  x/crypto `ssh.Server` API (`KeyboardInteractiveCallback`, not the legacy
  rig which predates it):
  - 2-round server (round 1 expects `b-pass`, returns partial success; round
    2 expects `t-pass`), plus a zero-question completion round;
  - full success: session with bastion hop → expect event order:
    `vault:hostkey-prompt` (fresh known_hosts, approve) →
    `vault:kbdint-prompt` (prefill = stored bastion password present) →
    `SubmitKbdintResponse` → second `vault:kbdint-prompt` → submit →
    `terminal:status: ready` + PTY echo; server-side assertion that the
    authenticated username equals `juser@targethost`;
  - failure paths: wrong answer → `error` state with hop-attributed message;
    `CancelKbdint` → "…authentication cancelled"; `PromptTimeout` ~300 ms →
    "…prompt timed out"; runaway server (>10 rounds) → "too many
    keyboard-interactive rounds";
  - disconnect mid-prompt (server drops conn while a round is pending) →
    dial fails, no leaked prompt slot, no goroutine delta (existing settle
    helper), `-race` clean.
- `TestConnection` with bastion: passes through the same prompt flow with an
  ephemeral connID; closes cleanly.
- Frontend: `tsc --noEmit` via `make lint` (no e2e framework in v1).

Integration (`make test-integration`): no change — the vendor bastion can't
be emulated in `docker/sshd` (custom PAM); the in-process unit suite covers
the protocol. Documented as out of scope.

Manual QA (real bastion 10.246.71.186, user runs):
1. Session: Host 10.246.220.151:22, user supp.bercut228, jump row
   10.246.71.186:22 user supp.bercut228 (password = LDAP pw) + Bastion
   checked → save → no validation errors.
2. First connect: host-key prompt (accept) → kbdint round 1 prefilled →
   Enter → round 2 (edit if the target password differs) → shell shows the
   target banner; status bar correct.
3. [Test connection] from the editor (same prompts, no terminal opened).
4. SFTP panel (Ctrl+Shift+E) on the bastion session — expected: works if the
   bastion relays subsystem channels, otherwise graceful error (record which).
5. P004 monitor on the bastion session — record exec relay behavior.
6. Reconnect/Retry, close tab, Lock with a kbdint prompt open (prompt drains,
   app locks cleanly), app exit.
7. Reference a bastion SavedJumpHost from a session (P006 path).
8. Wrong target in the session (e.g. 10.246.220.999-style valid-but-down
   host) → expect the bastion's "Bastion can't connect to target server"
   attributed to the bastion hop.

## 8. Docs

- Master plan: §2 D5 row (bastion variant of a jump host), §4 data model
  (`Bastion` field + rules), §5 event contract table (new
  `vault:kbdint-prompt` row with producer + payload), §6 session-editor spec
  (checkbox + note text + helper), §8 item 1 (prefill exception, scoped to
  `vault:kbdint-prompt.payload.prefill`, one password per hop per prompt).
- AGENTS.md: event contract table row; §5 item 1 exception clause; §6 one
  line on bastion mode (target login = bastion login, port 22 in v1).
- Session editor helper strings stay English-only (project convention).

## 9. Security review (master plan §8)

- **Prefill exception (user-approved)**: only the bastion hop's stored
  password may cross IPC, only inside `vault:kbdint-prompt.payload.prefill`,
  only to prefill masked inputs. Not logged, not persisted, not cached, not
  exposed by `Tree()`. Any other credential over IPC remains forbidden.
- kbdint answers: typed by the user at prompt time (same trust class as the
  key-passphrase flow); never stored, never cached in process memory.
- No local exec surface anywhere (no ProxyCommand-style local command); the
  bastion only relays SSH channels.
- TOFU unchanged: host_key verified for the bastion hop via the existing
  `known_hosts` flow; the target's own host key is never seen by the client
  (inherent to this bastion format — same limitation as the OpenSSH CLI
  using `user@target@bastion`; documented).
- Config dirs/permissions, atomic vault writes, zeroization: unaffected
  (no new secret material on disk or in memory).

## 10. Rollout / migration

- Purely additive: `bastion` optional JSON field in the encrypted payload and
  DTOs; no envelope version bump; existing vaults/sessions keep direct
  semantics byte-for-byte.
- Wails bindings regenerated by the build step; `make lint` gates tsc.
- Exit criteria: `make test` (race), `make lint` pass on a clean container;
  manual QA checklist §7 green; commit after: (1) model+engine+sshx,
  (2) wailsvc+frontend prompts/editor, (3) seed+docs.

## 11. Open questions (non-blocking; resolve during QA, do not gate coding)

1. Does the bastion format support `user@host:port`? (admin) → if yes:
   follow-up P (v1.1): encode port when `Session.Port != 22`.
2. What is kbdint round 2 exactly (target's password / 2FA / OTP)? (admin)
   → affects doc wording only; prefill-all-rounds design is agnostic.
3. Does the bastion relay subsystem (SFTP) and exec (monitor) channels?
   (QA item §7.4–7.5 records the answer.)
4. Is the target list allow-listed per user on the bastion? (admin) → doc.

## 12. Explicitly out of scope

- Emulating the vendor bastion in `docker/sshd` integration tests.
- kbdint support for non-bastion hops (future: generalize
  `BastionAuthMethods` to all hops — trivial once this lands).
- Storing/caching kbdint answers; target-login-differs-from-bastion-login
  formats; IPv6/non-22 targets through the bastion.
- Any bastion that forces its own login flow (menu/SSO web login,
  asset-picker UI) — different product.
