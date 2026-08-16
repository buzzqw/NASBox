#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="$ROOT_DIR/packaging/build/client-update"
DIST_DIR="${1:-$ROOT_DIR/dist}"
VERSION="$($ROOT_DIR/packaging/version.sh)"
EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT_DIR" log -1 --format=%ct 2>/dev/null || printf '0')}"
OUTPUT="$DIST_DIR/nasbox-client-update-$VERSION.tar.gz"

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR/client-update" "$DIST_DIR"
cp -a "$ROOT_DIR/client/." "$BUILD_DIR/client-update/"
rm -rf "$BUILD_DIR/client-update/tests" "$BUILD_DIR/client-update/.update"
find "$BUILD_DIR/client-update" -type d -name __pycache__ -prune -exec rm -rf {} +
find "$BUILD_DIR/client-update" -type f \( -name '*.pyc' -o -name '*.pyo' \) -delete

tar --sort=name --mtime="@$EPOCH" --owner=0 --group=0 --numeric-owner \
    -C "$BUILD_DIR" -cf - client-update | gzip -n > "$OUTPUT"
(cd "$DIST_DIR" && sha256sum "$(basename "$OUTPUT")" > "$(basename "$OUTPUT").sha256")
printf '%s\n' "$OUTPUT"
