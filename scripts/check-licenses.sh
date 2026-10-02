#!/usr/bin/env bash
# check-licenses.sh — tool-free drift guard for the third-party runtime notices.
#
# Runs inside the shelve-dev container via `make licenses-check` (a prerequisite
# of `make lint`), so it needs no host toolchain. It fails when:
#   1. a production Go module linked into ./cmd/shelve-backend is not declared in
#      third_party/runtime-deps.txt;
#   2. a runtime npm `dependencies` key from package.json is not declared there;
#   3. a declared runtime license id is copyleft (GPL/AGPL/LGPL/SSPL);
#   4. a declared runtime dependency is not named in THIRD-PARTY-NOTICES.md.
#
# The manifest is the machine-readable source; THIRD-PARTY-NOTICES.md reproduces
# the license texts. Update both when a runtime dependency changes.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIST="$ROOT/third_party/runtime-deps.txt"
NOTICES="$ROOT/THIRD-PARTY-NOTICES.md"
PKG="$ROOT/package.json"

fail=0
note() { printf '  FAIL %s\n' "$1" >&2; fail=1; }

if [ ! -f "$LIST" ]; then
    echo "error: missing runtime dependency manifest: $LIST" >&2
    exit 1
fi
if [ ! -f "$NOTICES" ]; then
    echo "error: missing notices file: $NOTICES" >&2
    exit 1
fi

echo "==> Checking runtime dependency notices..."

# --- 1. production Go modules ----------------------------------------------
actual_go="$(cd "$ROOT" && go list -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}' \
    ./cmd/shelve-backend 2>/dev/null | sort -u)"
listed_go="$(awk '$1 == "go" { print $2 }' "$LIST" | sort -u)"

if [ -z "$actual_go" ]; then
    note "could not enumerate production Go modules (go list failed)"
else
    while IFS= read -r mod; do
        [ -n "$mod" ] || continue
        if ! grep -qxF "$mod" <<<"$listed_go"; then
            note "Go module not declared in third_party/runtime-deps.txt: $mod"
        fi
    done <<<"$actual_go"
fi

# --- 2. runtime npm dependencies -------------------------------------------
if command -v node >/dev/null 2>&1; then
    actual_npm="$(node -e \
        'const p=require(process.argv[1]);for(const k of Object.keys(p.dependencies||{}))console.log(k)' \
        "$PKG" 2>/dev/null | sort -u)"
    listed_npm="$(awk '$1 == "npm" { print $2 }' "$LIST" | sort -u)"
    while IFS= read -r pkg; do
        [ -n "$pkg" ] || continue
        if ! grep -qxF "$pkg" <<<"$listed_npm"; then
            note "npm runtime dependency not declared in third_party/runtime-deps.txt: $pkg"
        fi
    done <<<"$actual_npm"
else
    note "node is required to read package.json dependencies"
fi

# --- 3. copyleft guard -----------------------------------------------------
if grep -nE '^[^#].*\b(GPL|AGPL|LGPL|SSPL)\b' "$LIST" >/dev/null 2>&1; then
    note "copyleft license id declared in the runtime set:"
    grep -nE '^[^#].*\b(GPL|AGPL|LGPL|SSPL)\b' "$LIST" >&2
fi

# --- 4. notices coverage ---------------------------------------------------
while read -r eco mod _lic; do
    case "$eco" in
        go|npm) ;;
        *) continue ;;
    esac
    if ! grep -qF "$mod" "$NOTICES"; then
        note "runtime dependency not named in THIRD-PARTY-NOTICES.md: $mod"
    fi
done <"$LIST"

if [ "$fail" -ne 0 ]; then
    echo "==> LICENSES CHECK FAILED" >&2
    exit 1
fi
echo "==> LICENSES CHECK OK"
