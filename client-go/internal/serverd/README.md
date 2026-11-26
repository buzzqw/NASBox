# serverd — demone NAS in Go

Sostituto in-house di `server/sync-daemon-server.sh`. Il client Go resta
invariato: il demone accetta gli stessi verbi SSH e produce gli stessi formati.

## Milestone 0 — shadow mode read-only (completata)

Verbi implementati: `--print-config`, `--capabilities`, `--status`,
`--journal-status`, `--diagnostics`, `--devices`, `--metrics`,
`--manifest-export` (pieno e delta), `--history-list`, `--browse-list`,
`--file-states-current`.

`tools/compare_server_go.py` confronta i due demoni sullo stato reale:
11/11 comandi uguali dopo la normalizzazione di valori dinamici e dell'ordine
delle righe (il renderer bash usa un array associativo `awk`).

## Milestone 1 — datapath a chunk (completata)

Nuovi verbi, non ancora usati dai client:

- `--chunk-stats`
- `--chunk-snapshot-create DEVICE PATH [PATH...]`
- `--chunk-snapshot-list`
- `--chunk-restore ID PATH DEST`

Lo store content-addressed vive in `STATE_DIR/chunks` (override
`CHUNK_STORE_DIR`), con chunk da 1 MiB (`CHUNK_SIZE`). Gli oggetti sono
immutabili e scritti con rename atomico; uno snapshot è un JSON con gli hash
dei chunk che compongono ogni file.

Prova su dati reali: due snapshot degli stessi file (CLAUDE.md + PDF da 31 MB)
hanno prodotto 32 chunk, 31.648.751 byte, senza oggetti nuovi al secondo
snapshot; il ripristino è risultato byte-identico (sha256 uguale).

## Milestone 2 — verbi mutanti sotto lease (completata)

Implementati: `--journal-append`, `--staging-publish` (V1/V2),
`--checked-delete`, `--browse-delete`, `--browse-rename` (V1/V2/V3),
`--transfer-wait`, `--run-once`, `--init-repository`.

- Il lease è verificato con `flock` non bloccante + campo device del file owner;
  i verbi mutanti non acquistano mai il lock globale (deadlock).
- Ogni transazione scrive un solo `BEGIN…COMMIT` e fa un solo incremento di
  `manifest.revision`, con rollback del journal in caso di errore.
- La rinomina V3 sposta una cartella in una richiesta, con journal
  DELETE+PUT per file e un solo bump.
- `NASBOX_E2E_SERVER=<binario> go test ./cmd/nasbox -run E2E` esegue gli E2E
  reali del client puntando al demone Go: **8 pass, 1 skip** (self-update del
  server, che richiede lo script bash).
- I 11 comandi read-only della milestone 0 restano uguali (11/11).

## Milestone 3 — ciclo del demone e retention (completata)

- `--start`, `--stop`, `--restart`, `--run-foreground`: PID file, lock del
  demone, timer interno `CHECK_INTERVAL_MINUTES`, gestione SIGTERM/SIGINT/SIGHUP.
- `--run-once` ora applica anche `RETENTION_DAYS`, ruota `events.jsonl` e
  `server.log`, ripulisce i temporanei vecchi e scrive `last-run` e
  `trash-usage`.
- Nuovi E2E: sempre 8 PASS, 1 SKIP. Lo skip è il self-update del server, che
  negli E2E sostituisce uno **script bash** con `VERSION="…"`: con un demone
  Go il self-update dovrà sostituire il **binario** e sarà implementato nella
  milestone 4.
- `--print-config` riporta `RUNNING=true` quando il demone Go è avviato.

## Milestone 4 — web UI e self-update nativo (completata)

- `--web [INDIRIZZO]`: UI minimale e API JSON (`/api/status`, `/api/metrics`,
  `/api/browse`, `/api/chunks`) servite dallo stesso binario. Default
  `127.0.0.1:8080`, quindi non esposta alla LAN per impostazione predefinita.
- `--server-update-check` / `--server-update-install VERSION`: il binario
  versionato `nasbox-server-<v>` viene verificato (sha256 opzionale), copiato
  in `.prev` e sostituito con rename atomico; la versione effettiva è letta da
  un sidecar `<binario>.version`, quindi `--print-config` riporta la nuova
  versione dopo il riavvio.
- Provato in locale: 9.9.9 installato, `VERSION=9.9.9`, `RUNNING=true`.
- E2E client: 8 PASS, 1 SKIP (il test di self-update usa ancora uno script
  bash con `VERSION="…"`; il percorso Go è coperto dal test locale sopra).
- Parity read-only: 11/11.

## Milestone 5 — protocollo a chunk lato client (completata)

Nuovi verbi server: `--chunk-put-batch`, `--chunk-get`, `--file-chunks`,
`--file-publish`. Il client ha i metodi corrispondenti in
`internal/remote/chunks.go`: carica solo i chunk mancanti, pubblica il file in
una transazione journalata e può rileggere i chunk per ricostruire il file.

Il protocollo è coperto da `TestE2EChunkProtocol` (~2 MB, più chunk da 1 MiB):
upload, publish con journal, listing e download verificato. Con il demone Go:
**9 PASS, 1 SKIP** (self-update bash). Con lo script bash il test viene saltato,
perché i verbi a chunk non esistono nel vecchio server.

## Milestone 6 — backend di trasferimento a chunk (completata)

- `internal/transfer/chunks.go`: `Chunks` implementa lo stesso backend di
  `Transfer` (download, staging upload, replace sotto lease, observer).
- `transfer.NewForConfig` seleziona `chunks` con
  `transfer_backend=chunks`; il default resta `rsync`, così i client puntati
  al vecchio demone bash continuano a funzionare.
