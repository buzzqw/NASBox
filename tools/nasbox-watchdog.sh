#!/bin/sh
# NASBox watchdog: verifica lo stato del NAS e riavvia il demone se è fermo.
# Va richiamato da un'attività pianificata DSM ogni 5 minuti (utente andres,
# nessun privilegio di root). Esce 0 se il NAS è sano, non-zero altrimenti.
set -u

BIN=${NASBOX_BIN:-/volume1/Varie/nasbox-server/nasbox-server}
CONF=${NASBOX_CONF:-/volume1/Varie/sync-daemon/server/server.conf}

if [ ! -x "$BIN" ]; then
	echo "watchdog: binario non eseguibile: $BIN" >&2
	exit 2
fi

output=$("$BIN" -c "$CONF" --self-check 2>&1)
status=$?
printf '%s\n' "$output"

case "$output" in
*"CHECK_DAEMON=fail"*)
	echo "watchdog: demone fermo, avvio"
	"$BIN" -c "$CONF" --start || exit 3
	output=$("$BIN" -c "$CONF" --self-check 2>&1)
	status=$?
	printf '%s\n' "$output"
	;;
esac

exit $status
