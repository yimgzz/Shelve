# Shelve

Stash your shells in style!

A lightweight, fast, fully local SSH session manager. Go backend
(`golang.org/x/crypto/ssh`) + Wails v3 frontend (vanilla TypeScript + Vite).
Sessions live in an encrypted vault (Argon2id + AES-256-GCM) under
`$XDG_CONFIG_HOME/shelve`; no cloud, no telemetry, no accounts.

**Status:** v1.1 — feature-complete (Phases 1–5c done, final gate closed). The
app is a working SSH session manager: encrypted vault, session tree + live
search, terminal tabs, and an optional SFTP browser (browse / upload / download /
mkdir / rename / delete / edit-text-with-system-editor). Roadmap: `.kilo/plans/`
(master plan + phase plans).

## Prerequisites

- **Docker** — the only host tool required for development & builds.
  No Go, Node, GTK or WebKit packages are needed on the host for
  `make dev` / `make build` / `make test` / `make lint`.
- To run the built binary directly on the host (`make run`): the host
  distro's GTK4 + WebKitGTK 6.0 **runtime** libraries
  (e.g. `gtk-4`, `webkitgtk-6.0` on ALT).

## Development workflow

| Command | Action |
|---|---|
| `make dev-image` | Build the `shelve-dev` toolchain image (lazy: `dev`/`build` do it automatically) |
| `make dev` | `wails3 dev` in the container — hot reload; the window opens on your desktop |
| `make build` | Release build in the container → `bin/shelve` |
| `make run` | Run the built binary on the host (X11) |
| `make test` | Go unit tests in the container |
| `make test-race` | Go unit tests with the race detector |
| `make test-integration` | SFTP/SSH integration tests via testcontainers (needs the Docker socket; builds `docker/sshd`) |
| `make lint` | `gofmt` + `go vet` (container) + frontend `tsc --noEmit` |
| `make clean` | Remove `bin/`, `frontend/dist/`, `frontend/bindings/` |
| `make wails-init` | Re-merge the pinned Wails template (recreates frontend/build scaffolding) |

`make dev` details:

- X11 forwarding: mounts `/tmp/.X11-unix`, passes `DISPLAY` (and
  `XAUTHORITY` if `~/.Xauthority` exists). Works on X11 and on Wayland via
  XWayland — no native Wayland socket forwarding in v1.
- `--network host` so the app can reach real SSH hosts.
- `~/.config/shelve` (created 0700) is mounted into the
  container so the real vault/settings persist across runs.
- Hot reload: frontend edits → Vite reloads the UI without a rebuild;
  Go edits → the app is rebuilt and restarted.

If the window fails to open under `make dev`:

1. Temporarily allow local X clients and retry: `xhost +local:`
   (revoke with `xhost -local:` afterwards).
2. On some desktops the in-container window still cannot start (GTK/DBus
   session restrictions inside the container, sandbox/namespace limits).
   In that case use the host-run workflow — it is fully supported and is
   the reliable path:

   ```
   make build   # compile in the container -> bin/shelve
   make run     # launch the binary on the host (window on your desktop)
   ```

## Development notes

- **Seed tool** (`cmd/seed`): generates a disposable vault with folders and
  sessions (e.g. a 300-session fixture for search/performance smoke tests).
  Build and run it inside the container, pointing it at a scratch config dir;
  it never touches your real vault unless you tell it to.
- **In-container window limitation vs `make run`:** `make dev` runs the app
  *inside* the container. On some desktops (observed: GNOME/XWayland on ALT
  Linux) the in-container window cannot start due to GTK/DBus session
  restrictions inside the container plus WebKit sandbox namespace limits. In
  that case the always-supported workflow is `make build` + `make run` (build
  in the container, run the binary on the host). The in-container hot-reload
  loop is then not available — but UI phases are QA'd via the host-run binary.
- **Settings** (gear menu → Settings): theme, auto-lock minutes, SFTP browser
  toggle, terminal font/size/scrollback, and the text-editor command are saved
  to `settings.json`. Theme and terminal options apply live.
