#!/usr/bin/env bash
# Installer/wizard for the NASBox graphical client (PyQt6). Bilingual (it/en) --
# see msg() below: every user-facing string goes through it instead of a
# literal echo, so the whole script (not just half of it) follows whichever
# language was picked at startup.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CANONICAL_ROOT="${HOME:?}/NASBox/sync-daemon"
CANONICAL_CLIENT_DIR="$CANONICAL_ROOT/client"
INSTALL_BIN="${HOME:?}/.local/bin"
DESKTOP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()  { printf "${GREEN}  OK${NC}  %s\n" "$*"; }
warn(){ printf "${YELLOW} WARN${NC} %s\n" "$*"; }
err() { printf "${RED}FAIL${NC} %s\n" "$*"; }
info(){ printf "${BLUE} INFO${NC} %s\n" "$*"; }

# --- language: guess from the environment, let the user override ---
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
            banner) echo "=== Installer client NASBox (GUI) ===" ;;
            dep_found) echo "%s trovato" ;;
            dep_missing) echo "%s NON TROVATO" ;;
            pyqt6_found) echo "PyQt6 trovato" ;;
            pyqt6_missing) echo "PyQt6 NON TROVATO" ;;
            pyqt6_hint_pacman) echo "Installalo con: sudo pacman -S python-pyqt6" ;;
            pyqt6_hint_generic) echo "Installa PyQt6 per la tua distro (Arch: python-pyqt6, Debian/Ubuntu: python3-pyqt6, oppure 'pip install --user PyQt6')" ;;
            missing_deps) echo "Dipendenze mancanti: %s" ;;
            install_and_rerun) echo "Installale e rilancia questo script." ;;
            inotify_found) echo "inotifywait trovato (rilevamento modifiche istantaneo)" ;;
            inotify_missing) echo "inotifywait NON trovato -- il client userà un controllo periodico ogni pochi secondi" ;;
            inotify_hint_pacman) echo "Consigliato: sudo pacman -S inotify-tools" ;;
            inotify_hint_generic) echo "Consigliato: installa il pacchetto 'inotify-tools' per la tua distro (Debian/Ubuntu: apt install inotify-tools)" ;;
            ssh_key_found) echo "Chiave SSH trovata: %s" ;;
            ssh_key_missing) echo "Nessuna chiave SSH trovata." ;;
            ssh_key_generate_prompt) echo "Generarne una ora (necessaria: il client gira in background e non può digitare password)? [Y/n] " ;;
            ssh_key_generated) echo "Chiave generata" ;;
            pubkey_header) echo "Chiave pubblica (verrà copiata automaticamente sul NAS/bastione se fornisci la password; questa è solo per copiarla a mano se preferisci):" ;;
            pubkey_nas_hint) echo "Sul NAS:      ssh-copy-id -p <porta_ssh_nas> <utente>@<host_nas>" ;;
            pubkey_bastion_hint) echo "Sul bastione: ssh-copy-id -p <porta_ssh_bastione> <utente>@<host_bastione>  (solo se lo usi)" ;;
            pubkey_warning) echo "Senza questo passaggio la sincronizzazione in background si bloccherà in attesa di una password." ;;
            sshpass_missing) echo "'sshpass' non trovato: non posso copiare la chiave automaticamente, dovrai farlo a mano (vedi sopra)." ;;
            sshpass_missing_hint_pacman) echo "Per abilitare la copia automatica: sudo pacman -S sshpass" ;;
            sshpass_missing_hint_generic) echo "Per abilitare la copia automatica installa il pacchetto 'sshpass' per la tua distro" ;;
            autocopy_prompt) echo "Accesso a chiave non ancora configurato. Copiare ora la chiave pubblica in automatico (serve la password)? [Y/n] " ;;
            nas_password_prompt) echo "Password SSH per %s@%s (non verrà salvata, solo usata ora): " ;;
            password_empty) echo "Password vuota, salto la copia automatica." ;;
            checking_password_ssh) echo "Verifico la password..." ;;
            password_ssh_failed) echo "Accesso con password fallito: controlla utente/password." ;;
            copying_key) echo "Password corretta, copio la chiave pubblica sul NAS..." ;;
            key_copied) echo "Chiave copiata: da ora l'accesso è solo a chiave, la password non serve più." ;;
            key_copy_failed) echo "Copia della chiave fallita nonostante la password corretta." ;;
            existing_config_found) echo "Trovata una configurazione esistente: %s" ;;
            redo_wizard_prompt) echo "Rifare la configurazione guidata (NAS + cartella NASBox)? [y/N] " ;;
            wizard_header) echo "--- Configurazione guidata ---" ;;
            local_root_prompt) echo "Cartella NASBox su questo PC [%s]: " ;;
            local_root_ready) echo "Cartella pronta: %s" ;;
            nas_host_prompt) echo "Host/IP del NAS in rete locale (es. nas.local o 192.168.1.50): " ;;
            nas_user_prompt) echo "Utente SSH sul NAS: " ;;
            nas_port_prompt) echo "Porta SSH sul NAS [22]: " ;;
            host_user_required) echo "Host e utente sono obbligatori." ;;
            checking_ssh) echo "Verifico l'accesso SSH..." ;;
            ssh_ok) echo "Accesso SSH al NAS riuscito." ;;
            ssh_failed) echo "Accesso SSH fallito con questi parametri." ;;
            ssh_failed_hint) echo "Controlla che il NAS sia acceso/raggiungibile e che la chiave pubblica sia stata copiata (vedi sopra: ssh-copy-id)." ;;
            retry_prompt) echo "Riprovare con altri valori? [Y/n] " ;;
            continue_anyway) echo "Continuo comunque con questi valori (l'app riproverà da sola più avanti)." ;;
            has_wan_prompt) echo "Il NAS è raggiungibile direttamente da fuori casa (host WAN diretto)? [y/N] " ;;
            wan_host_prompt) echo "Host WAN del NAS (es. nas.miodominio.com): " ;;
            has_jump_prompt) echo "Ti serve un bastione/ponte SSH per raggiungerlo da fuori? [y/N] " ;;
            jump_host_prompt) echo "Host del bastione (es. bastion.example.net): " ;;
            jump_port_prompt) echo "Porta SSH sul bastione [22]: " ;;
            jump_user_prompt) echo "Utente SSH sul bastione [vuoto = %s]: " ;;
            checking_bastion_ssh) echo "Verifico l'accesso SSH al bastione..." ;;
            bastion_ssh_ok) echo "Accesso SSH al bastione riuscito." ;;
            bastion_ssh_failed) echo "Accesso SSH al bastione fallito -- controlla chiave/ssh-copy-id, puoi sistemarlo dopo dal tab Impostazioni." ;;
            remote_prefix_prompt) echo "Cartella NASBox sul NAS [%s]: " ;;
            remote_script_prompt) echo "Percorso di sync-daemon-server.sh sul NAS, se già installato [invio per saltare, si può fare dopo]: " ;;
            wizard_done) echo "Configurazione guidata completata." ;;
            canonical_client_ready) echo "Client copiato nella directory canonica: %s" ;;
            launcher_installed) echo "Launcher installato in %s" ;;
            desktop_entry_installed) echo "Voce menu applicazioni installata: %s" ;;
            systemd_unit_installed) echo "Unità systemd installata: %s" ;;
            service_enabled) echo "Servizio abilitato all'avvio (systemctl --user enable)" ;;
            service_start_hint) echo "Avvialo ora con:  systemctl --user start sync-daemon-client.service" ;;
            service_logs_hint) echo "Log:               journalctl --user -u sync-daemon-client.service -f" ;;
            systemctl_not_found) echo "systemctl non trovato: salto l'abilitazione del servizio" ;;
            systemd_unavailable) echo "Il servizio systemd utente non è disponibile in questa sessione: launcher e voce di menu restano installati." ;;
            invalid_port) echo "La porta deve essere un numero tra 1 e 65535." ;;
            done_header) echo "=== Fatto ===" ;;
            run_with) echo "Avvialo con:  %s" ;;
            find_in_launcher) echo "Oppure trova 'NASBox' nel tuo menu applicazioni." ;;
            final_note) echo "Al primo avvio ti chiede dove mettere la cartella NASBox locale, poi configura la connessione al NAS dall'app stessa (tab 'Impostazioni'). La configurazione vive in %s." ;;
            *) echo "$key" ;;
        esac
    else
        case "$key" in
            banner) echo "=== NASBox client (GUI) installer ===" ;;
            dep_found) echo "%s found" ;;
            dep_missing) echo "%s NOT FOUND" ;;
            pyqt6_found) echo "PyQt6 found" ;;
            pyqt6_missing) echo "PyQt6 NOT FOUND" ;;
            pyqt6_hint_pacman) echo "Install it with: sudo pacman -S python-pyqt6" ;;
            pyqt6_hint_generic) echo "Install PyQt6 for your distro (Arch: python-pyqt6, Debian/Ubuntu: python3-pyqt6, or 'pip install --user PyQt6')" ;;
            missing_deps) echo "Missing dependencies: %s" ;;
            install_and_rerun) echo "Install them and re-run this script." ;;
            inotify_found) echo "inotifywait found (instant local change detection)" ;;
            inotify_missing) echo "inotifywait NOT found -- the client will fall back to polling every few seconds" ;;
            inotify_hint_pacman) echo "Recommended: sudo pacman -S inotify-tools" ;;
            inotify_hint_generic) echo "Recommended: install the 'inotify-tools' package for your distro (Debian/Ubuntu: apt install inotify-tools)" ;;
            ssh_key_found) echo "SSH key found: %s" ;;
            ssh_key_missing) echo "No SSH key found." ;;
            ssh_key_generate_prompt) echo "Generate one now (needed: the client runs in the background and can't type a password)? [Y/n] " ;;
            ssh_key_generated) echo "Key generated" ;;
            pubkey_header) echo "Public key (this will be copied to the NAS/bastion automatically if you give the password; this is only here so you can copy it by hand if you'd rather):" ;;
            pubkey_nas_hint) echo "On the NAS:     ssh-copy-id -p <nas_ssh_port> <user>@<nas_host>" ;;
            pubkey_bastion_hint) echo "On the bastion: ssh-copy-id -p <bastion_ssh_port> <user>@<bastion_host>  (only if you use one)" ;;
            pubkey_warning) echo "Without this step, background syncing will hang waiting for a password." ;;
            sshpass_missing) echo "'sshpass' not found: can't copy the key automatically, you'll need to do it by hand (see above)." ;;
            sshpass_missing_hint_pacman) echo "To enable automatic copying: sudo pacman -S sshpass" ;;
            sshpass_missing_hint_generic) echo "To enable automatic copying, install the 'sshpass' package for your distro" ;;
            autocopy_prompt) echo "Key-based access isn't set up yet. Copy the public key automatically now (needs the password)? [Y/n] " ;;
            nas_password_prompt) echo "SSH password for %s@%s (never saved, used only now): " ;;
            password_empty) echo "Empty password, skipping automatic copy." ;;
            checking_password_ssh) echo "Checking the password..." ;;
            password_ssh_failed) echo "Password login failed: check the user/password." ;;
            copying_key) echo "Password correct, copying the public key to the NAS..." ;;
            key_copied) echo "Key copied: access is now key-only, the password is no longer needed." ;;
            key_copy_failed) echo "Key copy failed even though the password was correct." ;;
            existing_config_found) echo "Found an existing configuration: %s" ;;
            redo_wizard_prompt) echo "Redo the guided setup (NAS + NASBox folder)? [y/N] " ;;
            wizard_header) echo "--- Guided setup ---" ;;
            local_root_prompt) echo "NASBox folder on this PC [%s]: " ;;
            local_root_ready) echo "Folder ready: %s" ;;
            nas_host_prompt) echo "NAS host/IP on the local network (e.g. nas.local or 192.168.1.50): " ;;
            nas_user_prompt) echo "SSH user on the NAS: " ;;
            nas_port_prompt) echo "SSH port on the NAS [22]: " ;;
            host_user_required) echo "Host and user are required." ;;
            checking_ssh) echo "Checking SSH access..." ;;
            ssh_ok) echo "SSH access to the NAS succeeded." ;;
            ssh_failed) echo "SSH access failed with these parameters." ;;
            ssh_failed_hint) echo "Check that the NAS is on/reachable and that the public key was copied (see above: ssh-copy-id)." ;;
            retry_prompt) echo "Try again with different values? [Y/n] " ;;
            continue_anyway) echo "Continuing anyway with these values (the app will retry on its own later)." ;;
            has_wan_prompt) echo "Is the NAS directly reachable from outside your home (direct WAN host)? [y/N] " ;;
            wan_host_prompt) echo "NAS WAN host (e.g. nas.mydomain.com): " ;;
            has_jump_prompt) echo "Do you need an SSH bastion/bridge to reach it from outside? [y/N] " ;;
            jump_host_prompt) echo "Bastion host (e.g. bastion.example.net): " ;;
            jump_port_prompt) echo "SSH port on the bastion [22]: " ;;
            jump_user_prompt) echo "SSH user on the bastion [blank = %s]: " ;;
            checking_bastion_ssh) echo "Checking SSH access to the bastion..." ;;
            bastion_ssh_ok) echo "SSH access to the bastion succeeded." ;;
            bastion_ssh_failed) echo "SSH access to the bastion failed -- check the key/ssh-copy-id, you can fix it later in the Settings tab." ;;
            remote_prefix_prompt) echo "NASBox folder on the NAS [%s]: " ;;
            remote_script_prompt) echo "Path to sync-daemon-server.sh on the NAS, if already installed [Enter to skip, can be done later]: " ;;
            wizard_done) echo "Guided setup complete." ;;
            canonical_client_ready) echo "Client copied to the canonical directory: %s" ;;
            launcher_installed) echo "Launcher installed to %s" ;;
            desktop_entry_installed) echo "Desktop entry installed: %s" ;;
            systemd_unit_installed) echo "Systemd unit installed: %s" ;;
            service_enabled) echo "Service enabled for autostart (systemctl --user enable)" ;;
            service_start_hint) echo "Start it now with:  systemctl --user start sync-daemon-client.service" ;;
            service_logs_hint) echo "Logs:               journalctl --user -u sync-daemon-client.service -f" ;;
            systemctl_not_found) echo "systemctl not found: skipping service enablement" ;;
            systemd_unavailable) echo "The systemd user service is unavailable in this session: the launcher and desktop entry remain installed." ;;
            invalid_port) echo "The port must be a number between 1 and 65535." ;;
            done_header) echo "=== Done ===" ;;
            run_with) echo "Run it with:  %s" ;;
            find_in_launcher) echo "Or find 'NASBox' in your application launcher." ;;
            final_note) echo "First run asks where to put the local NASBox folder, then configure the NAS connection from the app itself (tab 'Settings'). Config lives at %s." ;;
            *) echo "$key" ;;
        esac
    fi
}
m() { local key="$1"; shift; printf "$(msg "$key")" "$@"; }
valid_port() { [[ "$1" =~ ^[1-9][0-9]*$ ]] && (( "$1" <= 65535 )); }

