# Installazione e deploy — note operative

Questo file registra **dove** gira NASBox e **come** è stato aggiornato. Non
contiene password, chiavi o segreti: per la configurazione effettiva leggere
`server/server.conf` sul NAS (non committato).

## Stato installato (2026-11-26)

| Componente | Versione | Posizione |
|---|---|---|
| Demone NAS (Go) | **3.37.0** | `andres@192.168.1.119:/volume1/Varie/nasbox-server/nasbox-server` |
| Wrapper demone | — | `/volume1/Varie/nasbox-server/nasbox-server.sh` → `nasbox-server -c /volume1/Varie/sync-daemon/server/server.conf "$@"` |
| Config demone | — | `/volume1/Varie/sync-daemon/server/server.conf` (esclusa da git) |
| Stato demone | — | `/volume1/Varie/sync-daemon/server/state/` |
| Condivisione NAS | — | `/volume1/NASBox` |
| Client Linux | **1.24.0** | `~/.local/bin/nasbox-go` su 122 e 134 |
| Launcher consultazione | — | `~/.local/bin/nasbox-ghost` → `nasbox-go -ghost "$@"` (122 e 134) |
| Canale update Go | `go-stable` | `/volume1/NASBox/.nasbox-client-update-go/` (`manifest-go.json`, `nasbox-go-<ver>`) |

Il demone gira come processo unico con timer interno di **720 minuti** (12 ore).
`--status` è il modo rapido per verificarne lo stato.

## Backup fatti durante gli aggiornamenti

- `nasbox-server.prev-3.36.0-20261126` — demone precedente (anche
  `nasbox-server.prev-3.36.0` e `.prev-3.35.0` già presenti nella cartella).
- `/volume1/Varie/sync-daemon/server/server.conf.bak-20261126-2000` — config
  prima dell'aggiunta delle chiavi chunk.
- I client hanno `~/.local/bin/nasbox-go.bak` gestito dall'updater, con
  `nasbox-go.update-state.json` (`NEW_INSTALLED` / `CONFIRMED`).

## Configurazione chunk attiva (valori, non segreti)

```
CHUNK_MAX_MB=4096            # quota store chunk (store reale ~153 MB)
CHUNK_FSCK_DAYS=7
CHUNK_SCRUB_HOUR=3
CHUNK_SCRUB_WINDOW_HOURS=5   # finestra 03:00-07:59; 0 = sempre
CHUNK_SCRUB_NICE=10          # priorita' CPU di GC e fsck
```

Le stesse chiavi sono accettate anche dal vecchio demone bash
(allowlist in `server/sync-daemon-server.sh`), così `server.conf`resta condivisa.

## Capacità dichiarate dal NAS (rilevate, non inventate)

```
CPU_COUNT=4  MEM_TOTAL_MB=987  DISK_ROTATIONAL=1  FS_TYPE=ext4
RSYNC_VERSION=3.5.1  RSYNC_SYSTEM_VERSION=3.1.2
FILE_STATES_PARALLELISM=4  CHUNK_TRANSFER_VERSION=3
CAPABILITIES=... chunk_quota_v1, chunk_scrub_v1, server_metrics_v1, private_rsync
```

## Comandi di verifica usati

```bash
# sul NAS
/volume1/Varie/nasbox-server/nasbox-server --status
/volume1/Varie/nasbox-server/nasbox-server --capabilities
/volume1/Varie/nasbox-server/nasbox-server --print-config | grep -i chunk
/volume1/Varie/nasbox-server/nasbox-server --metrics | grep -E 'PERF_|CHUNK_HAS|CHUNK_FSCK'

# dai client
nasbox-go version
nasbox-go doctor --json
nasbox-ghost version          # launcher modalita' consultazione
```

## Procedura di rilascio usata

1. Modifiche e test in copia di lavoro (`go vet ./... && go test ./...` su un
   client con Go; il NAS non ha Go).
2. Commit sul repo, bundle → NAS → `git merge --ff-only` → `git push origin master`.
3. Server: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/nasbox-server`
   (il NAS è ARM64; un binario amd64 dà `Exec format error`). Copia su volume,
   backup del precedente, `mv`, `--restart`.
4. Client: `go build ./cmd/nasbox` (amd64), copia sul NAS, poi
   `python3 tools/publish_go_client.py --binary ... --dest-dir /volume1/NASBox/.nasbox-client-update-go --keep 2`.
5. Aggiornamento dei client: `nasbox-go update-install` + riavvio della GUI
   (122 e 134). Verificare `nasbox-go version`.

Nota: il canale di update Go si chiama `go-stable` e usa `manifest-go.json`;
quello Python legacy non va più pubblicato.

## Verifica finale dopo il deploy 1.23.0 / 3.37.0

- `--status` → v3.37.0, demone in esecuzione, ultimo pass registrato.
- `--capabilities` → `chunk_quota_v1`, `chunk_scrub_v1`, `server_metrics_v1`,
  `private_rsync`; `RSYNC_VERSION=3.5.1` (sistema 3.1.2).
- `--print-config` → `CHUNK_MAX_MB=4096`, `CHUNK_SCRUB_HOUR=3`,
  `CHUNK_SCRUB_WINDOW_HOURS=5`, `CHUNK_SCRUB_NICE=10`.
- `--metrics` su dati reali → `CHUNK_HAS_HIT_RATE_PERCENT=85.0`,
  `MANIFEST_DELTA_REQUESTS=212`, contatori `PERF_*` compilati dai client.
  `MANIFEST_RENDER_CACHE_HITS=0`: i client hanno cursori dentro
  `feed_start_revision`, quindi usano il delta per riga (la cache serve ai
  cursori più vecchi ed è stata verificata con test dedicati).
- Web UI → pagina "Stato" raggiungibile con `/api/metrics`.
- Client 122 e 134 → 1.23.0, `doctor` con server raggiungibile e lock libero.
- `last_manifest_revision` persistito nel `client.json` del 122 (legato al
  repository id): dopo il riavvio non serve più il manifest completo.

## Verifica finale dopo il deploy 1.24.0 / 3.37.0

- Solo client: il demone resta 3.37.0, `--status` e `--capabilities` invariati.
- Client 122 e 134 → 1.24.0, `nasbox-go version` dopo il riavvio della GUI.
- Dialog «Versioni»: pannello dettagli con data completa, byte esatti e
  percorso della copia nello storico; data non più «—» sulle versioni recenti.