- **SFTP temp files & edit logs:** the app keeps per-edit temp copies and
  editor logs under the config-directory `tmp/` (i.e.
  `$XDG_CONFIG_HOME/shelve/tmp/`). `tmp/edit-*.log` captures the
  text editor's stdout/stderr — handy when a configured editor misbehaves.
  Temp files are 0600 and are swept on vault lock, app exit, and at startup
  (stale-sweep); a clean exit leaves `tmp/` empty.
- **Text editor command (SFTP "Edit as text"):** set in Settings → Files →
  "Text editor command". It is run verbatim (`strings.Fields`: a bare
  executable plus space-separated arguments) with the temporary file path
  appended **last** — e.g. `nano` or `xdg-open` work as-is; for an editor with
  flags use something like `code --wait`. The app re-uploads automatically
  once the edited copy has been stable for 3 s.
- **SFTP scope (v1):** the left panel replaces the session tree while the
  active session is ready — browse, upload, download, mkdir, rename, delete,
  and "edit text file with system editor". Drag & drop uploads are **out of
  scope for v1** (D4).

## SFTP browser

Enable it with **Ctrl+Shift+E** or Settings → General → "SFTP browser". With a
connected (ready) tab active, the left panel becomes an SFTP browser rooted at
the remote `$HOME`: breadcrumb navigation, [Upload] / [New folder] / [Refresh],
double-click to open a folder, edit a text-like file in your system editor, or
download others; right-click rows for Edit / Download / Upload to here / New
folder / Rename / Delete. The footer shows live transfer progress. When the
browser is enabled but no session is ready, the session tree shows with a hint
line.

## Shortcuts

| Keys | Action |
|---|---|
| Ctrl+K / Ctrl+L | Focus search |
| Ctrl+T | Connect the selected session (or open a new-session draft) |
| Ctrl+W | Close the active tab |
| Ctrl+Tab / Ctrl+Shift+Tab | Cycle tabs |
| Ctrl+1…9 | Activate nth tab |
| Ctrl+, | Open Settings |
| F2 / Delete | Rename / delete the selected tree node |
| Esc | Close the topmost modal / clear search |
| Ctrl+Shift+E | Toggle SFTP browser (mirrors the Settings checkbox) |

Shortcuts are suppressed while you are typing in a form field (except Esc,
which the dialogs/search handle themselves).

## First run & master password

On the very first launch the app shows the **"Create master password"** screen:
enter a password of at least **8 characters** plus a confirmation. This creates
your encrypted vault (`vault.json` in the config directory). The master password
is **never written to disk** — only a per-file random salt and KDF parameters
are stored, and the derived key lives in memory for the app session.

On every subsequent start you are prompted to **unlock** with the master
password. Wrong passwords show an inline error; the app will not proceed without
it. You can **Lock** explicitly from the gear menu, and opt in to **auto-lock**
after N minutes of inactivity (Settings → General; default `0` = off). A locked
app is unusable without the password — your sessions are safe even if the
config directory is copied off the machine.

The session tree is kept in the encrypted vault. There is no recovery
mechanism for a lost master password: keep a safe backup of your
`vault.json`.

## Security model

- **Encryption:** AES-256-GCM (random 12-byte nonce per write) with a 32-byte
  key derived from your master password via **Argon2id** (memory 64 MiB,
  iterations 3, threads 4, 16-byte random salt). Each vault write is atomic
  (temp file + rename) and uses the AAD tag `dsmsv1`.
- **No plaintext credentials on disk:** `vault.json` is the only store of
  secrets; `settings.json` and `known_hosts` are guaranteed by tests to contain
  none. Credentials are never sent across the Wails IPC — the SFTP panel passes
  only file *paths*, and Go streams the bytes (large files never cross IPC).
- **Memory hygiene:** the master password and derived key are zeroized after
  use; keys are never logged and errors never include password/key material.
- **Host keys:** app-managed `known_hosts` with TOFU — you must approve a new
  host key; a mismatch is a hard fail (never auto-accepted).