- I file fuori banda vengono ingeriti con `--chunk-ingest`, quindi anche una
  modifica fatta direttamente sul NAS è scaricabile via chunk.
- `NASBOX_E2E_SERVER=<demone Go> NASBOX_E2E_CHUNKS=1 go test ./cmd/nasbox -run E2E`:
  **9 PASS, 1 SKIP** — tutti gli E2E reali del client girano con rsync
  sostituito dal protocollo a chunk.
- Nota: ogni file pubblicato usa una transazione journal propria; il batch
  single-transaction di `staging-publish` resta più efficiente e sarà il
  prossimo miglioramento.

## Milestone 7 — metadata SQLite e delta reali (completata)

- `STATE_DIR/metadata.sqlite3` è un indice derivato di `manifest.tsv` e
  `transfer-journal.tsv`: WAL, schema versionato, checkpoint del journal e
  ricostruzione automatica se manca, è stantio o è stato compattato.
- `--manifest-export-delta` restituisce solo le righe il cui
  `updated_revision` è successiva al cursore. Le revisioni precedenti a
  `feed_start_revision` ricevono il compatibile `FALLBACK_FULL=1`.
- Export, `--manifest-get`, `--file-chunks` e `--file-states-current` usano
  lookup indicizzati sotto il lock del journal; il renderer TSV resta il
  fallback di recupero.
- Il retry di una transazione ripara un bump di revisione mancante; un errore
  di compattazione dopo il commit viene registrato ma non annulla la mutazione.

## Milestone 8 — trasferimenti robusti e recovery (completata)

- Upload streaming con deduplica di rete (`--chunk-has`) e compressione zlib
  trasparente (`--chunk-put-compressed`); gli oggetti restano raw su disco.
- File vuoti, digest intero verificato, limiti di banda e ripresa download dal
  checkpoint locale.
- Pubblicazione batch V2 con modalità `create`/`replace`, precondition contro
  target concorrenti e rollback completo prima del commit.
- Intent persistenti `tx-intent.sqlite3` con recupero sotto lease per crash
  durante la pubblicazione.
- `--restore-batch`, validazione dei percorsi e protezione contro symlink antenati.

## Distribuzione futura

1. Distribuire il demone Go sul NAS e verificare i 11 comandi read-only.
2. Impostare `transfer_backend=chunks` nei client.
3. Disattivare rsync e lo script bash.

## Manutenzione dei chunk (fase 0 della roadmap)

Il chunk store è una cache di deduplica: i byte autorevoli stanno nell'albero,
gli snapshot sono l'unica parte non ricostruibile.

- `--chunk-gc [--dry-run] [--grace-hours N]`: rimuove chunk e meta-file non
  raggiungibili dal manifest vivo o da uno snapshot. Periodo di grazia di 24 ore
  (un upload non ancora pubblicato ha chunk ma non ancora il meta); nessuna
  cancellazione se uno snapshot è illeggibile. `--chunk-has` aggiorna la data dei
  chunk riusati, così non vengono raccolti tra la richiesta e la pubblicazione.
- `--chunk-fsck [--repair]`: rilegge ogni chunk e ne verifica lo SHA-256,
  controlla meta-file e snapshot; con `--repair` rimuove solo ciò che si
  ricostruisce (mai gli snapshot). Esce con codice 2 se restano problemi.

## Web UI di consultazione e caricamento

`--web [INDIRIZZO]` (o `WEB_LISTEN` nel demone). Pagina unica in `webui.go`,
API in `web.go` e `webapp.go`: `/api/browse`, `/api/search`, `/api/versions`,
`/api/download` (con `inline=1` solo per immagini, PDF, audio, video e testo),
`/api/upload` (POST multipart), `/api/info`.

- Senza `WEB_PASSWORD`: sola lettura, solo loopback. Con password: Basic auth,
  blocco per indirizzo dopo 5 errori in 10 minuti, header `X-NASBox-Web: 1` e
  Origin uguale all'host sulle scritture.
- L'upload scrive i byte nel chunk store, prende il lease globale come device
  `web` e chiama `publishChunkRecords(..., "create")`: journal, manifest e intent
  di recovery come per un client. Un nome già presente diventa `nome (1).ext`.
- I nomi con tab, newline o caratteri di controllo sono rifiutati (romperebbero
  journal e manifest TSV).

## Coerenza manifest/disco, manutenzione e notifiche

- `--audit-orphans [--list]`, `--audit-adopt [--dry-run]`,
  `--audit-phantoms [--forget] [--dry-run]`, `--journal-forget` (percorsi NUL su
  stdin): scrivono solo il journal, nessun byte si sposta. Un file scritto da meno
  di 2 minuti non si adotta; i percorsi interni (`.sync-trash` a qualsiasi
  profondità, `.nasbox-staging`, temporanei di rsync, `@eaDir`) sono ignorati.
- `--run-once` esegue anche: GC dei chunk, fsck settimanale, backup giornaliero
  dello stato (`state/backup/<data>/`) e un controllo del disallineamento. Le
  chiavi sono in `server/server.conf.example`.
- `NOTIFY_URL`: webhook compatibile con ntfy. Mai nomi di file nei messaggi.

## Link di condivisione

`POST /api/share?path=&days=1|7|30|0`, `GET /api/share`, `DELETE /api/share?token=`
(con password e header `X-NASBox-Web`) e `GET /s/<token>` (pubblico). Vedi
`webshare.go`. Il link è un token casuale: senza password non si crea né si
elenca nulla; le richieste a `/s/` con token sbagliato sono limitate per indirizzo.