# --- ensure key-based SSH access to $user@$host:$port, offering to configure it
#     automatically (password + ssh-copy-id) the first time it isn't already set
#     up. Returns 0 once key-based access works (whether it already did, or this
#     just set it up), 1 otherwise -- the caller decides what to do next (retry,
#     continue anyway, etc), same as before this existed.
#
#     The password only ever lives in a local shell variable for the few seconds
#     it takes to hand it to sshpass via the SSHPASS env var (not -p, which some
#     `ps` configurations can expose to other users on the same host); it is
#     never written to disk, logged, or passed to the NASBox client itself --
#     the client only ever authenticates with the SSH key this installer manages.
ensure_ssh_access() {
    local host="$1" port="$2" user="$3"

    if ssh -p "$port" -o BatchMode=yes -o ConnectTimeout=6 \
            -o StrictHostKeyChecking=accept-new "$user@$host" true 2>/dev/null; then
        return 0
    fi

    if ! command -v sshpass &>/dev/null; then
        warn "$(msg sshpass_missing)"
        if command -v pacman &>/dev/null; then
            info "$(msg sshpass_missing_hint_pacman)"
        else
            info "$(msg sshpass_missing_hint_generic)"
        fi
        return 1
    fi

    local autocopy
    read -r -p "$(msg autocopy_prompt)" autocopy
    [[ -z "$autocopy" || "$autocopy" =~ ^[Yy]$ ]] || return 1

    local password
    read -r -s -p "$(m nas_password_prompt "$user" "$host")" password
    echo ""
    if [[ -z "$password" ]]; then
        warn "$(msg password_empty)"
        return 1
    fi

    info "$(msg checking_password_ssh)"
    if ! SSHPASS="$password" sshpass -e ssh -p "$port" -o ConnectTimeout=6 \
            -o StrictHostKeyChecking=accept-new "$user@$host" true 2>/dev/null; then
        unset password
        err "$(msg password_ssh_failed)"
        return 1
    fi

    info "$(msg copying_key)"
    local copy_ok=1
    SSHPASS="$password" sshpass -e ssh-copy-id -p "$port" -i "${SSH_KEY}.pub" \
        -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new "$user@$host" \
        &>/dev/null || copy_ok=0
    unset password

    if [[ "$copy_ok" -ne 1 ]]; then
        err "$(msg key_copy_failed)"
        return 1
    fi
    ok "$(msg key_copied)"

    ssh -p "$port" -o BatchMode=yes -o ConnectTimeout=6 \
            -o StrictHostKeyChecking=accept-new "$user@$host" true 2>/dev/null
}

