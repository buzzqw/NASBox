# Installazione e deploy — note operative

Questo file registra **dove** gira NASBox e **come** è stato aggiornato. Non
contiene password, chiavi o segreti: per la configurazione effettiva leggere
`server/server.conf` sul NAS (non committato).

## Stato installato (2026-11-27)

| Componente | Versione | Posizione |
|---|---|---|
| Demone NAS (Go) | **3.38.0** | `andres@192.168.1.119:/volume1/Varie/nasbox-server/nasbox-server` |
| Wrapper demone | — | `/volume1/Varie/nasbox-server/nasbox-server.sh` → `nasbox-server -c /volume1/Varie/sync-daemon/server/server.conf "$@"` |
| Config demone | — | `/volume1/Varie/sync-daemon/server/server.conf` (esclusa da git) |
| Stato demone | — | `/volume1/Varie/sync-daemon/server/state/` |
| Condivisione NAS | — | `/volume1/NASBox` |
| Client Linux | **1.26.0** | `~/.local/bin/nasbox-go` su 122 e 134 |
| Launcher consultazione | — | `~/.local/bin/nasbox-ghost` → `nasbox-go -ghost "$@"` (122 e 134) |
| Canale update Go | `go-stable` | `/volume1/NASBox/.nasbox-client-update-go/` (`manifest-go.json`, `nasbox-go-<ver>`) |

Il demone gira come processo unico con timer interno di **720 minuti** (12 ore).
`--status` è il modo rapido per verificarne lo stato.

## Backup fatti durante gli aggiornamenti

- `nasbox-server.prev-3.37.0-20261127` — demone precedente al rilascio 3.38.0.
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

## Robustezza operativa (3.39.0 / client 1.27.0)

Tre cose non si possono fare dal repo e vanno impostate una volta sul NAS.

1. **Timeout delle sessioni SSH lasciate aperte** (richiede root/amministratore
   DSM). Senza, un client che sparisce senza chiudere la sessione può tenere la
   connessione mezza aperta per ore:

   ```bash
   sudo vi /etc/ssh/sshd_config
   # decommentare o aggiungere:
   ClientAliveInterval 60
   ClientAliveCountMax 3
   sudo synosystemctl restart sshd   # oppure: sudo systemctl restart sshd
   ```

   Con questi valori sshd chiude una sessione muta dopo ~3 minuti. Il lease
   globale viene rilasciato dal `flock` alla chiusura della sessione.

