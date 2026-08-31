#!/usr/bin/env bash
# verify-appimage.sh — plan P004 T3: headless verification of the shelve AppImage.
#
# Checks:
#   1. The artifact extracts cleanly (--appimage-extract, FUSE not required).
#   2. Required payload is bundled: binary, GTK4 + WebKitGTK 6.0 runtime libs,
#      WebKit helper processes, GLib schemas, GDK pixbuf loader cache, desktop
#      entry, AppRun, .DirIcon.
#   3. Dependency self-containment (master plan §12 #1): inside a pristine
#      debian:13-slim container (no GTK packages) every GTK/WebKit-stack
#      dependency of usr/bin/shelve must resolve inside the AppDir via
#      LD_LIBRARY_PATH. A small allowlist covers linuxdeploy's *intentional*
#      exclusions — desktop-base libs present on any stock desktop (X11/Wayland,
#      font stack) and graphics-driver libs (GL/EGL/drm/gbm) that must come from
#      the host GPU stack (master plan §11: AppImage bundles the app runtime so
#      end users need no distro packages beyond a stock desktop).
#   4. Desktop entry sanity (Exec=/Icon= present). Note: wails3 v3.0.0-beta.15
#      places the .desktop file at the AppDir ROOT (upstream linuxdeploy
#      layout); usr/share/applications is intentionally left empty.
#
# Usage: ./scripts/verify-appimage.sh [path-to-AppImage]
# Default artifact: bin/shelve-<arch>.AppImage derived from the host arch.
#
# Requires: docker on the host (the host needs Docker only — master plan §7).
# On first run the debian:13-slim image is pulled (~30 MB).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ---------------------------------------------------------------------------
# Resolve the artifact
# ---------------------------------------------------------------------------
detect_arch() {
    case "$(uname -m)" in
        x86_64 | amd64) echo "x86_64" ;;
        aarch64 | arm64) echo "aarch64" ;;
        *) echo "unknown" ;;
    esac
}

DEFAULT_IMG="$ROOT/bin/shelve-$(detect_arch).AppImage"
# Prefer the versioned artifact (primary output since appimagetool requires
# <name>-<version>.AppImage); fall back to the bare alias.
VERSIONED_IMG="$(ls "$ROOT"/bin/shelve-*-$(detect_arch).AppImage 2>/dev/null | head -1)"
APPIMAGE="${1:-${VERSIONED_IMG:-$DEFAULT_IMG}}"

if [ ! -f "$APPIMAGE" ]; then
    echo "error: AppImage not found: $APPIMAGE" >&2
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
    cd "$WORK"
    "$APPIMAGE" --appimage-extract >/dev/null
)
APPDIR="$WORK/squashfs-root"
if [ ! -d "$APPDIR" ]; then
    echo "error: extraction did not produce squashfs-root" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Required payload paths
# ---------------------------------------------------------------------------
echo "==> Checking bundled payload..."
FAIL=0

for rel in \
    usr/bin/shelve \
    usr/lib/libgtk-4.so.1 \
    usr/lib/libwebkitgtk-6.0.so.4 \
    usr/share/glib-2.0/schemas/gschemas.compiled \
    shelve.desktop \
    AppRun \
    .DirIcon \
    ; do
    if [ -e "$APPDIR/$rel" ]; then
        echo "  ok   $rel"
    else
        echo "  MISS $rel" >&2
        FAIL=1
    fi
done

# WebKit helper processes: wails3 preserves their system path, so accept
# either usr/libexec/webkit2gtk-6.0/ or usr/lib/<triplet>/webkitgtk-6.0/.
for helper in WebKitWebProcess WebKitNetworkProcess libwebkitgtkinjectedbundle.so; do
    if find "$APPDIR" -name "$helper" -print -quit | grep -q .; then
        echo "  ok   $helper (found under usr/)"
    else
        echo "  MISS $helper" >&2
        FAIL=1
    fi
done

