#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="$ROOT_DIR/packaging/build"
DIST_DIR="$ROOT_DIR/dist"
PACKAGE_DIR="$BUILD_DIR/deb"
VERSION="$($ROOT_DIR/packaging/version.sh)"
ARCH="${DEB_ARCH:-$(dpkg --print-architecture)}"

command -v dpkg-deb >/dev/null 2>&1 || { printf '%s\n' 'dpkg-deb is required.' >&2; exit 1; }
"$ROOT_DIR/packaging/build-client.sh" >/dev/null
rm -rf "$PACKAGE_DIR"
mkdir -p "$PACKAGE_DIR/DEBIAN" "$PACKAGE_DIR/usr/bin" "$PACKAGE_DIR/usr/lib" \
    "$PACKAGE_DIR/usr/share/applications" "$PACKAGE_DIR/usr/share/icons/hicolor/scalable/apps" "$DIST_DIR"
cp -a "$BUILD_DIR/client/nasbox" "$PACKAGE_DIR/usr/lib/nasbox"
cp "$ROOT_DIR/packaging/linux/nasbox.desktop" "$PACKAGE_DIR/usr/share/applications/nasbox.desktop"
cp "$ROOT_DIR/packaging/linux/nasbox.svg" "$PACKAGE_DIR/usr/share/icons/hicolor/scalable/apps/nasbox.svg"

cat > "$PACKAGE_DIR/usr/bin/nasbox" <<'EOF'
#!/usr/bin/env bash
export NASBOX_DISABLE_CLIENT_UPDATE=1
exec /usr/lib/nasbox/nasbox "$@"
EOF
chmod +x "$PACKAGE_DIR/usr/bin/nasbox"

cat > "$PACKAGE_DIR/DEBIAN/control" <<EOF
Package: nasbox-client
Version: $VERSION
Architecture: $ARCH
Maintainer: NASBox contributors
Depends: openssh-client, rsync
Recommends: inotify-tools
Section: utils
Priority: optional
Homepage: https://github.com/buzzqw/NASBox
Description: graphical NASBox synchronization client
 Synchronizes a local folder with a NAS using SSH and rsync.
EOF

OUTPUT="$DIST_DIR/nasbox-client_${VERSION}_${ARCH}.deb"
rm -f "$OUTPUT"
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT_DIR" log -1 --format=%ct 2>/dev/null || printf '0')}" \
    dpkg-deb --root-owner-group --build "$PACKAGE_DIR" "$OUTPUT"
printf '%s\n' "$OUTPUT"
