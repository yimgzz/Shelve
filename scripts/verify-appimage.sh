#!/usr/bin/env bash
# verify-appimage.sh — phase E5: headless verification of the Shelve AppImage.
#
# Checks:
#   1. The artifact extracts cleanly (--appimage-extract; FUSE not required).
#   2. Required payload is present: the `shelve` Electron binary, the bundled
#      Chromium `.so` set, `chrome-sandbox`, the desktop entry + icon, the app
#      payload in `resources/app.asar`, and the Go backend at
#      `resources/backend/shelve-backend` (executable, OUTSIDE the asar).
#   3. The Go backend is NOT inside app.asar (parsed from the asar header).
#   4. No `docker/sshd` integration-test fixture strings leaked into the
#      payload (master plan §8: no test credentials ship).
#   5. Best-effort smoke: in a pristine `debian:13-slim` with only the
#      documented Electron runtime libraries (NOT GTK4/WebKit), run the AppImage
#      under xvfb and assert the backend reaches its "backend ready" handshake
#      without a missing-library error. Skipped cleanly when the container
#      cannot install xvfb (no network) or is missing bash.
#
# Usage: ./scripts/verify-appimage.sh [path-to-AppImage]
# Default artifact: the newest bin/shelve-*.AppImage.
#
# Requires: docker on the host (the host needs Docker only — master plan §7).

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ---------------------------------------------------------------------------
# Resolve the artifact
# ---------------------------------------------------------------------------
DEFAULT_IMG="$(ls -1 "$ROOT"/bin/shelve-*.AppImage 2>/dev/null | head -1)"
APPIMAGE="${1:-$DEFAULT_IMG}"

if [ -z "${APPIMAGE:-}" ] || [ ! -f "$APPIMAGE" ]; then
    echo "error: AppImage not found: ${APPIMAGE:-<none>}" >&2
    echo "hint: run 'make appimage' first, or pass the artifact path explicitly." >&2
    exit 1
fi
if [ ! -x "$APPIMAGE" ]; then
    echo "error: AppImage is not executable: $APPIMAGE (chmod +x?)" >&2
    exit 1
fi

echo "==> Verifying $(basename "$APPIMAGE")"

# ---------------------------------------------------------------------------
# Extract (FUSE-free) into a scratch dir
# ---------------------------------------------------------------------------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> Extracting (--appimage-extract)..."
(
    cd "$WORK" || exit 1
    "$APPIMAGE" --appimage-extract >/dev/null
)
APPDIR="$WORK/squashfs-root"
if [ ! -d "$APPDIR" ]; then
    echo "error: extraction did not produce squashfs-root" >&2
    exit 1
fi

FAIL=0

# ---------------------------------------------------------------------------
# Required payload: electron-builder places the linux-unpacked tree, the
# desktop entry and AppRun at the AppDir root.
# ---------------------------------------------------------------------------
echo "==> Checking bundled payload..."

check() { # rel [exec]
    local rel="$1" mod="${2:-}"
    if [ ! -e "$APPDIR/$rel" ]; then
        echo "  MISS $rel" >&2
        FAIL=1
    elif [ "$mod" = exec ] && [ ! -x "$APPDIR/$rel" ]; then
        echo "  MISS $rel (not executable)" >&2
        FAIL=1
    else
        echo "  ok   $rel"
    fi
}

check AppRun exec
check shelve exec
check chrome-sandbox
check chrome_crashpad_handler
check resources/app.asar
check resources/backend/shelve-backend exec
check shelve.desktop
check .DirIcon

# Chromium's bundled runtime libs (proves Electron's Chromium ships, so the
# host needs no GTK4/WebKit stack; master plan §12 #6).
for lib in \
    libEGL.so \
    libGLESv2.so \
    libffmpeg.so \
    libvk_swiftshader.so \
    libvulkan.so.1 \
    ; do
    check "$lib"
done

# Chromium data files + at least one locale.
for data in \
    icudtl.dat \
    resources.pak \
    chrome_100_percent.pak \
    snapshot_blob.bin \
    v8_context_snapshot.bin \
    locales/en-US.pak \
    ; do
    check "$data"
done

# Desktop entry sanity: the launcher needs Exec/Icon/Name; StartupWMClass links
# the running window to the entry.
DESKTOP="$APPDIR/shelve.desktop"
if [ -f "$DESKTOP" ] \
    && grep -qE '^Exec=' "$DESKTOP" \
    && grep -qE '^Icon=' "$DESKTOP" \
    && grep -qE '^Name=' "$DESKTOP" \
    && grep -qE '^StartupWMClass=' "$DESKTOP"; then
    echo "  ok   desktop entry (Exec/Icon/Name/StartupWMClass)"
else
    echo "  MISS valid desktop entry $DESKTOP" >&2
    FAIL=1
fi

