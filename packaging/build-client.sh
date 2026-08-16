#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="$ROOT_DIR/packaging/build"
PAYLOAD_DIR="$BUILD_DIR/client"

if ! python3 -c 'import PyInstaller, PyQt6' >/dev/null 2>&1; then
    printf '%s\n' "Build dependencies are missing. Run:" >&2
    printf '  python3 -m pip install -r %q -r %q\n' \
        "$ROOT_DIR/requirements.txt" "$ROOT_DIR/packaging/requirements-build.txt" >&2
    exit 1
fi

rm -rf "$PAYLOAD_DIR" "$BUILD_DIR/pyinstaller"
mkdir -p "$PAYLOAD_DIR" "$BUILD_DIR/pyinstaller"

python3 -m PyInstaller \
    --noconfirm \
    --clean \
    --onedir \
    --windowed \
    --name nasbox \
    --distpath "$PAYLOAD_DIR" \
    --workpath "$BUILD_DIR/pyinstaller/work" \
    --specpath "$BUILD_DIR/pyinstaller" \
    "$ROOT_DIR/client/main.py"

test -x "$PAYLOAD_DIR/nasbox/nasbox"
printf '%s\n' "$PAYLOAD_DIR/nasbox"
