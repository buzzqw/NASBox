#!/usr/bin/env bash
# Installa il client NASBox Go per l'utente corrente (nessun permesso di root).
#
#   ./install.sh [binario]          installa (default: ./nasbox-go)
#   ./install.sh --migrate [bin]    installa e disattiva il client Python
#   ./install.sh --uninstall        rimuove binario, menu, avvio automatico
#
# Il client Python e quello Go non devono mai girare insieme: condividono
# configurazione e stato, e il lock di istanza li separa.
set -euo pipefail

BIN_DIR="$HOME/.local/bin"
APP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
ICON_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/scalable/apps"
AUTOSTART_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/autostart"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

migrate=false
if [[ "${1:-}" == "--uninstall" ]]; then
    "$BIN_DIR/nasbox-go" integrate --remove >/dev/null 2>&1 || true
    pkill -xf "$BIN_DIR/nasbox-go gui" 2>/dev/null || true
    rm -f "$BIN_DIR/nasbox-go" "$BIN_DIR/nasbox-go.bak" "$APP_DIR/nasbox-go.desktop" \
          "$AUTOSTART_DIR/nasbox-go.desktop" "$ICON_DIR/nasbox-go.svg"
    echo "NASBox Go rimosso (configurazione e stato in ~/.config/sync-daemon e ~/.local/state/sync-daemon conservati)."
    exit 0
fi
if [[ "${1:-}" == "--migrate" ]]; then
    migrate=true
    shift
fi
SOURCE="${1:-$HERE/nasbox-go}"
[[ -x "$SOURCE" ]] || { echo "binario non trovato o non eseguibile: $SOURCE" >&2; exit 1; }
"$SOURCE" version >/dev/null || { echo "il binario non parte: $SOURCE" >&2; exit 1; }

command -v ssh >/dev/null || echo "attenzione: 'ssh' non installato (necessario)." >&2
command -v rsync >/dev/null || echo "opzionale: 'rsync' non installato; serve solo con il backend rsync, non con chunks." >&2

if $migrate; then
    if systemctl --user list-unit-files sync-daemon-client.service >/dev/null 2>&1; then
        systemctl --user stop sync-daemon-client.service 2>/dev/null || true
        systemctl --user disable sync-daemon-client.service 2>/dev/null || true
        echo "client Python fermato e disattivato (sync-daemon-client.service)."
    fi
    pkill -f "nasbox/client/main.py" 2>/dev/null || true
fi

mkdir -p "$BIN_DIR" "$APP_DIR" "$ICON_DIR" "$AUTOSTART_DIR"
install -m 0755 "$SOURCE" "$BIN_DIR/nasbox-go.new"
mv -f "$BIN_DIR/nasbox-go.new" "$BIN_DIR/nasbox-go"
if [[ -f "$HERE/packaging/nasbox-go.svg" ]]; then
    install -m 0644 "$HERE/packaging/nasbox-go.svg" "$ICON_DIR/nasbox-go.svg"
fi
desktop() {
    cat <<DESKTOP
[Desktop Entry]
Type=Application
Name=NASBox
GenericName=Sincronizzazione file con il NAS
Comment=Il tuo cloud, sul tuo NAS
Exec=$BIN_DIR/nasbox-go gui
Icon=nasbox-go
Terminal=false
Categories=Network;FileTransfer;Utility;
StartupNotify=false
DESKTOP
}
desktop > "$APP_DIR/nasbox-go.desktop"
{ desktop; echo "X-GNOME-Autostart-enabled=true"; } > "$AUTOSTART_DIR/nasbox-go.desktop"
command -v update-desktop-database >/dev/null && update-desktop-database "$APP_DIR" >/dev/null 2>&1 || true

echo "NASBox $("$BIN_DIR/nasbox-go" version | awk '{print $NF}') installato in $BIN_DIR/nasbox-go"
echo "Avvio: menu applicazioni › NASBox, oppure: nasbox-go gui"
echo "Menu del file manager: Impostazioni › Generale › Integrazione con il desktop (o: nasbox-go integrate)"
