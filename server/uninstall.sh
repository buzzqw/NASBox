#!/usr/bin/env bash
# Stops the daemon and removes whatever system-level boot hook install.sh set
# up (systemd unit or Synology rc.d script). Does not delete server.conf,
# logs, or this folder -- remove the folder yourself if you want the package
# gone entirely. Bilingual (it/en) -- see msg() below.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SYSTEMD_UNIT="/etc/systemd/system/sync-daemon-server.service"
RC_SCRIPT="/usr/local/etc/rc.d/sync-daemon-server.sh"

case "${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}" in
    it*|IT*) LANG_CODE="it" ;;
    *)       LANG_CODE="en" ;;
esac
read -r -p "Lingua / Language [it/en] ($LANG_CODE): " lang_choice
case "$lang_choice" in
    it|IT) LANG_CODE="it" ;;
    en|EN) LANG_CODE="en" ;;
esac

msg() {
    local key="$1"
    if [[ "$LANG_CODE" == "it" ]]; then
        case "$key" in
            need_root) echo "Il servizio è registrato in systemd: rilancia questo script con sudo per rimuoverlo." ;;
            need_root_rc) echo "L'hook DSM è un servizio di sistema: rilancia questo script con sudo per rimuoverlo." ;;
            systemd_removed) echo "Rimosso il servizio di sistema systemd." ;;
            rc_removed) echo "Rimosso l'hook di avvio al boot DSM: %s" ;;
            stopped) echo "Demone fermato. Per rimuovere completamente il pacchetto, cancella questa cartella:" ;;
            *) echo "$key" ;;
        esac
    else
        case "$key" in
            need_root) echo "The service is registered with systemd: re-run this script with sudo to remove it." ;;
            need_root_rc) echo "The DSM hook is a system service: re-run this script with sudo to remove it." ;;
            systemd_removed) echo "Removed the systemd system service." ;;
            rc_removed) echo "Removed the DSM boot-startup hook: %s" ;;
            stopped) echo "Daemon stopped. To remove the package entirely, delete this folder:" ;;
            *) echo "$key" ;;
        esac
    fi
}
m() { local key="$1"; shift; printf "$(msg "$key")" "$@"; }

if [[ -f "$SYSTEMD_UNIT" ]] && command -v systemctl &>/dev/null; then
    if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
        echo "$(msg need_root)"
        exit 1
    fi
    systemctl disable --now sync-daemon-server.service 2>/dev/null || true
    rm -f "$SYSTEMD_UNIT"
    systemctl daemon-reload
    echo "$(msg systemd_removed)"
elif [[ -f "$RC_SCRIPT" ]]; then
    if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
        echo "$(msg need_root_rc)"
        exit 1
    fi
    "$RC_SCRIPT" stop
    rm -f "$RC_SCRIPT"
    echo "$(m rc_removed "$RC_SCRIPT")"
else
    "$SCRIPT_DIR/sync-daemon-server.sh" --stop
fi

echo ""
echo "$(msg stopped)"
echo "  $SCRIPT_DIR"