# GDK pixbuf loaders + cache (bundled by the linuxdeploy gtk plugin).
if find "$APPDIR/usr/lib" -path '*/gdk-pixbuf-2.0/*/loaders.cache' -print -quit | grep -q .; then
    echo "  ok   gdk-pixbuf loaders.cache"
else
    echo "  MISS gdk-pixbuf loaders.cache" >&2
    FAIL=1
fi

# Desktop entry sanity (AppDir root per wails3/linuxdeploy layout).
DESKTOP="$APPDIR/shelve.desktop"
if [ -f "$DESKTOP" ] && grep -qE '^Exec=' "$DESKTOP" && grep -qE '^Icon=' "$DESKTOP"; then
    echo "  ok   desktop entry (Exec/Icon, AppDir root)"
else
    echo "  MISS valid desktop entry at $DESKTOP" >&2
    FAIL=1
fi

# ---------------------------------------------------------------------------
# Zero-dependency ldd check in a pristine container (no GTK installed)
# ---------------------------------------------------------------------------
PLATFORM_ARGS=()
case "$(basename "$APPIMAGE")" in
    *aarch64*) PLATFORM_ARGS=(--platform linux/arm64) ;;
esac

echo "==> ldd check in debian:13-slim (no GTK installed)..."
# Allowlist split into two groups:
#  - glibc/kernel artifacts that linuxdeploy deliberately never bundles;
#  - linuxdeploy's intentional desktop-base exclusions: X11/Wayland/GL/font
#    stack present on any stock desktop (master §12 #1 target platform) plus
#    host-provided graphics drivers (GL/EGL/drm/gbm) which must NOT be bundled.
# Every GTK/WebKit-stack dependency NOT in this list MUST resolve in the AppDir.
ALLOW="libc.so.6 libm.so.6 libpthread.so.0 libdl.so.2 librt.so.1 \
libutil.so.1 libresolv.so.2 libnsl.so.1 libcrypt.so.1 libgcc_s.so.1 \
ld-linux-x86-64.so.2 ld-linux-aarch64.so.1 \
libX11.so.6 libX11-xcb.so.1 libxcb.so.1 libwayland-client.so.0 \
libGL.so.1 libEGL.so.1 libGLX.so.0 libOpenGL.so.0 libdrm.so.2 libgbm.so.1 \
libfontconfig.so.1 libfreetype.so.6 libharfbuzz.so.0 libfribidi.so.0 \
libexpat.so.1 libasound.so.2 libcom_err.so.2 libgpg-error.so.0 \
libz.so.1 libstdc++.so.6"

LDD_OUT="$(
    docker run --rm "${PLATFORM_ARGS[@]}" \
        -v "$APPDIR":/opt/app:ro \
        -e LD_LIBRARY_PATH=/opt/app/usr/lib \
        debian:13-slim \
        ldd /opt/app/usr/bin/shelve 2>&1 || true
)"

while IFS= read -r line; do
    case "$line" in
        *"not found"*)
            missing="$(printf '%s' "${line%% *}" | tr -d '[:space:]')"
            if echo " $ALLOW " | grep -q " $missing "; then
                echo "  ok   $missing (system allowlist, not bundled by design)"
            else
                echo "  FAIL $line" >&2
                FAIL=1
            fi
            ;;
        *"=>"*"/"*)
            lib="${line#*=> }"
            lib="${lib%% *}"
            base="$(basename "$lib")"
            case "$lib" in
                /opt/app/usr/lib/*)
                    echo "  ok   $base (bundled)"
                    ;;
                *)
                    if echo " $ALLOW " | grep -q " $base "; then
                        echo "  ok   $base (system allowlist)"
                    else
                        echo "  FAIL $lib resolves outside the AppDir" >&2
                        FAIL=1
                    fi
                    ;;
            esac
            ;;
    esac
done <<<"$LDD_OUT"

# ---------------------------------------------------------------------------
# Result
# ---------------------------------------------------------------------------
if [ "$FAIL" = 1 ]; then
    echo "==> VERIFY FAILED" >&2
    exit 1
fi
echo "==> VERIFY OK — $(basename "$APPIMAGE") is self-contained"