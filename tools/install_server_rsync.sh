#!/usr/bin/env bash
# Installa un rsync moderno *privato* del demone, senza toccare il sistema.
#
# Su un NAS Synology il rsync di sistema è la 3.1.2 (aarch64, setuid root). Ogni
# opzione introdotta dopo quella versione fa fallire l'intero trasferimento,
# perché rsync inoltra le opzioni al lato remoto. Questo script scarica un rsync
# più recente dal repository Entware (bin.entware.net, pacchetti firmati con
# sha256 e fissati qui sotto), lo mette in server/bin/ con le sue librerie e crea
# il wrapper server/bin/nasbox-rsync che il demone dichiara ai client.
#
# Il demone lo usa solo se esiste e funziona: se manca, o se qualcosa non torna,
# le capacità non lo dichiarano e i client tornano al rsync di sistema. Per
# rimuoverlo: ./tools/install_server_rsync.sh --remove
#
# Va eseguito SUL NAS, dalla radice del repository.
set -uo pipefail

BASE="https://bin.entware.net/aarch64-k3.10"
DEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/server/bin"
PINS="
rsync            3.5.1-1   c76f22469ebd23bd0725d0f16cf19592a6ee053cc8a595857df722ba46e95e5e
libpopt          1.19-1    5a90d36d916a28966bf8ba62089b11e55a3508300b07420b6ffe874ba08abfb0
zlib             2.3.3-2   ad404aaa66e10120982dd8addb164faa19e98e87fa9c39f2be3c69dd306d9ef5
libattr          2.6.0-1   41de6297091e3afc658aaf414de9142d5c0bdf0e08b637632abb19d7d7f18d2a
libacl           2.4.0-1   6e8be8a9cb2fe4896ec4664cb7df93260962af9d83a546c792a5ffa64a8f27b3
libzstd          1.5.7-1   ddf3027b9b54b0739fa0410e5965f71a9c144bbd39b4dc1fa07e84aeebb84735
libiconv-full    1.18-1    c3cff4536da2a6d7328e90b6594fbc146087cf44cfda7a3917bcc8157761ac83
"

die() { printf 'errore: %s\n' "$*" >&2; exit 1; }

if [[ "${1:-}" == "--remove" ]]; then
    rm -rf "$DEST_DIR"
    printf 'rsync privato rimosso: %s\n' "$DEST_DIR"
    printf 'Riavvia il demone per smettere di dichiararlo: server/sync-daemon-server.sh --restart\n'
    exit 0
fi

command -v curl >/dev/null 2>&1 || die "curl non disponibile"
command -v tar >/dev/null 2>&1 || die "tar non disponibile"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum non disponibile"

# Work on the volume, not /tmp: on DSM /tmp is mounted noexec, so nothing
# extracted there can be run (and the -x check below would be pointless).
work="$(mktemp -d "$(dirname "$DEST_DIR")/.rsync-install.XXXXXX")" || die "mktemp fallito"
trap 'rm -rf "$work"' EXIT

printf '%s\n' "$PINS" | while read -r name version sha; do
    [[ -n "${name:-}" ]] || continue
    file="${name}_${version}_aarch64-3.10.ipk"
    printf 'scarico %s\n' "$file"
    curl -fsS --max-time 120 -o "$work/$file" "$BASE/$file" || die "download fallito: $file"
    actual="$(sha256sum "$work/$file" | cut -d' ' -f1)"
    [[ "$actual" == "$sha" ]] || die "sha256 non corrispondente per $file (atteso $sha, trovato $actual)"
done || exit 1

mkdir -p "$work/extract"
for file in "$work"/*.ipk; do
    # .ipk = tar.gz con dentro debian-binary, control.tar.gz e data.tar.gz.
    # Extract the whole container: GNU tar 1.34 on DSM refuses to match the
    # member by a name that lacks the archive's "./" prefix.
    tar xzf "$file" -C "$work/extract" || die "pacchetto non valido: $file"
    tar xzf "$work/extract/data.tar.gz" -C "$work/extract" || die "data.tar.gz non valido: $file"
    rm -f "$work/extract/data.tar.gz" "$work/extract/control.tar.gz" "$work/extract/debian-binary"
done

[[ -f "$work/extract/opt/bin/rsync" ]] || die "binario rsync non trovato nel pacchetto"

rm -rf "$DEST_DIR"
mkdir -p "$DEST_DIR/lib"
cp "$work/extract/opt/bin/rsync" "$DEST_DIR/rsync" || die "copia del binario fallita"
chmod 755 "$DEST_DIR/rsync"
for lib in "$work"/extract/opt/lib/*.so*; do
    [[ -e "$lib" ]] || continue
    cp -a "$lib" "$DEST_DIR/lib/" || die "copia della libreria fallita: $lib"
done

# Wrapper: rsync inoltra il comando remoto così com'è, quindi il client usa
# questo script come --rsync-path e le librerie private vengono trovate qui.
cat > "$DEST_DIR/nasbox-rsync" <<'WRAPPER'
#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
LD_LIBRARY_PATH="$dir/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export LD_LIBRARY_PATH
exec "$dir/rsync" "$@"
WRAPPER
chmod 755 "$DEST_DIR/nasbox-rsync"

version="$("$DEST_DIR/nasbox-rsync" --version 2>/dev/null | head -1 | awk '{print $3}' | sed 's/[^0-9.].*$//')"
[[ -n "$version" ]] || die "il rsync installato non parte (librerie mancanti?)"
printf '\nrsync privato installato: %s\n' "$DEST_DIR"
printf 'versione dichiarata: %s\n' "$version"
printf 'wrapper per i client: %s\n' "$DEST_DIR/nasbox-rsync"
printf 'Riavvia il demone: server/sync-daemon-server.sh --restart\n'