- **SSH key files:** read-only use; the app never writes or rewrites your key
  files. Key passphrases are prompted once per connection and cached only in
  process memory.
- **File/dir permissions:** the config directory is `0700`, all files `0600`.
- **Temp files:** SFTP edit temp copies are `0600` under `tmp/` and are swept
  on lock, exit, and at startup (stale sweep).
- **Network:** the app makes no network calls except to the SSH hosts you
  configure and local port-forward sockets on `127.0.0.1`. No telemetry, no
  update checks, no crash reporting.
- **Corrupt vault:** the app refuses to unlock a vault it cannot decrypt and
  never auto-overwrites it — keep backups of `vault.json`.

## Session card & Extra Args

Create or edit a session with these fields:

| Field | Notes |
|---|---|
| Name | required, non-empty |
| Host | required; hostname, IPv4 or IPv6 |
| Port | default `22`, range 1–65535 |
| User | required, non-empty |
| Auth | exactly one of **Password** or **SSH key path** (a key passphrase is asked on connect, never stored) |
| Jump Hosts | optional ordered list; each: host, port, user, auth (structured jump chains) |
| Extra Args | optional strict grammar below; validated inline on save |

**Extra Args grammar** (whitespace-separated; single/double quotes strip quotes
but do not join tokens):

```
-L [bind:]localPort:dstHost:dstPort     # local port forward (default bind 127.0.0.1)
-D [bind:]localPort                     # dynamic SOCKS5 forward
-o ServerAliveInterval=<int>
-o ServerAliveCountMax=<int>
-o ConnectTimeout=<int>
-o StrictHostKeyChecking=no|ask
ProxyJump=[user@]host[:port]            # at most one; port 0 = 22
```

Rules: forwards bind to `127.0.0.1` by default (loops back only, §8.9);
explicit ports must be 1–65535; duplicate `-o` keys — last wins; IPv6 is a
documented v1 limitation and is rejected; unknown flags or bare words are
validation errors shown in the editor. For multiple jump hosts use the
structured Jump Hosts list instead of several `ProxyJump=` tokens.

## Troubleshooting

- **Blank / hung window on some desktops** (NVIDIA/gbm): Wails sets
  `WEBKIT_DISABLE_DMABUF_RENDERER=1` automatically; if the window is still
  blank, export that variable manually before `make run` and relaunch.
- **In-container window fails under `make dev`** (GTK/DBus session limits inside
  the container, WebKit sandbox namespace limits — observed on GNOME/XWayland
  ALT Linux): use the always-supported workflow `make build` + `make run`
  (compile in the container, run the binary on the host). See the Development
  notes above.
- **X11 vs Wayland:** X11 forwarding works on X11 and on Wayland via XWayland;
  native Wayland socket forwarding is a v1 non-goal.
- **SFTP "Edit as text" misbehaves:** check `$XDG_CONFIG_HOME/shelve/tmp/edit-*.log`
  for the configured editor's stdout/stderr. The command is run verbatim
  (`strings.Fields`) with the temp file path appended last — use something like
  `nano`, `xdg-open`, or `code --wait`.
- **Lost master password:** there is no recovery; restore `vault.json` from a
  backup, or start fresh by removing the config directory.
- **Vault won't unlock:** the app refuses to open a vault it cannot decrypt
  rather than overwrite it — restore from a backup or re-create.

## Pinned versions

- Wails v3 CLI: **v3.0.0-beta.15** — `Dockerfile.dev` (`WAILS3_VERSION`)
  and `go.mod` (module `shelve`, `github.com/wailsapp/wails/v3`).
- Base image: `golang:1.25-trixie` (Debian 13, GTK 4.18, WebKitGTK 6.0/2.52,
  Node 20).

## Layout

`main.go` + `internal/` packages per the master plan (§5): `app` (composition
root), `config` (XDG paths, settings.json), `model`, `store`, `vault`,
`sshx`, `sshengine`, `sftp`, `wailsvc` (the only Go code besides `main.go`
touching the Wails API). Frontend under `frontend/` (bindings generated into
`frontend/bindings/`, gitignored).
