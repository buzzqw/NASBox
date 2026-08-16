#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="$ROOT_DIR/packaging/build"
DIST_DIR="$ROOT_DIR/dist"
APPDIR="$BUILD_DIR/NASBox.AppDir"
VERSION="$($ROOT_DIR/packaging/version.sh)"
ARCH="${ARCH:-$(uname -m)}"

case "$ARCH" in
    x86_64|aarch64) ;;
    *) printf 'Unsupported AppImage architecture: %s\n' "$ARCH" >&2; exit 1 ;;
esac

APPIMAGETOOL="${APPIMAGETOOL:-$(command -v appimagetool || true)}"
if [[ -z "$APPIMAGETOOL" || ! -x "$APPIMAGETOOL" ]]; then
    printf '%s\n' 'Set APPIMAGETOOL to an executable appimagetool path.' >&2
    exit 1
fi

"$ROOT_DIR/packaging/build-client.sh" >/dev/null
rm -rf "$APPDIR"
mkdir -p "$APPDIR/usr/lib" "$APPDIR/usr/share/applications" "$APPDIR/usr/share/icons/hicolor/scalable/apps" "$DIST_DIR"
cp -a "$BUILD_DIR/client/nasbox" "$APPDIR/usr/lib/nasbox"
cp "$ROOT_DIR/packaging/linux/nasbox.desktop" "$APPDIR/nasbox.desktop"
cp "$ROOT_DIR/packaging/linux/nasbox.desktop" "$APPDIR/usr/share/applications/nasbox.desktop"
cp "$ROOT_DIR/packaging/linux/nasbox.svg" "$APPDIR/nasbox.svg"
cp "$ROOT_DIR/packaging/linux/nasbox.svg" "$APPDIR/usr/share/icons/hicolor/scalable/apps/nasbox.svg"

cat > "$APPDIR/AppRun" <<'EOF'
#!/usr/bin/env bash
set -e
HERE="$(dirname "$(readlink -f "$0")")"
export NASBOX_DISABLE_CLIENT_UPDATE=1
exec "$HERE/usr/lib/nasbox/nasbox" "$@"
EOF
chmod +x "$APPDIR/AppRun"

OUTPUT="$DIST_DIR/NASBox-$VERSION-$ARCH.AppImage"
rm -f "$OUTPUT"
RUNTIME_ARGS=()
if [[ -n "${APPIMAGE_RUNTIME_FILE:-}" ]]; then
    RUNTIME_ARGS=(--runtime-file "$APPIMAGE_RUNTIME_FILE")
fi
ARCH="$ARCH" VERSION="$VERSION" APPIMAGE_EXTRACT_AND_RUN=1 \
    "$APPIMAGETOOL" "${RUNTIME_ARGS[@]}" "$APPDIR" "$OUTPUT"
chmod +x "$OUTPUT"
printf '%s\n' "$OUTPUT"
