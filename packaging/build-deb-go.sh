#!/usr/bin/env bash
# Costruisce il pacchetto .deb del client NASBox Go.
#   packaging/build-deb-go.sh <binario nasbox-go> [arch]
# Non richiede dpkg-deb: il formato .deb (ar con debian-binary, control.tar.gz
# e data.tar.gz) viene assemblato con ar e tar, quindi funziona anche su
# distribuzioni non Debian. In un'installazione da pacchetto il binario sta in
# /usr/bin e si aggiorna con il gestore pacchetti, non con l'auto-updater.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY="${1:?uso: build-deb-go.sh <binario> [arch]}"
ARCH="${2:-amd64}"
VERSION="$("$BINARY" version | awk '{print $NF}')"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PKG="$WORK/pkg"
mkdir -p "$PKG/usr/bin" "$PKG/usr/share/applications" "$PKG/usr/share/icons/hicolor/scalable/apps" \
         "$PKG/etc/xdg/autostart" "$PKG/usr/share/doc/nasbox-go"
install -m 0755 "$BINARY" "$PKG/usr/bin/nasbox-go"
install -m 0644 "$ROOT/client-go/packaging/nasbox-go.svg" "$PKG/usr/share/icons/hicolor/scalable/apps/nasbox-go.svg"
cat > "$PKG/usr/share/applications/nasbox-go.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=NASBox
GenericName=Sincronizzazione file con il NAS
Comment=Il tuo cloud, sul tuo NAS
Exec=/usr/bin/nasbox-go gui
Icon=nasbox-go
Terminal=false
Categories=Network;FileTransfer;Utility;
DESKTOP
{ cat "$PKG/usr/share/applications/nasbox-go.desktop"; echo "X-GNOME-Autostart-enabled=true"; } \
    > "$PKG/etc/xdg/autostart/nasbox-go.desktop"
install -m 0644 "$ROOT/CHANGELOG.md" "$PKG/usr/share/doc/nasbox-go/changelog"
mkdir -p "$WORK/control"
SIZE_KB=$(du -sk "$PKG" | cut -f1)
cat > "$WORK/control/control" <<CONTROL
Package: nasbox-go
Version: $VERSION
Architecture: $ARCH
Maintainer: NASBox contributors
Installed-Size: $SIZE_KB
Depends: openssh-client, rsync (>= 3.1)
Recommends: inotify-tools
Conflicts: nasbox-client
Section: net
Priority: optional
Homepage: https://github.com/buzzqw/NASBox
Description: NASBox, il tuo cloud sul tuo NAS (client Go)
 Sincronizza una cartella locale con un NAS via SSH e rsync, con storico
 delle versioni, gestione dei conflitti e interfaccia per X11 e Wayland.
CONTROL
(cd "$PKG" && find . -type f ! -path ./DEBIAN -printf '%P\n' | sort | xargs md5sum) > "$WORK/control/md5sums"
EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || date +%s)}"
TAR=(tar --owner=0 --group=0 --numeric-owner --mtime="@$EPOCH" --sort=name -czf)
"${TAR[@]}" "$WORK/control.tar.gz" -C "$WORK/control" .
"${TAR[@]}" "$WORK/data.tar.gz" -C "$PKG" .
printf '2.0\n' > "$WORK/debian-binary"
mkdir -p "$ROOT/dist"
OUT="$ROOT/dist/nasbox-go_${VERSION}_${ARCH}.deb"
rm -f "$OUT"
(cd "$WORK" && ar rc "$OUT" debian-binary control.tar.gz data.tar.gz)
echo "$OUT"