echo ""
echo "$(msg banner)"
echo ""

# --- deps ---
MISSING=()
for dep in rsync ssh python3; do
    command -v "$dep" &>/dev/null && ok "$(m dep_found "$dep")" || { MISSING+=("$dep"); err "$(m dep_missing "$dep")"; }
done

if command -v python3 &>/dev/null && python3 -c "from PyQt6 import QtWidgets" &>/dev/null; then
    ok "$(msg pyqt6_found)"
else
    err "$(msg pyqt6_missing)"
    if command -v pacman &>/dev/null; then
        warn "$(msg pyqt6_hint_pacman)"
    else
        warn "$(msg pyqt6_hint_generic)"
    fi
    MISSING+=("PyQt6")
fi

if [[ ${#MISSING[@]} -gt 0 ]]; then
    err "$(m missing_deps "${MISSING[*]}")"
    echo "$(msg install_and_rerun)"
    exit 1
fi

# Keep the runnable client in one stable location. This also makes the launcher
# independent from the directory containing the installer or an update bundle.
if [[ "$SCRIPT_DIR" != "$CANONICAL_CLIENT_DIR" ]]; then
    mkdir -p "$CANONICAL_CLIENT_DIR"
    rsync -a --delete --exclude='.update' "$SCRIPT_DIR/" "$CANONICAL_CLIENT_DIR/"
    UPDATE_SOURCE_DIR="$SCRIPT_DIR/../client-update"
    if [[ -d "$UPDATE_SOURCE_DIR" && "$UPDATE_SOURCE_DIR" != "$CANONICAL_ROOT/client-update" ]]; then
        mkdir -p "$CANONICAL_ROOT/client-update"
        rsync -a --delete "$UPDATE_SOURCE_DIR/" "$CANONICAL_ROOT/client-update/"
    fi
    ok "$(m canonical_client_ready "$CANONICAL_CLIENT_DIR")"
fi

# inotifywait (inotify-tools) is optional -- the client falls back to polling the
# filesystem every couple seconds without it, which still works but is slower to
# react and, critically, has to fully re-scan the whole tree to notice changes.
# Not required, so this doesn't add to MISSING/block the install.
if command -v inotifywait &>/dev/null; then
    ok "$(msg inotify_found)"
else
    warn "$(msg inotify_missing)"
    if command -v pacman &>/dev/null; then
        info "$(msg inotify_hint_pacman)"
    else
        info "$(msg inotify_hint_generic)"
    fi
fi

echo ""

# --- SSH key (needed for unattended, password-less auth to the NAS and,
#     if configured, to a jump-host bastion) ---
SSH_KEY="$HOME/.ssh/id_ed25519"
if [[ -f "$SSH_KEY" ]]; then
    ok "$(m ssh_key_found "$SSH_KEY")"
else
    warn "$(msg ssh_key_missing)"
    read -r -p "$(msg ssh_key_generate_prompt)" answer
    if [[ -z "$answer" || "$answer" =~ ^[Yy]$ ]]; then
        mkdir -p "$(dirname "$SSH_KEY")"
        chmod 700 "$(dirname "$SSH_KEY")"
        ssh-keygen -t ed25519 -f "$SSH_KEY" -N "" -C "nasbox@$(hostname)"
        ok "$(msg ssh_key_generated)"
    fi
fi

if [[ -f "$SSH_KEY.pub" ]]; then
    echo ""
    echo "$(msg pubkey_header)"
    echo ""
    cat "$SSH_KEY.pub"
    echo ""
    warn "$(msg pubkey_nas_hint)"
    warn "$(msg pubkey_bastion_hint)"
    echo "$(msg pubkey_warning)"
fi

echo ""

# --- guided setup wizard: NAS connection + local NASBox folder ---
#
# Writes straight into client.json via the same Config class the app itself
# uses (so schema/defaults never drift between this script and sync_client/
# config.py) -- this is the one and only place that asks "where's your NAS"
# and "where do you want the NASBox folder", so a fresh install ends with a
# client that's actually ready to sync, not just installed.
CONFIG_FILE_PATH="$(python3 -c '
import sys
sys.path.insert(0, sys.argv[1])
from sync_client import paths
print(paths.config_file())
' "$SCRIPT_DIR")"

RUN_WIZARD=1
if [[ -f "$CONFIG_FILE_PATH" ]]; then
    echo "$(m existing_config_found "$CONFIG_FILE_PATH")"
    read -r -p "$(msg redo_wizard_prompt)" answer
    [[ "$answer" =~ ^[Yy]$ ]] || RUN_WIZARD=0
fi

if [[ "$RUN_WIZARD" -eq 1 ]]; then
    echo ""
    echo "$(msg wizard_header)"
    echo ""

    # 1) Where the local NASBox folder goes on this PC
    DEFAULT_LOCAL_ROOT="$HOME/NASBox"
    read -r -p "$(m local_root_prompt "$DEFAULT_LOCAL_ROOT")" NASBOX_LOCAL_ROOT
    NASBOX_LOCAL_ROOT="${NASBOX_LOCAL_ROOT:-$DEFAULT_LOCAL_ROOT}"
    mkdir -p "$NASBOX_LOCAL_ROOT" && ok "$(m local_root_ready "$NASBOX_LOCAL_ROOT")"

    # 2) The NAS itself, with a live SSH check so a typo is caught now, not
    #    discovered later staring at "NAS non raggiungibile" in the app.
    NASBOX_NAS_LAN="" NASBOX_NAS_USER="" NASBOX_SSH_PORT="22"
    while true; do
        echo ""
        read -r -p "$(msg nas_host_prompt)" NASBOX_NAS_LAN
        read -r -p "$(msg nas_user_prompt)" NASBOX_NAS_USER
        read -r -p "$(msg nas_port_prompt)" NASBOX_SSH_PORT
        NASBOX_SSH_PORT="${NASBOX_SSH_PORT:-22}"

        if [[ -z "$NASBOX_NAS_LAN" || -z "$NASBOX_NAS_USER" ]]; then
            warn "$(msg host_user_required)"
            continue
        fi
        if ! valid_port "$NASBOX_SSH_PORT"; then
            warn "$(msg invalid_port)"
            continue
        fi

        info "$(msg checking_ssh)"
        if ensure_ssh_access "$NASBOX_NAS_LAN" "$NASBOX_SSH_PORT" "$NASBOX_NAS_USER"; then
            ok "$(msg ssh_ok)"
            break
        fi
        err "$(msg ssh_failed)"
        warn "$(msg ssh_failed_hint)"
        read -r -p "$(msg retry_prompt)" retry
        if [[ "$retry" =~ ^[Nn]$ ]]; then
            warn "$(msg continue_anyway)"
            break
        fi
    done

    # 3) Optional bastion, only if the NAS isn't directly reachable from outside
    NASBOX_NAS_WAN="" NASBOX_JUMP_HOST="" NASBOX_JUMP_PORT="22" NASBOX_JUMP_USER=""
    echo ""
    read -r -p "$(msg has_wan_prompt)" has_wan
    if [[ "$has_wan" =~ ^[Yy]$ ]]; then
        read -r -p "$(msg wan_host_prompt)" NASBOX_NAS_WAN
    else
        read -r -p "$(msg has_jump_prompt)" has_jump
        if [[ "$has_jump" =~ ^[Yy]$ ]]; then
            read -r -p "$(msg jump_host_prompt)" NASBOX_JUMP_HOST
            read -r -p "$(msg jump_port_prompt)" NASBOX_JUMP_PORT
            NASBOX_JUMP_PORT="${NASBOX_JUMP_PORT:-22}"
            while ! valid_port "$NASBOX_JUMP_PORT"; do
                warn "$(msg invalid_port)"
                read -r -p "$(msg jump_port_prompt)" NASBOX_JUMP_PORT
                NASBOX_JUMP_PORT="${NASBOX_JUMP_PORT:-22}"
            done
            read -r -p "$(m jump_user_prompt "$NASBOX_NAS_USER")" NASBOX_JUMP_USER
            if [[ -n "$NASBOX_JUMP_HOST" ]]; then
                info "$(msg checking_bastion_ssh)"
                jump_user_test="${NASBOX_JUMP_USER:-$NASBOX_NAS_USER}"
                if ensure_ssh_access "$NASBOX_JUMP_HOST" "$NASBOX_JUMP_PORT" "$jump_user_test"; then
                    ok "$(msg bastion_ssh_ok)"
                else
                    warn "$(msg bastion_ssh_failed)"
                fi
            fi
        fi
    fi

    # 4) Where the NASBox folder lives on the NAS side
    echo ""
    DEFAULT_REMOTE_PREFIX="/volume1/NASBox"
    read -r -p "$(m remote_prefix_prompt "$DEFAULT_REMOTE_PREFIX")" NASBOX_REMOTE_PREFIX
    NASBOX_REMOTE_PREFIX="${NASBOX_REMOTE_PREFIX:-$DEFAULT_REMOTE_PREFIX}"

    read -r -p "$(msg remote_script_prompt)" NASBOX_REMOTE_SCRIPT

    export NASBOX_LOCAL_ROOT NASBOX_NAS_LAN NASBOX_NAS_WAN NASBOX_NAS_USER NASBOX_SSH_PORT \
           NASBOX_JUMP_HOST NASBOX_JUMP_PORT NASBOX_JUMP_USER NASBOX_REMOTE_PREFIX NASBOX_REMOTE_SCRIPT NASBOX_LANG_CODE="$LANG_CODE"

    python3 - "$SCRIPT_DIR" <<'PYEOF'
import os, sys
sys.path.insert(0, sys.argv[1])
from sync_client import config as config_module

cfg = config_module.shared()
values = {
    "local_root": os.environ.get("NASBOX_LOCAL_ROOT", ""),
    "nas_lan": os.environ.get("NASBOX_NAS_LAN", ""),
    "nas_wan": os.environ.get("NASBOX_NAS_WAN", ""),
    "nas_user": os.environ.get("NASBOX_NAS_USER", ""),
    "ssh_port": int(os.environ.get("NASBOX_SSH_PORT") or 22),
    "jump_host": os.environ.get("NASBOX_JUMP_HOST", ""),
    "jump_port": int(os.environ.get("NASBOX_JUMP_PORT") or 22),
    "jump_user": os.environ.get("NASBOX_JUMP_USER", ""),
    "remote_prefix": os.environ.get("NASBOX_REMOTE_PREFIX", ""),
    "remote_server_script": os.environ.get("NASBOX_REMOTE_SCRIPT", ""),
    "language": os.environ.get("NASBOX_LANG_CODE", "auto"),
}
for key, value in values.items():
    cfg.set(key, value, persist=False)
cfg.save()
print("Configurazione salvata in / Config saved to", config_module.paths.config_file())
PYEOF
    ok "$(msg wizard_done)"
fi

echo ""

# --- launcher script ---
mkdir -p "$INSTALL_BIN"
LAUNCHER="$INSTALL_BIN/sync-daemon-gui"
cat > "$LAUNCHER" <<EOF
#!/usr/bin/env bash
exec python3 "$CANONICAL_CLIENT_DIR/main.py" "\$@"
EOF
chmod +x "$LAUNCHER"
ok "$(m launcher_installed "$LAUNCHER")"

# --- desktop entry ---
mkdir -p "$DESKTOP_DIR"
cat > "$DESKTOP_DIR/sync-daemon-client.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=NASBox
Comment=NASBox
Exec="$LAUNCHER"
Icon=folder-sync
Terminal=false
Categories=Utility;Network;
EOF
ok "$(m desktop_entry_installed "$DESKTOP_DIR/sync-daemon-client.desktop")"

# --- systemd user service (autostart) ---
SYSTEMD_USER_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
mkdir -p "$SYSTEMD_USER_DIR"
cat > "$SYSTEMD_USER_DIR/sync-daemon-client.service" <<EOF
[Unit]
Description=NASBox client
After=graphical-session.target

[Service]
Type=simple
ExecStart="$LAUNCHER"
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
ok "$(m systemd_unit_installed "$SYSTEMD_USER_DIR/sync-daemon-client.service")"

if command -v systemctl &>/dev/null && systemctl --user show-environment &>/dev/null; then
    was_active=0
    systemctl --user is-active --quiet sync-daemon-client.service && was_active=1
    if systemctl --user daemon-reload && systemctl --user enable sync-daemon-client.service; then
        if [[ "$was_active" -eq 1 ]]; then
            systemctl --user restart sync-daemon-client.service || warn "$(msg systemd_unavailable)"
        fi
        ok "$(msg service_enabled)"
        info "$(msg service_start_hint)"
        info "$(msg service_logs_hint)"
    else
        warn "$(msg systemd_unavailable)"
    fi
else
    warn "$(msg systemd_unavailable)"
fi

echo ""
echo "$(msg done_header)"
echo "$(m run_with "$LAUNCHER")"
echo "$(msg find_in_launcher)"
echo "$(m final_note "\$XDG_CONFIG_HOME/sync-daemon/client.json")"