# ---------------------------------------------------------------------------
# The Go backend must live OUTSIDE the asar. app.asar is a 16-byte header with
# a uint32LE JSON size at offset 12, then the JSON file table at offset 16.
# ---------------------------------------------------------------------------
echo "==> Checking the backend is outside app.asar..."
ASAR="$APPDIR/resources/app.asar"
ASAR_SIZE="$(od -An -tu4 -j12 -N4 "$ASAR" 2>/dev/null | tr -d '[:space:]')"
if [ -n "$ASAR_SIZE" ] && [ "$ASAR_SIZE" -gt 0 ] 2>/dev/null; then
    ASAR_HEADER="$(dd if="$ASAR" bs=1 skip=16 count="$ASAR_SIZE" 2>/dev/null)"
    if printf '%s' "$ASAR_HEADER" | grep -q 'shelve-backend'; then
        echo "  FAIL the Go backend is bundled inside app.asar" >&2
        FAIL=1
    else
        echo "  ok   no backend entry in the asar header"
    fi
    if printf '%s' "$ASAR_HEADER" | grep -q 'dist-electron' \
        && printf '%s' "$ASAR_HEADER" | grep -q 'frontend'; then
        echo "  ok   main/preload + renderer present in the asar"
    else
        echo "  FAIL asar is missing the main/preload or renderer payload" >&2
        FAIL=1
    fi
else
    echo "  FAIL could not read the app.asar header" >&2
    FAIL=1
fi

# ---------------------------------------------------------------------------
# No integration-test credentials / fixtures in the shipped payload.
# ---------------------------------------------------------------------------
echo "==> Scanning for test-fixture leakage..."
LEAK=0
for needle in "hello root text" "hello nested markdown" "nested deep file" "dsm-fake-editor"; do
    if grep -rlaF --exclude-dir=locales --exclude=LICENSES.chromium.html -- "$needle" "$APPDIR" >/dev/null 2>&1; then
        echo "  FAIL test fixture string present: $needle" >&2
        LEAK=1
    fi
done
if [ "$LEAK" -eq 0 ]; then
    echo "  ok   no docker/sshd fixture strings in the payload"
else
    FAIL=1
fi

# ---------------------------------------------------------------------------
# Best-effort smoke: pristine debian:13-slim + xvfb + the documented Electron
# runtime libraries. This proves the AppImage needs no GTK4/WebKit stack and
# reaches the Go backend handshake. The container gets a read-only mount of the
# artifact and writes its app config to /tmp.
# ---------------------------------------------------------------------------
echo "==> Smoke run in debian:13-slim + xvfb (best-effort)..."
SMOKE_IMAGE="debian:13-slim"
LIBS_FILE="$ROOT/scripts/host-runtime-libs.txt"
if [ ! -f "$LIBS_FILE" ]; then
    echo "error: runtime-library list not found: $LIBS_FILE" >&2
    exit 1
fi
if ! docker image inspect "$SMOKE_IMAGE" >/dev/null 2>&1; then
    docker pull -q "$SMOKE_IMAGE" >/dev/null 2>&1 || true
fi

if ! docker image inspect "$SMOKE_IMAGE" >/dev/null 2>&1; then
    echo "  skip: cannot obtain $SMOKE_IMAGE (no docker/network)"
else
    SMOKE_OUT="$(docker run --rm \
        -v "$APPIMAGE":/opt/shelve.AppImage:ro \
        -v "$LIBS_FILE":/tmp/host-runtime-libs.txt:ro \
        -e HOME=/tmp -e XDG_CONFIG_HOME=/tmp/shelve-config \
        "$SMOKE_IMAGE" bash -c '
            set -e
            export DEBIAN_FRONTEND=noninteractive
            command -v apt-get >/dev/null 2>&1 || exit 43
            apt-get update -qq >/dev/null 2>&1 || exit 42
            # The Electron/Chromium runtime set comes from the mounted
            # scripts/host-runtime-libs.txt (single source with the Makefile/README).
            apt-get install -y -qq --no-install-recommends \
                xvfb xauth $(grep -v "^#" /tmp/host-runtime-libs.txt) \
                >/dev/null 2>&1 || exit 42
            command -v xvfb-run >/dev/null 2>&1 || exit 43
            mkdir -p /tmp/shelve-config
            timeout 60 xvfb-run -a /opt/shelve.AppImage \
                --appimage-extract-and-run --no-sandbox --disable-gpu \
                > /tmp/smoke.log 2>&1 || true
            cat /tmp/smoke.log
            echo "__SHELVE_SMOKE_DONE__"
            exit 0
        ' 2>&1)"
    SMOKE_RC=$?

    if printf '%s' "$SMOKE_OUT" | grep -q "__SHELVE_SMOKE_DONE__"; then
        if printf '%s' "$SMOKE_OUT" | grep -qiE "error while loading shared libraries|cannot open shared object"; then
            echo "  FAIL missing shared library in the pristine container:" >&2
            printf '%s\n' "$SMOKE_OUT" | grep -iE "error while loading shared libraries|cannot open shared object" >&2
            FAIL=1
        elif printf '%s' "$SMOKE_OUT" | grep -q "backend ready at"; then
            echo "  ok   reached the backend-ready handshake without GTK4/WebKit"
        else
            echo "  FAIL the app never logged 'backend ready at'" >&2
            printf '%s\n' "$SMOKE_OUT" | tail -n 20 >&2
            FAIL=1
        fi
    else
        echo "  skip: container could not install/run xvfb (rc=$SMOKE_RC)"
    fi
fi

# ---------------------------------------------------------------------------
# Result
# ---------------------------------------------------------------------------
if [ "$FAIL" -ne 0 ]; then
    echo "==> VERIFY FAILED" >&2
    exit 1
fi
echo "==> VERIFY OK — $(basename "$APPIMAGE") is self-contained"