2. **Attività pianificata DSM per il watchdog** (Pannello di controllo →
   Utilità pianificate → Crea → Script definito dall'utente), ogni **30
   minuti**, utente `andres`:

   ```bash
   /volume1/Varie/sync-daemon/tools/nasbox-watchdog.sh
   ```

   Lo script esegue `--self-check --quick` (non apre l'indice SQLite, quindi non
   tocca il disco), riavvia il demone se è fermo e lascia nel log dell'attività
   una riga per ogni controllo fallito. Il controllo completo gira una volta al
   giorno. Non scendere sotto i 30 minuti: un controllo completo ogni pochi
   minuti impedisce l'ibernazione degli hard disk.

3. **Scadenza del lease** in `server.conf` (runtime, non si committa):

   ```ini
   SYNC_LOCK_MAX_AGE_MINUTES=120
   ```

   Il demone controlla ogni minuto il file `state/lease-holder.json`; se un
   client con heartbeat (≥ 1.27.0) è silenzioso da più di 120 minuti, termina
   il processo `--transfer-wait` e libera il lock. I client più vecchi non
   mandano heartbeat e non vengono mai interrotti.

Verifica dopo il deploy:

```bash
/volume1/Varie/nasbox-server/nasbox-server -c /volume1/Varie/sync-daemon/server/server.conf --self-check
/volume1/Varie/nasbox-server/nasbox-server -c ... --print-config | grep -E 'SYNC_LOCK|ACCESS_LOG'
ls -l /volume1/Varie/sync-daemon/server/state/access.log*
```

### Ibernazione degli hard disk

Cosa tocca il disco e quando (misurato il 2026-11-27 sul DS420j):

- **Heartbeat del lease**: esiste solo mentre un client tiene il lease, cioè
  durante un trasferimento. A NAS fermo non c'è lease, quindi nessun frame e
  nessuna scrittura. Scrive due file da poche centinaia di byte ogni 30 s, ma
  con i dischi già attivi per il trasferimento.
- **Ticker del demone** (ogni 60 s): legge solo `lease-holder.json`, che di
  solito non esiste. Misurato a sistema fermo: `read_bytes` e `write_bytes` del
  processo demone invariati (0 byte) in 90 secondi.
- **Access log**: scrive solo quando arriva una richiesta alla UI/API. Un
  monitoraggio che interroga la web UI ogni minuto tiene i dischi svegli: è
  l'unico caso da evitare.
- **Watchdog `--self-check`**: la versione completa apre l'indice SQLite e
  quindi legge/scrive; per questo lo script usa `--quick` a ogni giro e il
  controllo completo una volta al giorno.
- **Fuori da NASBox**: sul NAS il sistema scrive comunque ~1 MB ogni 90 secondi
  sulla partizione di sistema (RAID1), quindi l'ibernazione è già poco
  praticabile a prescindere dal demone.

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

## Verifica finale dopo il deploy 1.26.0 / 3.38.0

- Solo client: il demone resta 3.38.0.
- Client 122 e 134 → 1.26.0, GUI riavviata.
- Canale Go: `manifest-go.json` 1.26.0 + `manifest-go.json.sig`; il client
  rifiuta manifest assente, corrotto o non firmato.
- Chiave privata **fuori dal NAS**: `~/.config/nasbox-signing/ed25519-private.pem`
  su 122; pubblica incorporata nel client.
- Firma del manifest dopo la pubblicazione:

  ```bash
  rsync -a andres@192.168.1.119:/volume1/NASBox/.nasbox-client-update-go/manifest-go.json /tmp/manifest.json
  python3 tools/sign_update_manifest.py --manifest /tmp/manifest.json \
    --key ~/.config/nasbox-signing/ed25519-private.pem
  rsync -a /tmp/manifest.json.sig \
    andres@192.168.1.119:/volume1/NASBox/.nasbox-client-update-go/manifest-go.json.sig
  ```

- Prova negativa: con `.sig` assente o non valido `update-check` si ferma con
  errore di firma; ripristinata la firma, torna “nessun aggiornamento”.
- Conteggi dopo la pulizia delle righe-cartella legacy: 122 `98.169`
  (attivi `22.856`, tombstone `75.313`, osservati `41.987`), 134 `98.574`
  (attivi `21.826`, tombstone `76.748`, osservati `40.711`). La differenza 405
  è storia locale (tombstone/conflitti), non file vivi mancanti.

## Verifica finale dopo il deploy 1.25.0 / 3.38.0

- `--status` → v3.38.0, demone in esecuzione, ultimo pass registrato.
- `--capabilities` → 3.38.0 con le capacità chunk/private_rsync invariate.
- `--print-config` → `RECOVERY_PENDING=0`, `RECOVERY_ERROR=` vuoto.
- `--metrics` → demone attivo, lock libero.
- Client 122 e 134 → 1.25.0, `doctor` con server 3.38.0 raggiungibile,
  backend `chunks`, lock libero.
- Web UI: endpoint `/api/delete`, `/api/rename`, `/api/mkdir`, `/api/entry` e
  card Recovery nella pagina Stato.
- Canale Go: `manifest-go.json` → 1.25.0, `requires_server=3.38.0`, sha256
  `401d9aa6cf196bc185a7d354c1afbf2d0f39930c27bebdef6b279f903b7fed85`.

## Verifica finale dopo il deploy 1.24.0 / 3.37.0

- Solo client: il demone resta 3.37.0, `--status` e `--capabilities` invariati.
- Client 122 e 134 → 1.24.0, `nasbox-go version` dopo il riavvio della GUI.
- Dialog «Versioni»: pannello dettagli con data completa, byte esatti e
  percorso della copia nello storico; data non più «—» sulle versioni recenti.
