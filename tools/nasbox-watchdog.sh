#!/bin/sh
# NASBox watchdog: verifica lo stato del NAS e riavvia il demone se è fermo.
# Va richiamato da un'attività pianificata DSM (utente andres, senza root),
# ogni 30 minuti circa.
#
# Per non impedire l'ibernazione degli hard disk:
#   - a ogni esecuzione usa --quick, che non apre l'indice SQLite (nessuna
#     scrittura, nessuna lettura dal volume);
#   - il controllo completo (journal incluso) gira una volta al giorno.
set -u

BIN=${NASBOX_BIN:-/volume1/Varie/nasbox-server/nasbox-server}
CONF=${NASBOX_CONF:-/volume1/Varie/sync-daemon/server/server.conf}
STATE=${NASBOX_STATE:-/volume1/Varie/sync-daemon/server/state}
STAMP="$STATE/self-check-full.stamp"

if [ ! -x "$BIN" ]; then
	echo "watchdog: binario non eseguibile: $BIN" >&2
	exit 2
fi

run_full=1
if [ -f "$STAMP" ]; then
	stamp_age=$(( $(date +%s) - $(stat -c %Y "$STAMP" 2>/dev/null || echo 0) ))
	if [ "$stamp_age" -lt 86400 ]; then
		run_full=0
	fi
fi

if [ "$run_full" = 1 ]; then
	output=$("$BIN" -c "$CONF" --self-check 2>&1)
	status=$?
	if [ "$status" -eq 0 ]; then
		: > "$STAMP"
	fi
else
	output=$("$BIN" -c "$CONF" --self-check --quick 2>&1)
	status=$?
fi
printf '%s\n' "$output"

case "$output" in
*"CHECK_DAEMON=fail"*)
	echo "watchdog: demone fermo, avvio"
	"$BIN" -c "$CONF" --start || exit 3
	output=$("$BIN" -c "$CONF" --self-check --quick 2>&1)
	status=$?
	printf '%s\n' "$output"
	;;
esac

exit $status
