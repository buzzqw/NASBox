# Changelog

Tutte le modifiche rilevanti di NASBox sono documentate in questo file.

## Unreleased

## 1.27.0 / 3.39.0 - 2026-11-27

Client 1.27.0, server 3.39.0. Nessun cambio di formato su manifest, journal e
intent: i client 1.26.0 continuano a funzionare con il server nuovo.

- **Lease che non resta bloccato**: il client manda un frame `ACTIVITY_V1` ogni
  30 secondi mentre tiene il lease; il server registra PID e ultimo battito in
  `state/lease-holder.json` e, con `SYNC_LOCK_MAX_AGE_MINUTES > 0`, termina un
  `--transfer-wait` rimasto senza heartbeat oltre la soglia (0 = disabilitato).
  Prima di terminarlo verifica che il PID sia ancora un `nasbox-server
  --transfer-wait`, così un PID riusato non fa danni. I client che non mandano
  heartbeat non vengono mai interrotti.
- **`--self-check`**: un controllo per voce (`CHECK_DAEMON`, `CHECK_JOURNAL`,
  `CHECK_LAST_RUN`, `CHECK_DISK_*`, `CHECK_CHUNK_QUOTA`, `CHECK_LEASE`,
  `CHECK_TEMP_DIRS`) con exit code non-zero se qualcosa non va, pensato per un
  watchdog esterno. Aggiunto `tools/nasbox-watchdog.sh` per l'attività
  pianificata DSM (riavvia il demone fermo).
- **Access log della web UI/API** in `state/access.log`: una riga per richiesta
  (data, IP, metodo, percorso, stato, byte, durata, utente), con rotazione a
  dimensione (`ACCESS_LOG_MAX_BYTES`, default 5 MB, `ACCESS_LOG_KEEP` default 3).
  I token `/s/<token>` diventano `/s/[token]` e la query string non viene
  scritta. `--status` mostra la dimensione corrente.
- **Restore verificato**: `--chunk-restore` ricalcola lo SHA-256 del file
  ricomposto e, se non coincide con lo snapshot, elimina il risultato e
  fallisce invece di lasciare su disco un file sbagliato.
- **Export manifest più veloce**: una sola scrittura bufferizzata da 256 KB
  invece di un `Println` per riga (~100k righe su un manifest pieno) e nessuna
  riscrittura del render cache quando la revisione è già in cache.
- **Pulizia dei temporanei completa**: vengono rimosse anche le directory
  `.manifest-delta.<pid>.<rand>` e le copie SQLite messe in quarantena, dopo
  `STATE_TEMP_MAX_AGE_HOURS` e solo se il PID non è più vivo. Sul NAS erano
  rimaste 44 MB di delta orfani.
- **Stato del lease trasparente**: `--status` e `--print-config` riportano
  `SYNC_LOCK_IDLE_SECONDS`, `SYNC_LOCK_STALE`, `SYNC_LOCK_HEARTBEAT` e la
  soglia reale; la pagina Stato espone `LEASE_*` e `ACCESS_LOG_BYTES`.
- `--web` limita gli header a 16 KB; i link condivisi inviano anche
  `X-Frame-Options: DENY`.

## 1.26.0 - 2026-11-27

Client 1.26.0, server 3.38.0 (invariato).

- **Aggiornamenti firmati**: il manifest `manifest-go.json` ha una firma
  detached Ed25519 in `manifest-go.json.sig`; il client verifica la firma prima
  di accettare versione, artifact o hash. La chiave privata resta fuori dal NAS;
  nel client è incorporata solo la chiave pubblica. Un manifest senza firma,
  con firma errata o modificato viene rifiutato.
- **Conteggio file più chiaro**: `doctor` separa baseline attive, tombstone e
  file osservati localmente. Le righe legacy che rappresentavano cartelle con
  una barra finale vengono rimosse all'apertura dello stato; il conteggio
  “tracciato” non viene più confuso con un numero di file vivi.
- Aggiunto `tools/sign_update_manifest.py` per firmare il manifest con la chiave
  privata offline.

## 1.25.0 / 3.38.0 - 2026-11-27

Client 1.25.0 e server 3.38.0.

- **Recovery durevole per delete, rename e restore**: ogni operazione scrive un
  intent persistente prima dello spostamento su disco; se il processo si
  interrompe, il lease successivo completa il journal o annulla l’operazione in
  modo idempotente. Lo stato è visibile in `doctor`, nella schermata di controllo
  e nella pagina Stato della web UI (`RECOVERY_PENDING`); gli stati ambigui
  falliscono chiusi e richiedono intervento.
- **Diagnostica allineata al backend reale**: la schermata di controllo e
  `doctor` mostrano il backend effettivo (`chunks` o `rsync`); rsync locale e
  sul NAS vengono verificati solo quando quel backend lo usa; il rilevamento
  modifiche riporta lo stato del watcher `fsnotify` invece di cercare
  `inotifywait`. README e installer non chiedono più `inotify-tools`.
- **Stato per file nel browser NAS**: ogni file mostra Sincronizzato, In
  caricamento, In scaricamento, Conflitto, Solo sul NAS o Non sincronizzato,
  con una spiegazione nella barra di selezione. Lo stato è indicativo (presenza
  e dimensione); conflitto e trasferimento in corso hanno priorità. Restano
  disponibili Versioni per il ripristino e Annulla per fermare il batch.
- **Web UI: elimina, rinomina e crea cartelle**: dalla pagina File si possono
  eliminare file e cartelle (spostati nel cestino del NAS, con conferma e
  conteggio dei file), rinominare file e cartelle senza sovrascrivere, e creare
  una nuova cartella. Le operazioni usano lo stesso lease, journal e cestino del
  client desktop; richiedono `WEB_PASSWORD`, l'header anti-CSRF e il percorso
  sicuro già usato da browse/upload. Una cartella vuota creata dal web compare
  sugli altri PC quando contiene almeno un file.

## 1.24.0 - 2026-11-27

Client 1.24.0 e server 3.37.0 (invariato).

- **Dettagli completi nel dialog «Versioni»**: selezionando una versione ora
  compare un pannello con percorso del file, data e ora completa (secondi e
  offset), dimensione leggibile e byte esatti, dove è conservata la copia
  (questo PC o NAS), percorso della copia nello storico e identificativo della
  versione. Anche le righe della lista mostrano timestamp con secondi e
  dimensione esatta.
- **Date delle versioni corrette**: il parser del dialog accettava solo un
  letterale `-000000Z`, quindi le versioni recenti (con microsecondi) non
  avevano data e venivano ordinate male. Usa ora lo stesso parser dello storico.

## 1.23.0 / 3.37.0 - 2026-11-26

Client 1.23.0 e server 3.37.0.

- **Delta del manifest anche per cursori vecchi**: l'indice SQLite continua a
  servire il delta per riga ai client recenti; per gli altri il server tiene
  una cache del render completo per revisione e restituisce solo le righe
  cambiate (con `REMOVED=`) invece dell'export da 11 MB. Lo snapshot resta
  byte-identico a quello del renderer storico; se la cache non c'è scatta
  ancora `FALLBACK_FULL=1`.
- **Il cursore del manifest sopravvive al riavvio** del client (`last_manifest_revision`,
  legato al `repository_id`): dopo un riavvio non serve più scaricare il
  manifest intero.
- **Statistiche lato NAS**: `--metrics` e `/api/metrics` espongono tempi delle
  fasi lato server, hit rate di `--chunk-has` e byte risparmiati, byte di delta
  serviti e fallback, attesa/durata del lease per dispositivo, esito dell'ultimo
  fsck dei chunk. I contatori sono su disco, quindi visibili anche dai comandi
  SSH di breve durata.
- **Web UI: pagina Stato** con lock, coda, deduplica, manifest, lock per
  dispositivo, integrità e tempi delle fasi (nessuna libreria esterna).
- **Quota dello store chunk** (`CHUNK_MAX_MB`) con rimozione dei chunk non
  referenziati più vecchi: non tocca mai chunk vivi o di snapshot e, se la quota
  non è raggiungibile, segnala `chunk_quota_blocked` invece di cancellare dati.
- **Manutenzione in finestra notturna** (`CHUNK_SCRUB_HOUR`,
  `CHUNK_SCRUB_WINDOW_HOURS`) ed eseguita con priorità CPU bassa
  (`CHUNK_SCRUB_NICE`): GC e fsck non girano più in qualsiasi momento.
- Nuove capacità dichiarate: `chunk_quota_v1`, `chunk_scrub_v1`,
  `server_metrics_v1`. Il lease resta globale per le mutazioni: una sua
  suddivisione per prefisso richiederebbe journal, revisione e tx-intent
  separati e non è stata fatta.

## 1.22.0 - 2026-11-26

Client 1.22.0 e server 3.36.0 (invariato).

- **Modalità consultazione (`-ghost`)**: `nasbox -ghost gui` (oppure
  `"mode": "ghost"` in `client.json`) avvia un client che **non sincronizza
  nulla**: non parte il watcher, non parte il change feed, non ci sono passate
  periodiche né caricamenti automatici. La GUI si apre direttamente su
  *File › Sfoglia NAS* e nasconde le sezioni legate alla sincronizzazione.
- **Sfoglia qualsiasi cartella del NAS**: nuovo campo «Vai a cartella» per
  aprire direttamente un sottoalbero condiviso, senza toccare le cartelle di
  sync. Funziona anche da riga di comando con `nasbox browse [percorso]`.
- **Caricamento su richiesta**: pulsante *Carica* nella Sfoglia NAS e comando
  `nasbox upload <file> [percorso-remoto]`. I file nuovi vengono pubblicati con
  il consueto journal; quelli già esistenti vengono sostituiti con
  precondizione e conservano la versione precedente nel cestino del NAS.
- **Download di cartelle con entrambi i backend**: `nasbox download <percorso>`
  (alias di `browse-download`) e il pulsante «Scarica cartella» attraversano
  l'albero lato client, quindi funzionano anche col backend a chunk.
- La modalità predefinita resta `normal`: un client esistente non cambia
  comportamento.

## 1.21.3 - 2026-11-26

Client 1.21.3 e server 3.36.0.

- **«Scarica selezionato» non sembra più un pulsante morto**: se il file è già
  sul PC con la stessa dimensione e data del NAS non viene riscaricato (prima
  un PDF da 90 MB veniva ricopiato per intero e non cambiava nulla); l'esito
  compare nell'etichetta della scheda File e in una finestra, anche in caso di
  errore. Un file non ancora presente viene trasferito come prima, ma ora il
  progresso si vede nella scheda Trasferimenti: il trasferimento manuale non
  aveva osservatori, quindi restava muto fino alla fine.
- Un solo download manuale per volta: un secondo click non lancia un rsync in
  parallelo ma avvisa che ce n'è già uno in corso.

## 1.21.2 - 2026-11-26

Client 1.21.2 e server 3.36.0.

- **La finestra non lampeggia più quando sincronizza un file**: un solo
  caricamento causava più passate ravvicinate e il banner passava ogni volta da
  «in corso» a «sincronizzato». Ora «sincronizzato» attende 1,2 secondi e viene
  annullato se parte subito un'altra passata; uno stato identico non ridisegna il
  banner; la scheda del trasferimento compare solo se dura più di 0,6 secondi
  (prima spostava tutta la schermata su e giù); l'icona della tray non viene
  reimpostata se è già quella giusta.

## 1.21.1 - 2026-11-26

Client 1.21.1 e server 3.36.0.

- Rimossa una copia obsoleta dello script del demone (`sync-daemon-server-3.8.2.sh`).
- La build Windows (sperimentale) in CI ha un limite di 20 minuti: se il runner è
  lento non blocca più la release.

## 1.21.0 - 2026-11-26

Client 1.21.0 e server 3.36.0.

Pulizia:
- **Rimosso il client Python/PyQt6** (ritirato da tempo) con i suoi strumenti di
  build e di pubblicazione: ~37.000 righe in meno. L'ultima versione resta nel tag
  Git `python-legacy-final`. `client-go/install.sh --migrate` resta per chi passa al
  client Go; configurazione e stato mantengono lo stesso formato.
- **CI più chiara**: il workflow `Tests` esegue ora anche vet e test Go a ogni push
  (prima solo sui tag) e i test del demone a script; il workflow `Package` non ha
  più il job Python.
- `CONTRIBUTING.md` riscritto in inglese; `server/install.sh` non rimanda più a un
  installer che non esiste.

## 1.20.1 - 2026-11-26

Client 1.20.1 e server 3.36.0.

Documentazione:
- **README e manuale riscritti, in inglese**: il README è una guida per chi usa
  NASBox (cosa fa, avvio rapido, uso quotidiano, sicurezza) e `MANUAL.md` sostituisce
  il vecchio manuale, che descriveva ancora il client Python ritirato.
- **Documentazione interna resa privata**: note operative, roadmap, backlog, parità
  con il client Python, note di design e procedura di rilascio non sono più nel
  repository (restano nelle copie private di chi sviluppa). Restano nella cronologia
  di Git dei commit precedenti.
- **Le release includono il demone per il NAS** (`nasbox-server-linux-arm64` e
  `-amd64`) e `server.conf.example`, con le istruzioni di installazione nel manuale.

## 1.20.0 - 2026-11-26

Client 1.20.0 e server 3.36.0.

- **Tray più compatta** (da ~27 a ~15 voci): ultimi 3 file senza duplicati, pausa
  «1 ora» e «fino a domani» (le altre durate in «Altre pause…»), un solo
  interruttore per il limite di velocità (spuntato = limitato; ricorda l'ultimo
  limite scelto) più «Altri limiti…», «Conflitti» solo quando ce ne sono, e
  tolte «Cerca un file…» e «Controlla aggiornamenti» (restano nella finestra).

## 1.19.1 - 2026-11-26

Client 1.19.1 e server 3.36.0.

## 1.19.0 - 2026-11-26

Client 1.19.0 e server 3.36.0.

Windows (sperimentale, **non ancora provato su un PC Windows**):
- **Il client ora compila per Windows**: nuovi pacchetti `filelock` (flock su Unix,
  `LockFileEx` su Windows) e `osx` (aprire file/cartelle, processo staccato, spazio
  libero, riavvio), identità dei file e del volume con versioni per sistema.
  Nessun cambiamento di comportamento su Linux.
- La release GitHub allega `nasbox-go-X-windows-amd64-sperimentale.exe` se la
  build su runner Windows riesce; se fallisce la release Linux non è toccata.
- Limiti noti su Windows: nessun aggiornamento automatico (si installa a mano),
  stato locale in `~/.config/sync-daemon`, trasporto solo via backend a chunk su
  `ssh` (OpenSSH di Windows non ha ControlMaster, quindi più lento), cache dei
  file senza inode/ctime.

## 1.18.1 - 2026-11-26

Client 1.18.1 e server 3.36.0.

- **Test della GUI stabili su CI**: il caricamento in background dell'albero delle
  cartelle (sync selettiva) gareggiava con i ridisegni dei test; nei test non
  parte più (nell'app è invariato). Il rilascio 1.18.0 era fallito su GitHub per
  questo motivo: il demone 3.36.0 e il canale erano già aggiornati, la release
  GitHub arriva con 1.18.1.
- `tools/release_all.sh` ferma la consegna se il workflow GitHub fallisce.

## 1.18.0 - 2026-11-26

Client 1.18.0 e server 3.36.0.

Web UI:
- **Link di condivisione**: dal pulsante «Condividi» su un file si crea un link
  (1, 7, 30 giorni o senza scadenza) scaricabile da chiunque senza password, e
  «Link condivisi» li elenca, li copia e li revoca. Token casuale a 192 bit in
  `state/share-links.json` (0600); il file è servito sempre come allegato, mai
  inline; 404 per token sconosciuti e 429 dopo 5 tentativi falliti; un file
  sostituito da un symlink non viene mai servito. Solo file reali (non cartelle,
  non percorsi interni). Creare, elencare e revocare richiedono `WEB_PASSWORD`.
  `WEB_PUBLIC_URL` imposta l'indirizzo scritto nei link (proxy o nome DNS).

## 1.17.0 - 2026-11-26

Client 1.17.0 e server 3.35.0.

Interfaccia:
- **Impostazioni in vista semplice**: solo Generale, Cartelle e mirror,
  Informazioni. Sicurezza, Integrità e Avanzate compaiono con «Strumenti
  avanzati». I rimandi interni alle schede nascoste le rivelano da soli.

Demone (server):
- **Verifiche di coerenza anche nel demone Go**: `--audit-orphans [--list]`,
  `--audit-adopt [--dry-run]`, `--audit-phantoms [--forget] [--dry-run]` e
  `--journal-forget` non esistevano più dopo il passaggio dallo script bash.
  Gli orfani adottati hanno l'mtime in nanosecondi esatti (il bash lo salvava in
  secondi e `--file-states` li rileggeva a ogni controllo); l'hashing è parallelo
  secondo i core del NAS; un file cambiato mentre lo si leggeva non viene adottato.
- **Manutenzione notturna più completa** (`--run-once`): GC dei chunk, verifica
  d'integrità settimanale (`CHUNK_FSCK_DAYS`), backup giornaliero di journal e
  manifest in `state/backup` (`STATE_BACKUP_KEEP`) e controllo del disallineamento
  manifest/disco (`AUDIT_AUTO=report|adopt|off`). Le righe fantasma non si
  dimenticano mai da sole. Gli esiti vanno in `events.jsonl`.
- **Notifiche** (`NOTIFY_URL`, compatibile con ntfy): disco oltre la soglia,
  problemi dei chunk, manifest e disco non coerenti. Solo conteggi, mai nomi di
  file; un messaggio per tipo ogni `NOTIFY_MIN_HOURS`.
- Lo script bash accetta le nuove chiavi di `server.conf`.

## 1.16.0 - 2026-11-26

Client 1.16.0 e server 3.34.0.

Web UI e tray:
- **Web UI di consultazione e caricamento** (server): pagina unica, chiara/scura,
  adatta al telefono. Sfoglia le cartelle, cerca per nome, anteprima di immagini,
  PDF, audio, video e testo, scarica file e versioni precedenti, carica file con
  trascinamento. Un caricamento passa dal publish transazionale sotto lo stesso
  lease dei PC (device `web`): finisce nel journal e nel manifest, quindi arriva
  agli altri PC, e non sovrascrive mai un file esistente (`nota (1).txt`).
  Sicurezza: senza `WEB_PASSWORD` è di sola lettura e `--web` rifiuta di ascoltare
  fuori da loopback; le scritture richiedono password, header `X-NASBox-Web` e
  Origin coerente (anti-CSRF); blocco dopo 5 password errate; HTML/SVG dell'utente
  non sono mai mostrati inline; TLS con `WEB_TLS_CERT`/`WEB_TLS_KEY`. Chiavi in
  `server/server.conf.example`.
- **Tray**: «File recenti» e «Pausa» erano vuoti su XFCE (e altri desktop) perché
  usavano sottomenu. Il menu ora è piatto, mostra gli ultimi 5 file, le pause
  (15 min, 1 ora, 4 ore, fino a domani, finché non riprendo) e «Riprendi». Si
  ricostruisce solo quando cambia qualcosa, al massimo ogni 2 secondi.
- **Limite di banda** a portata di mano: voce «Velocità massima» nella tray (con
  spunta sul limite attivo) e nella Home (Illimitata, 256 KB/s … 10 MB/s, valore
  libero). Vale per caricamenti e scaricamenti ed è immediato; i tre campi
  separati restano nelle impostazioni avanzate.

Interfaccia più semplice (feedback: «la GUI è complessa, un utente si perde»):
- **Vista semplice di default**: barra laterale con Stato, File, Impostazioni.
  Attività (coda, log) e NAS (monitor, dispositivi, storage) stanno sotto
  «Strumenti avanzati» (`ui_advanced`); i rimandi interni li mostrano da soli.
- **Home essenziale**: banner, attività recente, spazio NAS. Contatori e
  strumenti tecnici solo in avanzato; niente più «cosa fare ora» ridondante
  quando è tutto sincronizzato.
- **File › Sfoglia** su due righe (navigazione e azioni sulla selezione), senza
  percorso duplicato né campo «Percorso remoto»; «Scarica cartella» a un clic.
- Etichette senza gergo (Audit → Verifica, «Differenze audit» → «Differenze
  trovate»).

Fase 0 della roadmap (`roadmap-claude.md`):
- **La conferma delle cancellazioni di massa sblocca davvero la protezione
  cartella**: `confirm-delete` (CLI e pulsante della GUI) valeva solo per la
  guardia cumulativa; ora vale anche per la guardia "cartella" e per il limite di
  cancellazioni locali (`max_delete_files`). Le sentinelle mancanti restano un
  blocco duro, perché di solito indicano un disco non montato.
- **Garbage collection dei chunk** (server): `--chunk-gc [--dry-run]
  [--grace-hours N]` rimuove chunk e meta-file non raggiungibili da manifest vivo
  o snapshot, con periodo di grazia di 24 ore; non cancella nulla se uno snapshot
  è illeggibile. `--chunk-has` aggiorna la data dei chunk riusati, così la GC non
  li raccoglie tra la domanda e la pubblicazione.
- **`--chunk-fsck [--repair]`** (server): rilegge ogni chunk e ne verifica lo
  SHA-256, controlla meta-file e snapshot; con `--repair` rimuove solo ciò che è
  ricostruibile (mai gli snapshot). Esce con codice 2 se restano problemi.

## 1.15.3 - 2026-11-26

Client 1.15.3 e server 3.33.0:
- **Metadata NAS indicizzata da SQLite**: `server/state/metadata.sqlite3` è un
  indice derivato e ricostruibile di manifest e journal. Export delta, lookup
  manifest e `file-states-current` non rendono più l'intero repository; journal e
  TSV restano il formato compatibile e il livello di recupero.
- **Delta manifest realmente incrementale**: ogni transazione aggiorna la
  revisione della riga nell'indice. Snapshot e revisione sono coerenti sotto il
  lock del journal; i client troppo indietro ricevono ancora `FALLBACK_FULL`.
- **Recovery del cursore**: un retry dopo un crash tra commit del journal e bump
  della revisione ripara il cursore. La compattazione avvenuta dopo un commit non
  può più far rollbackare filesystem e metadati.
- **Database client versionato**: migrazioni additive transazionali, schema
  compatibile con Python, pragma WAL/busy timeout/foreign key e `Observe` atomica.
- **Manifest client più sicuro**: revisione letta prima dell'export completo,
  validazione della base del delta e nessun secondo download quando il server
  invia già `FALLBACK_FULL`.
- **File states precisi**: lookup indicizzato che include il journal recente e
  mtime in nanosecondi, senza rihash inutili sui file invariati.
- **Upload chunk in streaming**: il file viene riletto e inviato un chunk alla
  volta, senza mantenere in RAM l'intero contenuto; il lease NAS è tenuto solo
  per precondition e pubblicazione.
- **Deduplica di rete e compressione**: il client chiede al NAS quali chunk
  mancano e comprime solo quelli da inviare quando conviene; gli oggetti sul NAS
  restano raw e content-addressed.
- **Limiti di banda e ripresa**: upload/download rispettano i limiti KB/s già
  presenti nelle Impostazioni; il download conserva un checkpoint verificato e
  riprende dai chunk mancanti dopo un'interruzione.
- **Progresso e cancellazione chunk**: callback di avanzamento per upload e
  download e propagazione del context di annullamento alle operazioni SSH.
- **File vuoti e digest intero**: pubblicazione/download dei file da zero byte e
  verifica SHA-256 dell'intero file scaricato.
- **Pubblicazioni atomiche**: rollback completo dei nuovi target, retry
  idempotenti e modalità `create` che rifiuta un percorso creato nel frattempo.
- **Capability del backend**: il client negozia `chunk_transfer_v3` e ripiega su
  rsync se il server non la supporta.
- **Recovery delle pubblicazioni**: intent persistente `PREPARED/READY/COMMITTED`,
  recupero sotto lease e nessun rollback dopo il commit.
- **Restore batch**: ripristino NAS multiplo con backup, journal, retry idempotente
  e protezioni sui percorsi.
- **Sicurezza web e protocollo**: rifiuto di symlink antenati e validazione dei
  percorsi/operazioni server.

## 1.15.2 - 2026-11-26

Client e server Go:

- **Publishing in batch**: più file vengono pubblicati in una sola transazione
  journal (`FILE_PUBLISH_BATCH_V1`), con rollback di tutti gli spostamenti in
  caso di errore. Molto meno journal e una sola revisione per batch.
- **Web UI utilizzabile**: storico versioni, download di file e versioni via
  `/api/download`, autenticazione Basic opzionale (`WEB_USER`/`WEB_PASSWORD`)
  e avvio automatico nel demone con `WEB_LISTEN`.

## 1.15.1 - 2026-11-26

Client Go:

- **`rsync` rimosso anche da `show` e dal ripristino delle versioni**: questi
  percorsi usano ora lo store a chunk (`--history-chunks` lato NAS), quindi
  il backend `chunks` copre tutti i trasferimenti.
- `doctor` mostra il backend attivo e indica che rsync non è più usato per i
  trasferimenti.

## 1.15.0 - 2026-11-26

Client Go:

- **Trasferimenti senza rsync**: il backend predefinito è il protocollo a chunk
  del demone Go (`transfer_backend=chunks`). Vengono inviati solo i chunk che
  il NAS non ha già, con dedup tra file e versioni; `rsync` resta disponibile
  impostando `transfer_backend=rsync`.
- Il client richiede il demone NAS Go con i verbi a chunk; lo script bash
  non è più sufficiente per i trasferimenti.
- Aggiornato il change feed lato server (`--wait-for-revision`).

## 1.14.1 - 2026-11-26

Client Go:

- **Corretto l'aggiornamento avviato dalla GUI.** Il pulsante *Controlla
  aggiornamenti* mostrava il dialog e accettava la conferma, ma l'installazione
  falliva sempre con `versione server sconosciuta, impossibile verificare
  requires_server`: il client passato all'updater non esponeva `ServerVersion()`
  (solo la CLI lo avvolgeva in un adapter). Ora `remote.Client` implementa
  direttamente l'interfaccia, quindi il controllo `requires_server` funziona
  sia dalla GUI sia dalla CLI. In caso di errore, il dialog di conferma mostra
  anche una notifica nella tray invece di limitarsi a scrivere nella scheda
  Attività.

## 1.14.0 - 2026-11-26

Client Go:

- **Notifiche di sincronizzazione raggruppate.** Con il tempo reale attivo una
  passata può concludersi ogni pochi secondi e ogni completamento apriva un
  popup quasi identico, fino a riempire il desktop. Ora i completamenti vengono
  accumulati e inviati come unico riepilogo dopo 45 secondi di quiete (al più
  ogni 10 minuti durante un'attività ininterrotta), con il numero di
  sincronizzazioni e i totali di caricamenti, download, eliminazioni e
  conflitti del gruppo. Le voci nella cronologia attività restano una per
  passata.

## 3.32.4 - 2026-11-25

Server:

- **`--audit-phantoms [--forget]`**: il controllo simmetrico di
  `--audit-orphans`. Una riga viva nel manifest il cui file non è più sul disco
  resta lì per sempre: il manifest continua a dichiarare file che non esistono
  (un client con la copia locale verrebbe invitato a cancellarla, uno senza non
  riuscirebbe a scaricarla). `--forget` le mette a tombstone nel journal senza
  toccare il disco; i percorsi che esistono come cartella o link sono lasciati a
  un intervento umano.

## 3.32.3 - 2026-11-25

Server:

- **Il manifest non esporta mai un percorso interno**, nemmeno come tombstone:
  una sola riga con `.sync-trash` (anche annidata), `.sync-staging`, `@eaDir`…
  faceva rifiutare il manifest intero a ogni client. Il filtro è nel render,
  quindi vale per export completo, delta e compattazione.

## 3.32.2 - 2026-11-25

Server:

- `--journal-forget` accetta i percorsi interni: sono esattamente quelli che
  deve poter togliere dal manifest, quindi non può usare la validazione delle
  operazioni normali (che li rifiuta). Restano esclusi percorsi assoluti, `..` e
  antenati che sono link simbolici.

## 3.32.1 - 2026-11-25

Server:

- **Le cartelle interne vengono riconosciute a qualsiasi profondità.** Il primo
  giro di `--audit-adopt` ha registrato 3 file dentro `RPG/.sync-trash/…` (una
  cartella di storico che era stata sincronizzata a suo tempo): quei percorsi
  sono interni per i client, che rifiutano il manifest per intero. Ora
  `.sync-trash`, `.nasbox-staging`, `.sync-partial`, `@eaDir` e i canali di
  update sono esclusi come *componente* del percorso, non solo alla radice.
- Nuovo `--journal-forget` (input NUL): mette a tombstone percorsi che il
  manifest non dovrebbe contenere, senza toccare il disco. Usato per riparare i
  3 percorsi adottati per errore.

## 3.32.0 - 2026-11-25

Server:

- **Riallineamento manifest↔disco.** Un file può arrivare nella share senza
  transazione nel journal (backup ripristinato, copia a mano, append perso in un
  crash). Il manifest è scritto solo dal journal, quindi quel file resta
  invisibile: nessun client lo scarica, nessun altro PC lo vede, e un client
  nuovo non lo riceve mai.
  - `--audit-orphans [--list]` elenca i file sul disco senza riga viva nel
    manifest (sola lettura; esclude cestino, staging, canali di update, `@eaDir`
    e i temporanei di rsync).
  - `--audit-adopt [--dry-run]` scrive per loro una transazione con digest,
    dimensione e data reali: diventano file tracciati normali, con versioni e
    cancellazioni, senza spostare un byte. I file scritti negli ultimi 2 minuti
    sono lasciati al giro successivo (potrebbero essere trasferimenti in corso).
  - In più: `exec 9>>... 2>/dev/null` nella lettura del journal silenziava
    stderr per il resto del comando, nascondendo ogni errore successivo.

## 1.13.0 - 2026-11-25

Client Go:

- **Un file che il NAS non conosce viene caricato.** Se un percorso locale non ha
  nessuna riga nel manifest, il pianificatore rispondeva "vince il remoto" e il
  ciclo non faceva nulla: il file restava solo su quel PC, il riepilogo diceva
  "0 operazioni" e un client nuovo non l'avrebbe mai visto. Ora viene caricato
  (se i byte sono già sul disco del NAS la pubblicazione lo adotta senza
  trasferire nulla).

## 1.12.0 - 2026-11-25

Client Go:

- **Le stesse esclusioni su tutti i PC.** Le regole che ignorano file generati
  (build, dist, target, cache di IDE, classi, oggetti, log, LaTeX, venv,
  node_modules...) vivono ora nel codice e non nella configurazione del singolo
  client: due macchine non possono più divergere perché una delle due le ha
  diverse. Resta possibile aggiungere esclusioni locali (es. `sync-daemon/`),
  ma solo in aggiunta.
- **Le voci di stato dei percorsi esclusi vengono potate** (una volta per ogni
  insieme di regole): erano righe che nessuna passata avrebbe più guardato e
  facevano sembrare diversi due stati identici — è così che un buco reale nel
  set tracciato era rimasto nascosto dai numeri. Il riepilogo di `sync` dice
  quante voci sono state rimosse.

## 1.11.1 - 2026-11-25

Client Go:

- `doctor` mostra il rsync che i trasferimenti usano davvero: con il rsync
  privato del NAS stampa `NAS 3.5.1 privato, sistema 3.1.2` invece del solo
  3.1.2 letto dalla shell di login (che resta la versione di sistema).

## 3.31.0 - 2026-11-25

Server:

- **Un rsync più recente accanto al demone, senza toccare quello di sistema.**
  `tools/install_server_rsync.sh` scarica da Entware (sha256 fissati nello
  script) rsync 3.5.1 per aarch64 e lo mette in `server/bin/` con le sue
  librerie e un wrapper. Il demone lo dichiara in `--capabilities`
  (`RSYNC_PATH`, `RSYNC_VERSION=3.5.1`, `RSYNC_SYSTEM_VERSION=3.1.2`,
  capacità `private_rsync`) solo se parte davvero; i client lo chiedono con
  `--rsync-path`, quindi il rsync di DSM resta intatto e nessun altro servizio
  cambia. Se il binario manca o non funziona, non viene dichiarato e i client
  tornano al 3.1.2 come prima. Rimozione: `--remove` dello stesso script.
  Verificato sul NAS: il wrapper risponde 3.5.1 (protocollo 33), `--old-args`
  e `--compress-choice=zstd` disponibili, trasferimenti normali.

## 1.11.0 - 2026-11-25

Client Go:

- **Usa il rsync privato quando il NAS lo dichiara**, con `--rsync-path`: nessuna
  opzione nuova, nessun cambio di comportamento se il NAS non ce l'ha (un NAS
  vecchio continua a funzionare identico). Il percorso è letto dalle capacità
  al bootstrap e usato da trasferimenti, download singoli e ripristini dallo
  storico.

## 3.30.1 - 2026-11-25

Server:

- **Export del manifest in streaming**: `--manifest-export` e
  `--manifest-export-delta` tenevano l'intero manifest renderizzato (11 MB) in
  una variabile di bash, che lo copia due volte: su questo NAS erano ~5 s di
  CPU per export, e il delta viene chiesto a ogni passata che vede un cambiamento
  remoto — era il vero limite del "tempo reale", più del trasferimento. Ora il
  render va direttamente su file: **delta da 6,9 s a 2,2 s**, export completo da
  2,1 s a 1,4 s, uscita identica byte per byte.
- Resta da fare (prossimo passo): un delta *per client* basato su un indice
  revisione→offset del journal. Oggi lo snapshot è uno solo, quindi il client
  riceve spesso il manifest completo invece delle poche righe cambiate.

## 3.30.0 - 2026-11-25

Server:

- **Il NAS dichiara le proprie capacità** con `--capabilities` (e le include in
  `--print-config`): change feed, protocolli di rinomina, core, memoria, tipo di
  dischi, versione di rsync, filesystem, parallelismo dell'hashing. I client se
  ne servono e ogni chiave ha un ripiego, quindi un client vecchio continua a
  funzionare identico.
- **Parallelismo dell'hashing derivato dal NAS**: `--file-states` lanciava
  sempre 8 job in parallelo, troppi per 2 core e pochi per 16. Ora è un job per
  core (massimo 8) e si può forzare con `FILE_STATES_PARALLELISM` in
  `server.conf` (1-64, valore non valido rifiutato all'avvio).

## 1.10.0 - 2026-11-25

Client Go:

- **Il sync in tempo reale segue le capacità del NAS**: il change feed
  (long-poll su `manifest.revision`, 4-6 s misurati su dati veri) resta attivo
  quando il NAS lo dichiara; se non lo dichiara si usa il timer normale. Nuova
  impostazione `realtime` con tre valori: automatico (default), sempre (se il
  NAS non sa avvisare controlla ogni minuto) e disattivato.
- **Risparmio energetico solo dove serve**: con `power_saving: auto` il client
  smette di rallentare il change feed quando il NAS dichiara dischi a stato
  solido; con dischi rotanti o tipo sconosciuto resta il comportamento di prima.
- **`doctor` mostra lo stato vero**: una riga `tempo reale:` (attivo con change
  feed / senza change feed / disattivato) e una `capacità NAS:` con core,
  memoria, dischi, rsync e filesystem letti dal NAS.

## 3.29.0 - 2026-11-25

Server:

- **Le cartelle rimaste vuote non restano più sul NAS.** Cancellare una
  cartella da un PC svuotava i file ma lasciava le cartelle nella share (i
  client non creano mai directory sul NAS: esistono solo come genitori di un
  file). Ora dopo una cancellazione riuscita, e dopo una cancellazione dal
  browser (`--browse-delete`), i genitori rimasti vuoti vengono rimossi
  risalendo la gerarchia: `rmdir` non tocca una cartella che contiene ancora
  qualcosa, quindi un trasferimento in corso o un file di un altro PC restano
  al loro posto, e cestino e staging sono esclusi.

## 1.9.0 - 2026-11-25

Client Go, logica di sincronizzazione:

- **Una rinomina fatta su un altro PC non si riscarica più.** Il journal arriva
  come `DELETE vecchio` + `PUT nuovo`; quando digest e dimensione coincidono con
  un file presente in locale sotto il vecchio nome, il pull lo sposta con un
  rename locale invece di scaricare di nuovo gli stessi byte. Vale per cartelle
  intere (una cartella da 2.000 file rinominata altrove = 2.000 rename locali,
  zero trasferimenti) e per i file singoli. Se qualcosa non torna — byte
  diversi, file modificato dopo la scansione, destinazione già occupata — resta
  il percorso normale di download e cancellazione, quindi non si perde nulla.
- La cartella vecchia non resta più come guscio vuoto nemmeno sul PC che segue
  una rinomina, e il riepilogo del comando `sync` riporta gli spostamenti
  applicati in locale (`rinominati in locale N`).

## 3.28.0 - 2026-11-25

Server:

- **`--checked-delete` in blocco**: un solo append al journal e un solo
  avanzamento della revisione per richiesta, invece di uno per file (due fsync
  a file: il costo reale su un NAS piccolo). Ogni file resta spostato sotto una
  propria transazione e un crash prima del commit si recupera file per file.
  Tolta la riscrittura inutile dello stadio "MOVED" e le chiamate `mkdir` ripetute.
  Misurato: 300 file in 3,7 s contro 9,6 s anche su un SSD veloce (sul NAS il
  guadagno è maggiore, dominano gli fsync). Se il journal rifiuta il blocco, tutti i
  file tornano al loro posto.
- **`--browse-rename` V3**: rinomina un'intera cartella in una richiesta, con
  digest/dimensione/data già noti al client: il NAS non rilegge né calcola
  l'hash di ogni file, controlla solo dimensione e data e che la cartella
  contenga esattamente quei file.
- **Corretto il controllo della data nella V2**: confrontava secondi con
  nanosecondi e rifiutava ("la sorgente è cambiata") i file con data
  sub-secondo nel manifest, cioè quelli caricati da un client.

## 1.8.0 - 2026-11-25

Client Go, logica di sincronizzazione:

- **Rinomina di cartelle in una sola richiesta.** Una cartella da 2.000 file
  rinominata era 2.000 richieste SSH, ognuna con il lock preso e rilasciato.
  Ora è una (con ripiego automatico sui rinomina per file se il server è più
  vecchio, la cartella è cambiata o la destinazione è occupata).
- **Cartelle vuote rimosse** sugli altri PC quando una cartella viene rinominata
  o eliminata altrove (prima restava il guscio vuoto).
- **Cancellazioni sul NAS più veloci** grazie al server 3.28.0 (vedi sotto);
  il client non cambia.
- **I mirror non svegliano più il motore.** Prima, ogni 5 minuti, il timer dei
  mirror faceva partire una passata completa anche senza modifiche. Ora i
  mirror vengono eseguiti e, se rsync non ha cambiato nulla, la passata non
  parte affatto.
- **Base del download corretta.** Se un file cambia sul NAS tra il manifest e il
  download, la base ricordata è l'hash reale dei byte arrivati (controllo
  economico su dimensione e data; l'hash si calcola solo se non coincidono),
  non quello vecchio del manifest.

## 1.7.0 - 2026-11-25

Client Go, installazione e avvio più semplici:

- **Il NAS si trova da solo.** Nel primo avvio il client cerca in rete (mDNS,
  come fanno Synology e QNAP, più una ricerca di host con SSH nella sottorete)
  e mostra i NAS trovati: un tocco e l'indirizzo è compilato. Se un solo NAS
  si annuncia da solo, viene proposto già scritto. Da terminale:
  `nasbox-go find-nas` (5 secondi, esclude il PC stesso).
- **Chiave SSH automatica.** Se il NAS risponde ma non conosce questo PC, il
  primo avvio chiede la password del NAS *una volta*, crea la chiave
  (ed25519) se manca, la installa in `authorized_keys` correggendo i permessi
  della cartella home (causa tipica del rifiuto su Synology), verifica che il
  login con chiave funzioni e dimentica la password: non viene salvata né
  passata in argomenti.
- **Prima sincronizzazione grande con avanzamento vero**: durante l'analisi
  iniziale il banner dice "Analizzo i file di questo PC: 23.400 controllati ·
  1.200 letti (3,4 GB)" invece di un generico "in corso".
- **Client Python ritirato.** `tools/publish_client.py` rifiuta di pubblicare
  senza `--i-know-it-is-retired`; tolta dalle Impostazioni la voce del canale
  Python. La cartella del canale sul NAS è stata archiviata fuori dalla share.
- **Diagnostica passate**: ogni passata registra quali percorsi locali l'hanno
  avviata, per individuare cosa le fa partire a raffica.

## 1.6.1 - 2026-11-25

Client Go:

- **Niente più passate a raffica.** Se il NAS rifiutava la cancellazione di
  alcuni file (cambiati nel frattempo), il client li riproponeva a ogni passata
  e, con il proseguimento automatico delle cancellazioni, rilanciava subito
  un'altra passata ogni secondo circa. Ora un file non cancellabile va in
  «In attesa» con le pause crescenti (1, 2, 5, 15, 60 minuti) e il
  proseguimento riparte solo se è stato fatto progresso.
- **Stato per file nel gestore file**: i file in attesa mostrano l'emblema di
  problema (Nautilus, Nemo, Caja…) e lo perdono appena passano.

## 1.6.0 - 2026-11-25

Client Go:

- **File eliminati ripristinabili in un clic.** File › Storico / Cestino ora
  si apre su «File eliminati di recente»: i file spariti da questo PC, l'ultima
  copia di ciascuno, con "Eliminato · ieri 18:05 · 1,5 MB". Un menu permette di
  passare a «Tutte le versioni precedenti». Dopo il ripristino un messaggio
  conferma e offre «Mostra cartella».
- **Primo avvio in tre passi**: dove tieni i file (cartella proposta
  `~/NASBox`), collegati al NAS (solo indirizzo e utente: porta, cartella e
  script del server vengono trovati da soli e restano sotto «Impostazioni
  avanzate»), tutto pronto. Se il collegamento fallisce il messaggio dice cosa
  controllare (accesso SSH, NAS spento, chiave diversa).

## 1.5.0 - 2026-11-25

Client Go, più pratico e più vicino a Dropbox:

- **Ricerca file immediata** (File › Cerca, pulsante «Cerca» in alto, voce nel
  menu della tray e **Ctrl+F** da qualsiasi schermata). Cerca per nome o
  cartella su tutti i file sincronizzati mentre scrivi, senza interrogare il
  NAS: più parole = tutte devono comparire, gli accenti non contano
  ("perche" trova "Perché"), i match nel nome vengono prima di quelli nella
  cartella, a parità vince il file modificato più di recente. Per ogni
  risultato: **Apri**, **Mostra cartella**, **Versioni**, **Copia percorso**.
- **«Apri cartella»** sempre a portata di mano nell'intestazione.
- **Attività recente apre il file** con un clic (le serie di file aprono la
  cartella); il tooltip dice cosa fa il clic.
- Corretto un crash raro nei test del selettore cartelle (aggiornamento UI a
  finestra già chiusa).

## 1.4.4 - 2026-11-25

Client Go:

- **Stato: «Sincronizza» e «Pausa» non compaiono più due volte.** Erano
  nell'intestazione e di nuovo nel banner. Il pulsante del banner resta solo
  quando porta altrove (Trasferimenti, Log, Impostazioni).

## 1.4.3 - 2026-11-25

Client Go:

- **«Pulisci storico» non faceva nulla — corretto.** Le date delle versioni nel
  cestino ("…-851296Z", con i microsecondi) non venivano lette (il formato
  Go non accetta i microsecondi dopo un "-"), quindi la pulizia le scartava
  tutte in silenzio, anche quella automatica giornaliera. Ora le date sono
  lette correttamente (anche l'ordinamento dello storico).
- **La pulizia dice cosa ha fatto**: scelta tra "solo le versioni più vecchie
  di N giorni" e "tutto lo storico", con numero di versioni e spazio; al
  termine un riepilogo ("Rimosse 120 versioni, liberati 3,4 GB") oppure il
  motivo per cui non c'era nulla da rimuovere.

## 1.4.2 - 2026-11-25

Client Go:

- **Finestra stretta anche nell'app vera.** Sul PC con la cronologia reale la
  Stato imponeva ancora 1385 px: la riga "Storico versioni sul NAS: … quota
  piena: …" non andava a capo e le due schede affiancate raddoppiavano la
  larghezza. Ora le etichette delle schede NAS vanno a capo; minimo misurato
  nell'app in esecuzione: 574 px (finestra iniziale 980×700).

## 1.4.1 - 2026-11-25

Client Go:

- **Controllo dei nomi già presenti sul NAS senza calcolare hash.** Per capire
  se un rename ha come destinazione un file esistente il client faceva
  calcolare lo SHA-256 di migliaia di file al NAS (minuti, con il lock
  tenuto). Ora basta un controllo di esistenza (secondi); l'hash serve solo
  per i file effettivamente già presenti.
- **Finestra davvero meno larga.** Sul PC reale la pagina Stato imponeva circa
  1300 px di larghezza minima (il testo "Audit completo: mai eseguito" nella
  tessera dell'audit, ripetuto su quattro tessere uguali): la finestra si
  allargava a 1634 px. Ora il valore è breve e troncato, e la larghezza minima
  della Stato è circa 570 px. Strumento di diagnostica: build con
  `-tags debugsize` e `NASBOX_UI_DEBUG_MINSIZE=1` stampa la catena di widget
  che impone la larghezza minima.

## 1.4.0 - 2026-11-25

Client Go, "come Dropbox": un file non ferma più gli altri.

- **Errori per singolo file, mai per passata.** Un upload o download che fallisce
  viene isolato dividendo il blocco a metà finché si trova il file
  responsabile; tutti gli altri passano. Il file problematico entra in
  "In attesa" e viene riprovato da solo dopo 1, 2, 5, 15, 60 minuti. Errori
  globali (rete, lock di un altro PC, disco pieno) continuano a fermare la
  passata e a essere ritentati.
- **File illeggibili o in uso** (permessi, documento aperto che cambia) non
  interrompono più la scansione: vengono tenuti fermi (mai caricati e mai
  scambiati per cancellati) e riprovati.
- **La protezione cancellazioni ferma solo le cancellazioni.** Upload, download
  e rename continuano; prima una cartella duplicata eliminata a mano congelava
  l'intera sincronizzazione.
- **Scelta in un clic** per le cancellazioni trattenute: «Elimina anche dal
  NAS» (resta nello storico) o «Ripristina qui» (riscarica i file). Si vede
  quanti file e in quali cartelle.
- **Nuova tab Attività › In attesa** con un motivo in italiano per ogni file e
  «Riprova ora»; badge sul menu e scheda nello Stato.
- **rsync exit 24** (file sparito durante il trasferimento) non è più un errore.
- **Messaggi di stato senza gergo tecnico**: "Il NAS non risponde…" invece di
  "ssh: exit status 255", "Serve una tua scelta" invece di "Bloccato".

## 1.3.0 - 2026-11-25

Client Go:

- **Corretto "Offline / ssh exit status 1" che bloccava ogni sincronizzazione.**
  Il rilevamento dei rename considerava come destinazione anche un file che il
  NAS già conosceva (una copia identica in un'altra cartella, es.
  `SADE/images` accanto a `SADE/SADE/images`). Il server rifiutava lo
  spostamento con exit 1 e la passata veniva interrotta a ogni ciclo. Ora una
  destinazione già tracciata non è un rename: i file mancanti passano dalla
  normale protezione cancellazioni (conferma). Il rifiuto del NAS riporta il
  motivo e non viene più scambiato per "offline": Offline solo per problemi
  di connessione (ssh 255, timeout, host non raggiungibile).
- **Un rifiuto del NAS non ferma più la sincronizzazione.** Se il NAS rifiuta
  un singolo rename (destinazione già presente, sorgente cambiata dopo il
  manifest) il file viene caricato col nuovo nome e il vecchio passa dalle
  normali protezioni cancellazioni (con copia nello storico). I file che il
  NAS ha già su disco ma il manifest non conosce vengono adottati nel journal
  (se identici, senza ritrasferirli) o sostituiti sotto lease (se diversi)
  invece di far fallire `staging-publish` a ogni passata.
- **Log leggibili**: frasi complete ("Scaricato dal NAS: …", "Il NAS ha
  rifiutato un'operazione…"), icone per livello, orario breve, voci ripetute
  raggruppate ("19.445 file scaricati in …") e filtro predefinito "Eventi
  principali" che nasconde la diagnostica (PERF, DOCTOR).
- **Sfoglia NAS ordinabile**: cartelle prima, file prima, nome A→Z / Z→A,
  dimensione, data. La scelta viene ricordata; compare anche la data.
- **Tooltip veri** su pulsanti, voci del menu, tessere dello stato e righe
  recenti (nuvoletta vicino al puntatore, non intercetta i clic).
- **Finestra meno larga**: larghezza minima da circa 1210 a circa 810 px e
  dimensione iniziale 980×700. Le barre di pulsanti vanno su più righe e le
  liste hanno colonne più strette.
- **Scheda NAS più stretta**: contenuto limitato a 760 px, tessere su quattro
  colonne.
- **Impostazioni**: lingua, tema e backend su un'unica riga; i parametri in
  due colonne (le righe lunghe, come la pausa programmata, restano a tutta
  larghezza). Il backend è stato spostato da Avanzate.

## 1.2.1 - 2026-11-25

Client Go:

- **Attività recente più compatta**: le righe non hanno più il doppio
  padding (circa 33 px l'una invece di 48) e niente spazio extra tra una e
  l'altra.

## 1.2.0 - 2026-11-25

Client Go, interfaccia e avanzamento (dalla revisione degli screenshot della 1.1.1):

- **Avanzamento dei trasferimenti coerente.** Prima la barra seguiva la stima di
  rsync (79%) mentre il contatore dei file diceva 53% e il tempo residuo
  implicava circa metà dei byte ancora da fare. Ora il client conosce la
  dimensione pianificata di ogni batch: percentuale, velocità (media mobile) e
  tempo residuo derivano dagli stessi byte, la percentuale non torna mai
  indietro e resta al 99% finché il batch non termina. Il contatore "N di M
  file" usa `xfr#N` di rsync (file realmente completati) invece dei nomi
  stampati, che andavano un file avanti. Il dettaglio mostra "35,4 GB di 66,1 GB".
- **Il file corrente non resta più indietro.** Ogni file faceva scrivere
  `events.jsonl` in modo sincrono e ricostruire i widget, nello stesso thread
  che legge rsync: con 25 file/s l'interfaccia accumulava ritardo (e rallentava
  rsync). Ora i file sono accodati e applicati a 4 Hz; l'ultimo
  aggiornamento di una raffica viene sempre disegnato.
- **Attività recente compatta e raggruppata**: righe da una riga, e una serie
  di file dello stesso tipo e cartella diventa una voce ("1.240 file
  scaricati · SADE/SADE/images"). La tab Trasferimenti ha lo stesso blocco di
  avanzamento della Stato (con barra).
- **Impostazioni**: i campi non si allungano più fino all'altezza della
  descrizione (box da 90 px per scrivere "22"); numeri e scelte sono compatti,
  righe separate, descrizioni piccole. Unità corrette: i limiti di banda sono
  KB/s (l'etichetta diceva kbps). Esclusioni: parte dalla prima riga.
- **Scheda NAS**: schede di altezza naturale (la griglia uniforme le faceva
  alte quanto la più alta), valore sotto l'etichetta, grafici a tutta
  larghezza con riga di dettaglio (iowait, carico 5/15 min, rete) al posto dei
  riquadri duplicati, barra disco senza percentuale sovrapposta, versione del
  server sempre visibile (non più "—"), "Questo PC" nei dispositivi, "Assegna"
  attivo solo con un dispositivo selezionato, tab con scorrimento proprio.
- **Storage**: "Non ancora analizzato" invece di "Nessun file duplicato" prima
  dell'analisi; barra visibile solo durante l'analisi.
- **Stato**: separatori delle migliaia, plurale corretto, "Differenze audit"
  (il numero è quello delle differenze), avviso a quota storico piena,
  percentuale e velocità nella barra laterale durante il trasferimento,
  pulsanti "Ripristina eliminati" e "Ferma trasferimento" non più ambigui.
- Intestazioni delle tab non più ripetute; margini a destra delle pagine
  scorrevoli; separatore sotto le tab più leggero.

## 1.1.1 - 2026-11-25

Client Go:

- **I link simbolici e i file speciali non fermano più la sincronizzazione.**
  La scansione locale li tratta come fa il client Python (ignorati, mai
  caricati, mai interpretati come cancellazioni) e li riporta una volta nel log
  come `UNSUPPORTED`. Prima il primo link simbolico nell'albero bloccava
  l'intera passata (`percorso locale non regolare, sync interrotto`), cosa che
  accadeva per esempio sui backup della configurazione di sistema.
- `nasbox-go sync` indica quanti percorsi non regolari ha ignorato.
- Resta fail-closed sui file regolari illeggibili o che cambiano durante la
  lettura.

## 3.27.1 - 2026-11-25

- Server: con `--dry-run` il riepilogo della quota dello storico dice
  "verrebbero rimosse" invece di "rimosse".

## 1.1.0 - 2026-11-25

Client Go, efficienza (misurata sul PC con 119.000 file e sul DS420j):

- **Passata di sync da circa 10 s a circa 0,6 s.** Le regole di esclusione
  venivano valutate con ~40 `path.Match` per file e per cartella, tre volte
  per passata (5,2 s ogni 120.000 file): ora sono compilate una volta
  (0,18 s, risultato identico, verificato contro l'implementazione precedente).
- **Niente più passata completa ogni 5 minuti**: il timer dei mirror forzava
  manifest completo e scansione di tutto l'albero anche senza mirror
  configurati. Senza inotify il ripiego forzava una passata completa ogni
  2 secondi: ora ogni 60.
- **Scansione locale incrementale** con inotify: si riesaminano solo i
  percorsi cambiati. Sicurezza: ogni file che risulterebbe sparito viene
  verificato su disco (altrimenti scansione completa), scansione completa
  comunque ogni 30 minuti (`full_scan_interval_seconds`) e quando inotify non
  sorveglia tutte le cartelle.
- Osservazioni locali lette con una sola query invece di una per file.
- **Risparmio energetico NAS** (`power_saving`: off/auto, Impostazioni ›
  Sincronizzazione): dopo 15 minuti senza attività il client smette di
  interrogare il NAS di continuo (controllo ogni `power_saving_poll_minutes`,
  default 10) e tiene aperta una sola connessione SSH, così i dischi possono
  andare in standby. Una modifica locale riattiva subito la sync.
- Il monitor NAS non interroga il NAS quando la finestra è chiusa nella tray.
- Comandi pesanti sul NAS (`du`, storico, hash) a bassa priorità (`nice`).
- Eventi `PERF` per fase della passata nel log, per diagnosticare i
  rallentamenti sui PC reali.
- Storico versioni del NAS (occupazione e limite) nella scheda "Il tuo NAS" e
  in "Controlla il sistema", con verifica che il demone del server sia attivo.

## 3.27.0 - 2026-11-25

Server:

- **Dischi a riposo**: i file di lock venivano aperti con troncamento
  (`exec 9>file`), aggiornando la data di modifica a ogni comando anche di
  sola lettura: i dischi non potevano andare in standby. Ora in append (21
  aperture), nessuna scrittura senza modifiche.
- **Quota dello storico** `TRASH_MAX_MB` (0 = nessun limite): oltre la soglia
  si rimuovono prima le versioni più vecchie, mai quelle più recenti di
  `TRASH_MIN_KEEP_DAYS` (default 7). L'occupazione viene registrata a ogni
  manutenzione e pubblicata da `--print-config` (`TRASH_USED_BYTES`,
  `TRASH_FILES`) senza ripercorrere l'albero.
- La manutenzione gira a bassa priorità (`renice`).

## 1.0.0 - 2026-11-24

NASBox Go diventa il client definitivo ("NASBox"):

- **Integrazione con il file manager**: voce "NASBox: versioni precedenti" nel
  clic destro di Thunar, Nautilus, Nemo, Dolphin e Caja (Impostazioni ›
  Generale o `nasbox-go integrate`, reversibile; `uca.xml` di Thunar salvato
  prima della modifica). Emblema di stato sulla cartella NASBox e sulle copie
  di conflitto.
- **Controllo locale** (socket utente): riaprire NASBox porta in primo piano
  la finestra esistente; `nasbox-go versions <file>`, `show`, `status`.
- **Sync selettiva sull'albero del NAS** con dimensioni calcolate sul NAS. Le
  cartelle escluse restano visibili e reincludibili (prima l'elenco era quello
  locale: una cartella esclusa spariva e non si poteva più includere).
- **Corretto: esclusioni selettive** ancorate e complete. `Documenti/Progetti`
  escludeva solo la voce della cartella, non i file al suo interno; `Foto`
  escludeva anche `Lavoro/Foto`. Test end-to-end dedicato.
- **Avvio senza NAS**: un PC già configurato parte anche con il NAS
  irraggiungibile (stato "NAS non raggiungibile", nuovi tentativi in
  background) invece di non aprirsi.
- **Installazione**: `install.sh` (utente, menu, avvio automatico, `--migrate`
  dal client Python, `--uninstall`) e `packaging/build-deb-go.sh` (pacchetto
  `.deb` senza bisogno di `dpkg-deb`). Con installazione da pacchetto
  l'aggiornamento passa dal gestore pacchetti.
- README riscritto: funzioni, requisiti, migrazione dal Python, CLI, sviluppo.

## 0.21.0 - 2026-11-24

Client Go, parità completa con il client Python:

- **Aggiornamento automatico del client**, come il Python: controllo 2 minuti
  dopo l'avvio e ogni 6 ore, installazione solo quando non ci sono
  trasferimenti in corso, notifica e riavvio automatico (rollback se la nuova
  versione non parte). Disattivabile con `go_auto_update=false`.
- **Note di rilascio**: il manifest Go contiene la sezione del CHANGELOG della
  versione (estratta da `publish_go_client.py`); "Controlla aggiornamenti"
  la mostra prima di installare.
- **Aggiornamento del server dal client**: `sync-daemon-server-<versione>.sh`
  accanto allo script attivo viene proposto (mai una versione più vecchia),
  installato dopo conferma, il demone riavviato e la versione verificata; lo
  script precedente resta come `.prev`. Comandi CLI `server-update-check` e
  `server-update-install`. Il client notifica i nuovi server disponibili.
- **Versioni del file**: per ogni file, un'unica lista delle versioni salvate
  su questo PC e sul NAS, ripristinabili con un clic (da File › Sfoglia NAS e
  da Storico). Il ripristino non cancella mai la versione attuale: finisce a
  sua volta nello storico. Se il file esiste, la versione del NAS viene
  scaricata e poi caricata come modifica normale (il server rifiuta di
  ripristinare sopra un file esistente).
- **Grafici del NAS**: andamento di CPU, carico e rete (ultimi 120 campioni)
  nel monitor NAS.
- Nuovi test end-to-end: aggiornamento del server e ripristino dallo storico.

## 0.20.0 - 2026-11-24

Client Go, affidabilità:

- **Test end-to-end con lo script server vero.** Il binario `nasbox`, rsync e
  `sync-daemon-server.sh` girano contro una share temporanea con due PC
  simulati (ssh finto locale): creazione, modifica, download, cancellazioni,
  conflitti concorrenti, cancellazioni di massa e modifiche fatte direttamente
  sul NAS. Girano con `go test ./...`.
- **Le cancellazioni arrivano agli altri PC.** Le tombstone del manifest
  venivano scartate prima della pianificazione: un file cancellato su un PC
  restava per sempre sugli altri. Ora il file locale viene spostato nel
  cestino locale (sempre recuperabile); oltre `max_delete_files` la sync si
  blocca senza toccare nulla.
- **Nessuna perdita della versione locale nei conflitti.** Quando vince la
  versione del NAS, il download conserva prima la copia locale nello storico
  locale (`rsync --backup`, come il client Python).
- **Modifiche fatte direttamente sul NAS** (SMB, File Station): se la
  sostituzione di un file viene saltata perché il NAS contiene una versione
  sconosciuta al manifest, quella versione viene registrata nel journal e il
  passaggio successivo applica la regola dei conflitti, invece di saltare il
  file all'infinito.
- Storico locale: `MoveToTrash` ripeteva la cartella nel percorso
  (`trash/a/a/b.txt-…`), quindi lo storico mostrava l'originale sbagliato.
- "NAS occupato" non è più un errore: evento `LOCK_DEFERRED` (avviso), stato
  "Il NAS è occupato da un altro PC: riprovo tra poco".
- `RunLocked` lasciava il file owner del lock con fase "checking" (la `trap`
  non scattava per via di `exec`): diagnostica fuorviante.
- **Controlla il sistema** (Impostazioni › Informazioni): cartella locale e
  spazio, associazione al repository, server e journal NAS, rsync locale e
  del NAS, rilevamento modifiche (inotify), errori delle ultime 24 ore; per
  ogni problema indica cosa fare. `doctor` mostra le versioni di rsync.
- Con rsync locale < 3.1 il client rinuncia al progresso in tempo reale invece
  di fallire. La CLI `nasbox sync` elenca i file trasferiti.

## 0.19.0 - 2026-11-24

Client Go, nuova interfaccia e feedback in tempo reale:

- **Interfaccia ridisegnata.** Barra laterale con icone ed evidenziazione,
  colore d'accento blu, schede pulite, tema chiaro e scuro coerenti. Corretto
  alla radice il difetto che disegnava pulsanti senza sfondo (testo bianco su
  bianco) e voci di menu tagliate: `hintButton` ora è un vero pulsante Fyne.
- **Home in stile client di sync:** grande stato con icona semantica e azione
  suggerita, riquadri numerici cliccabili, "Attività recente" con i file
  caricati/scaricati e scheda "Il tuo NAS" (disco, server, dispositivi).
  Le azioni rare sono raccolte in "Altre azioni"; "Conferma cancellazioni"
  compare solo quando una guardia ha bloccato la sync.
- **Trasferimenti in tempo reale:** file corrente, "file 3 di 12",
  percentuale, velocità e tempo stimato, letti da rsync
  (`--info=progress2,name1`, solo opzioni di output locali, compatibili con
  rsync 3.1.2 del NAS). La scheda Attività › Trasferimenti mostra lo storico
  dettagliato; clic su un file apre la sua cartella.
- **Tray completa:** stato in tempo reale in cima al menu, file recenti,
  pausa 15 min/30 min/1 h/4 h/8 h/fino a domani e riprendi, conflitti da
  rivedere, collegamenti rapidi. Notifiche per blocchi, NAS non raggiungibile
  e nuovi conflitti, senza ripetizioni.
- Barra laterale con stato e "Ultima sincronizzazione: 2 min fa"; badge con
  il numero di conflitti sulla voce File.
- I file trasferiti vengono registrati in `events.jsonl`, così la cronologia
  sopravvive ai riavvii ed è condivisa con il client Python.
- Le schermate si possono renderizzare senza display (renderer software di
  Fyne) per la revisione grafica: `NASBOX_UI_CAPTURE=<dir>`.

## 0.18.1 - 2026-11-24

Client Go, correzione urgente:

- **Trasferimenti di nuovo funzionanti con il NAS.** Il client Go passava
  `--mkpath` a rsync; l'opzione viene inoltrata al rsync del NAS (3.1.2 su
  Synology), che la rifiuta (`--mkpath: unknown option`): da 0.18.0 nessun
  upload né download riusciva. Con `--files-from` le cartelle intermedie sono
  già create da rsync; per il download singolo la cartella locale viene creata
  dal client. Test di regressione che vieta le opzioni sconosciute a rsync 3.1.2.
- **Modifiche a file già presenti sul NAS di nuovo caricate.** Il client Go
  mandava ogni upload nello staging, ma `staging-publish` crea solo file nuovi
  e rifiutava l'intero blocco se un file esisteva già. Ora, come nel client
  Python, i file esistenti vengono sostituiti sotto il lease NAS: verifica che
  sul NAS ci sia ancora la versione attesa (altrimenti il file viene saltato e
  ripianificato), rsync diretto con la versione precedente conservata in
  `.sync-trash` (storico NAS ripristinabile) e journal con lo stato reale del
  NAS. I file nuovi continuano a usare lo staging atomico.
- **Lease NAS rilasciato davvero.** `AcquireLease` (usato da mirror, browse e
  sostituzioni) teneva il lock con `sleep 3600` e lo rilasciava terminando il
  processo ssh locale: attraverso la connessione SSH multiplexata la sessione
  remota sopravviveva e il NAS restava bloccato fino a un'ora per tutti i PC.
  Ora usa `--transfer-wait` del server come il client Python (coda equa, file
  owner gestito dal server) e rilascia chiudendo stdin; attesa massima 45 s,
  poi il lease viene richiesto di nuovo alla passata successiva.
- Updater: un binario avviato da dentro una cartella NASBox sincronizzata (per
  esempio direttamente dal canale `.nasbox-client-update-go`) non scrive più
  stato e lock accanto a sé e rifiuta l'auto-aggiornamento: quei file finivano
  sul NAS e su tutti i PC.
- `doctor` mostra solo gli errori delle ultime 24 ore, raggruppando le
  ripetizioni: gli errori storici del client Python non sembrano più guasti in
  corso.
- `tools/publish_go_client.py` conserva solo le ultime versioni nel canale
  (`--keep`, default 2) e rimuove file spuri (lock e stati dell'updater).

## 0.18.0 - 2026-11-24

Client Go, parità con il client Python 1.35.2:

- Storage insights nel client Go: cartelle più grandi, duplicati con SHA-256 e
  riepilogo del cestino locale, con scansione limitata e conteggio degli errori.
- Pulizia guidata dei duplicati: selezione per gruppo, file "Mantieni" protetto,
  spostamento nel cestino locale con ripristino.
- Monitor NAS completo: CPU e attesa I/O, rete, I/O disco, swap, uptime,
  staging, journal/manifest e giudizio di salute.
- Log filtrabile per categoria, ricerca testuale e ricarica dagli eventi
  persistenti condivisi con il client Python.
- Anteprima modifiche strutturata con cartelle, anomalie, spostamenti e
  cancellazioni sospette.
- Pausa programmata con giorni e finestre notturne (corretto il salvataggio che
  perdeva i giorni) e pausa temporizzata `pause_until`.
- Setup guidato al primo avvio: cartella locale, SSH, rilevamento dello script
  server e verifica della connessione.
- Tray con notifiche di completamento e collegamenti a conflitti, storico, log e
  impostazioni; editor delle esclusioni per pattern.
- Change feed NAS (`--wait-for-revision`) con manifest delta, così una modifica
  di un altro PC arriva subito invece di attendere il polling.
- Nuovi test Go per storage, metriche, pianificazione, change feed, filtri log e
  nuove schermate. Stato e lacune residue in `client-go/PARITY.md`.

## 3.26.0 - 2026-11-24

- `restore-batch` transazionale con marker `STARTED`/`MOVED`, recovery idempotente
  e rollback journal/revisione; restore ripetuto non duplica il PUT.
- Ordine journal/revisione durable con `sync` esplicito e backup rollback.
- `log_event()` ora escape correttamente newline, tab e caratteri di controllo
  nei nomi file, mantenendo `events.jsonl` JSON valido.
- Nuovi test di crash, bump failure, path con caratteri di controllo.

## 1.35.2 - 2026-11-24

- Correzione critica di sicurezza: un file locale illeggibile, non regolare o
  instabile durante l'hash non viene più interpretato come assente. La
  sincronizzazione si interrompe in modo sicuro invece di propagare una
  cancellazione remota.
- Aggiunti test per permessi negati e symlink nel percorso di un file tracciato.

## 1.35.1 - 2026-11-24

- Backfill dell'identità filesystem dell'intero albero locale (solo `stat`,
  nessun hashing): la classificazione riconosce gli spostamenti di cartelle
  anche per file mai inviati in precedenza, riducendo i falsi positivi nella
  guardia alle cancellazioni.

## 1.35.0 - 2026-11-24

- **Anteprima modifiche**: quando una cancellazione viene bloccata, lo Stato
  mostra il pulsante "Anteprima modifiche" con una tabella per cartella (file
  coinvolti e presenti sul NAS), le cartelle anomale evidenziate, esempi di
  spostamenti e cancellazioni, e pulsanti per aprire il Log o confermare.
- L'anteprima è calcolata sul PC, in sola lettura: nessun nuovo lavoro per il
  NAS ARM.

## 1.34.1 - 2026-11-24

- Timer `PERF` nelle operazioni che possono causare picchi CPU (scan completa,
  export del manifest, pianificazione dei blocchi push, manifest dell'audit):
  il log registra solo le operazioni oltre 0,5 secondi, senza overhead nelle
  sync normali. Serve a misurare prima di ottimizzare, non a cambiare il motore.

## 1.34.0 - 2026-11-24

- **Undo stile Rewind**: le cancellazioni recenti vengono registrate nel DB
  locale con la loro versione nel cestino; il pulsante "Annulla eliminazioni
  recenti" ripristina i file dal NAS in batch streaming, senza nuovi servizi.
- Nuovo verbo server `--restore-batch` (3.25.0): sposta i file dal cestino al
  loro percorso e aggiorna journal e manifest, sempre con il lock globale e con
  memoria costante (adatto al NAS ARM da 1 GB).
- L'anteprima di sicurezza include ora le informazioni necessarie a capire se
  una cancellazione è un errore e come annullarla.

## 1.33.0 - 2026-11-24

- Intelligenza lato client nella guardia alle cancellazioni: i percorsi spiegati
  da un rename non vengono più contati come eliminazioni e una cartella che
  perde più della metà dei propri file viene bloccata con una spiegazione
  dedicata, invece del solo conteggio globale.
- Identità filesystem (`dev`, `ino`, `ctime`, dimensione, digest) salvata nel DB
  locale senza hashing aggiuntivo, usata per riconoscere spostamenti di cartelle
  e unità non montate.
- Sentinelle opzionali: se un file segnaposto manca nella cartella locale, la
  sincronizzazione distruttiva viene rifiutata perché l'albero non è quello
  atteso.
- Anteprima delle modifiche nel messaggio di blocco: categorie, conteggi per
  cartella e rename esclusi, con il dettaglio mostrato anche nella guida "Cosa
  fare ora".
- Nessun nuovo processo o database sul NAS: tutta la classificazione gira sui PC.

## 1.32.0 - 2026-11-24

- Messaggi di stato guidati: il banner dice il motivo specifico (blocco di
  sicurezza, coda, conflitti, checksum mai verificato) e mostra "Cosa fare ora"
  con il pulsante per aprire Log, Trasferimenti o Conflitti, oppure per avviare
  la verifica dei checksum.
- I blocchi di sicurezza ora indicano quanti file verrebbero cancellati, che
  nessun file è stato modificato e come distinguere un errore da una
  cancellazione intenzionale; i messaggi seguono la lingua dell'interfaccia.
- "Conferma cancellazioni di massa" compare solo quando una cancellazione è
  realmente bloccata e il suo tooltip avvisa che i file verranno rimossi anche
  dal NAS.
- La Panoramica aggiunge una riga "Cosa fare" sotto il motivo dell'attenzione.
- Corretta la pubblicazione del client: i moduli dei messaggi si importano anche
  senza PyQt6, come negli strumenti headless sul NAS.

## 1.31.0 - 2026-11-23

- Audit esteso ai problemi di sincronizzazione: coda pending con voci stale,
  file del baseline mancanti in locale, errori journal e percorsi esclusi.
- Stato "Attenzione" con motivo leggibile nella Panoramica e nello Stato.

## 1.30.1 - 2026-11-23

- Audit e verifica checksum rispettano le esclusioni di sincronizzazione: le
  differenze in cartelle come `sync-daemon/` non vengono più conteggiate.
- I report di audit precedenti con sole differenze escluse sono considerati
  puliti senza bisogno di rieseguire l'audit.
- La tab Panoramica mostra il motivo specifico dello stato di attenzione.

## 1.30.0 - 2026-11-23

- Nuova tab **Panoramica**: schermata fissa con stato di sincronizzazione,
  contatori, ultimo audit e dispositivi; pulsante per aprire i dettagli.
- **Activity feed multi-PC** nel Monitor NAS, con polling ogni 5 minuti e
  aggiornamento manuale.
- **Pulizia guidata dei duplicati**: spostamento sicuro nel cestino locale con
  conferma, calcolo dello spazio recuperabile e protezione del file mantenuto.

## 1.29.0 - 2026-11-23

- **Certificato di coerenza**: audit completo settimanale a blocchi da 250
  file, in background e senza lock; report persistente degli ultimi 5 audit
  con data, durata, file confrontati e differenze.
- Nuovo pannello nella scheda Stato con l'esito dell'ultimo audit e pulsante
  "Audit completo ora".
- L'audit è solo diagnostico: non modifica file né journal.

## 1.28.12 - 2026-11-23

- Safety check: conteggio cancellazioni dal manifest in cache invece di un
  rsync `--dry-run` sull'intero albero (fallback a rsync se cache assente).
- `changed_paths()` riscritto a passata singola con `os.scandir`.

## 1.28.11 - 2026-11-23

- Manifest condiviso tra PullWorker e PushWorker (cache con scadenza 5 minuti):
  la scansione completa tombstonae subito le cancellazioni già presenti sul
  NAS invece di accodarle e far rinviare il pull.
- Log `PULL_DEFERRED` deduplicato per insieme di percorsi bloccanti.

## 1.28.10 - 2026-11-23

- Completamento del fix del loop `PULL_DEFERRED`: le cancellazioni già
  applicate su entrambi i lati vengono tombstonate anche nel baseline locale,
  impedendo alla scansione successiva di rimetterle in coda.

## 1.28.9 - 2026-11-23

- Rotto il loop di `PULL_DEFERRED`: i percorsi di cancellazioni locali il cui
  file è già assente anche sul NAS vengono ora rimossi dalla coda `pending`
  invece di bloccare ogni pull successivo.

## 1.28.8 - 2026-11-23

- Barra verticale sempre riservata nei tab scrollabili: elimina il salto
  orizzontale del contenuto quando si passa da un tab all'altro.

## 1.28.7 - 2026-11-23

- Densità globale compatta: font base 12 px, margini dei QGroupBox dimezzati
  (8 px in alto, 6/8 di padding), titoli e pulsanti più stretti.
- Finestra iniziale ridotta a 1100×680.
- Riduzione stimata dell'altezza complessiva di tutti i tab del 20-25%.

## 1.28.6 - 2026-11-23

- Monitor NAS: "Rete", "Uptime NAS" e "Attività NASBox" affiancati in un'unica
  riga compatta.

## 1.28.5 - 2026-11-23

- Storage insights: titolo "File duplicati" non duplicato e rendering con un
  percorso per riga (max 5 gruppi e 3 path per gruppo, con conteggio dei
  rimanenti).
- La conferma delle cancellazioni massive ora chiama anche `sync_now()`: in
  precedenza veniva rimosso solo il blocco, senza avviare la propagazione.

## 1.28.4 - 2026-11-23

- Stato: "Garanzia di sincronizzazione" e "Stato" affiancati 50/50.
- Monitor NAS: "Dettagli analisi" e "File duplicati" affiancati e aperti di
  default.
- Impostazioni: tutte le sezioni sempre visibili (niente collasso iniziale),
  campi banda stretti e blocco esclusioni subito sotto la banda.

## 1.28.3 - 2026-11-23

- **Interfaccia compatta**: Stato, Monitor NAS e Impostazioni usano 2 colonne
  con controlli e margini più stretti, riducendo molto lo scroll verticale.
  La card di garanzia di sincronizzazione, la barra del Monitor e la ricerca
  delle Impostazioni restano come header fissi sempre visibili.
- Grafici e sezioni del Monitor collassabili; le sezioni Impostazioni partono
  al massimo 2 aperte per colonna (la ricerca le riapre comunque).
- Sotto una soglia di larghezza le colonne si impilano automaticamente.

## 1.28.2 - 2026-11-23

- **Fix urgente**: `entry_count()` era stato definito dentro `CausalVersion`
  invece che in `SyncStateStore`, e generava un errore a ogni tick del motore.
- L'updater elimina `__pycache__` dopo l'installazione: con i mtime
  normalizzati dell'archivio, i bytecode della versione precedente potevano
  essere considerati validi e mascherare il nuovo codice.

## 1.28.1 - 2026-11-23

- **Avvio lento corretto**: il controllo di integrità automatico non scatta più
  al primo avvio dopo l'aggiornamento (rinviato di 30 minuti e solo a client
  a riposo). La verifica manuale usa ora un campione di 500 file dal manifest
  invece di un `--checksum` sull'intero albero, che su volumi da oltre 1 TB
  poteva occupare diversi gigabyte di RAM. `integrity_check_sample_size = 0`
  ripristina l'audit completo.
- Il conteggio dei file nella card di garanzia è memoizzato per 30 secondi.

## 1.28.0 - 2026-11-23

Sprint interfaccia: la certezza che tutto sia sincronizzato diventa il dato
principale, non un dettaglio tecnico.

- **Garanzia di sincronizzazione**: nuova card in cima alla scheda Stato con
  "Tutto sincronizzato", ultima verifica dei checksum, file confrontati,
  differenze, code, conflitti, repository, journal e stato del NAS. Pulsante
  "Verifica adesso" e stessa sintesi nel tooltip della tray.
- **Novità dopo l'aggiornamento**: finestra con le note di rilascio, letta da
  `RELEASE_NOTES.md` inclusa nel bundle.
- **Esplora NAS**: menu contestuale (copia percorsi, apri cartella locale,
  escludi/includi, modalità di sync, crea mirror), drag & drop per upload che
  passa dalla coda normale, empty state.
- **Storico/Cestino**: raggruppamento per data, ripristino multiplo e in un'altra
  cartella, spazio recuperabile.
- **Impostazioni**: ricerca con Ctrl+K, sezioni collassabili, valori rilevati
  dal NAS, export del bundle diagnostico senza segreti.
- **Trasferimenti**: raggruppamento per operazione, priorità e aging visibili.
- **Metriche**: storage insights con cartelle più grandi, duplicati e spazio
  recuperabile dallo storico.
- **Conflitti**: confronto affiancato, pulsanti grandi, merge a tre vie per
  testo e anteprima del nome della copia.
- Memorizzazione della geometria della finestra.

## 1.27.0 - 2026-11-23

Client 1.27.0 / server 3.24.0. Hardening di sicurezza, test e prestazioni.

- **Cancellazioni di massa**: limite cumulativo nella finestra di un'ora oltre
  al limite per singolo batch, con conferma esplicita temporanea dalla scheda
  Stato. Le cancellazioni da Sfoglia NAS usano un solo lease e aggiornano la
  baseline locale.
- **Integrità**: gli eventi di cancellazione vengono registrati solo dopo il
  successo effettivo di rsync (`--delete-after`), non appena compaiono
  nell'itemize. Aggiunto controllo automatico periodico dei checksum con
  `integrity_check_interval_seconds` (default 24 h).
- **Annullo affidabile**: l'attesa sullo scheduler è interrompibile; la
  sessione SSH che detiene il lock non viene più uccisa da "Annulla" e un
  annullo arrivato durante il preflight non viene più perso.
- **Fingerprint**: la cache dei digest include `ctime` e viene invalidata dopo
  ogni commit, evitando che una modifica con mtime preservato non venga mai
  caricata.
- **Pull**: il full pull non scarica più due volte gli stessi file né crea
  backup duplicati.
- **Aggiornamenti**: `requires_server` del manifest ora applicato, rollback
  transazionale, conferma di avvio adattiva, versioni con suffisso gestite.
- **Manifest incrementale**: il server 3.24.0 espone `--manifest-export-delta
  SINCE`; il client scarica solo le voci cambiate con fallback al manifest
  completo. Aggiunto `--devices` per la presence multi-PC, mostrata nella
  scheda Stato.
- **GUI e test**: accessibilità, tema scuro/sistema, i18n completato,
  scorciatoie, `pytest.ini`/conftest per PyQt6 e skip esplicito dei test che
  richiedono il server. Suite client 263 test, server 34 test.

## 1.26.14 - 2026-11-21

Rende leggibile ciò che sta succedendo: messaggi più chiari e avanzamento
sempre visibile anche quando il NAS è occupato da un altro PC.

- **Niente più "errore e poi silenzio".** Quando il lock del NAS è occupato da
  un altro client, al posto di una riga di errore statica compare un conto alla
  rovescia che scorre ("riprovo da solo tra 12s"), la coda resta visibile e
  viene detto esplicitamente che nessun file è perso. Il conto alla rovescia è
  mostrato nella scheda Stato, nella scheda Trasferimenti e nel tooltip della
  tray.
- **Un errore vero resta un errore, un'attesa no.** La contesa tra PC non
  produce più un messaggio d'errore; un lock davvero in errore (SSH, permessi)
  sì. Un trasferimento interrotto dice ora che i file restano in coda e che il
  tentativo riparte da solo.
- **Si vedono i file scorrere.** La riga di stato mostra `file N di M` del batch
  e la barra del batch avanza a ogni file completato (prima solo la barra del
  singolo file, che sui tanti file piccoli resta a 0→100 e sembra ferma). Nella
  scheda Trasferimenti compaiono anche la posizione nel batch e l'ultimo file
  completato.
- **"Sincronizza adesso" dalla tray ora funziona davvero:** chiamava
  `engine.wake(force=True)`, che non accetta argomenti; l'eccezione veniva
  inghiottita dallo slot Qt e la voce di menu non faceva nulla.
- **Meno rumore da errori già risolti.** Un `ERROR` di trasferimento resta nella
  scheda "Richiede attenzione" per 15 minuti invece di 24 ore: dopo quel tempo
  il worker lo ha già ritentato e il problema non è più azionabile. I blocchi di
  sicurezza e del journal mantengono la finestra di 24 ore.
- Aggiunti 15 test di regressione su conto alla rovescia, posizione nel batch,
  distinzione contesa/errore e verifica del tray.


## 1.26.13 - 2026-11-19

Ripristina i pull completi e svuota il log dagli errori rsync.

- **Progresso rsync 3.5.2.** Il totale trasferito delle righe
  `--info=progress2` include ora l'unità (`600,000,000B 100% ...`): la vecchia
  regex non lo riconosceva, quindi il progresso non veniva letto, la coda delle
  completioni non si svuotava e al 4096° file il trasferimento veniva abortito
  con `output rsync non compatibile: coda completioni oltre il limite`. Il pull
  completo dell'albero veniva ritentato all'infinito e ogni tentativo scaricava
  fino a 64 KB di progresso nel log come errore. Ora sono riconosciuti entrambi
  i formati e le righe di progresso non finiscono mai nel dettaglio errore.
- **Mirror senza sorgente.** Se la cartella esterna manca o il sync fallisce,
  il mirror riprova ogni 5 minuti invece che a ogni tick (0,5 s) e non riscrive
  più `client.json` due volte al secondo; l'errore viene salvato e registrato
  solo quando cambia.
- Aggiunti test di regressione per il formato rsync 3.5.2, il trasferimento di
  alberi con più di 4096 file e il backoff del mirror.

## 1.26.12 - 2026-11-16

Evita che un pull si annulli da solo mentre applica le cancellazioni arrivate dal
NAS.

- Le cancellazioni e i download eseguiti dal pull generano eventi del watcher e
  finivano nella coda del push: al controllo successivo il pull li scambiava per
  modifiche locali esterne e si rinviava, riavviando la scansione completa dopo
  ogni elemento. Ora i percorsi scritti dal pull stesso (e le directory, che non
  sono file sincronizzabili) vengono ignorati in quel controllo.
- Aggiunto un test di regressione per scritture proprie e directory.

## 1.26.11 - 2026-11-16

Completa la correzione dei picchi di CPU: la causa residua era un falso
"file modificato".

- **Solo il contenuto conta.** `changed_paths()` confrontava l'intero
  fingerprint (incluso `mtime`) con la base locale. Un file con gli stessi byte
  ma mtime diverso finiva quindi nella coda del push; il push lo classificava
  correttamente come "vince il NAS" e non faceva nulla, mentre il pull restava
  rinviato perché vedeva quella modifica pendente. Il confronto ora usa digest
  e dimensione: un semplice `touch` non genera più lavoro inutile.
- Aggiunto un test di regressione per la differenza di solo mtime.

## 1.26.10 - 2026-11-16

Corretto il ciclo che poteva tenere la CPU del client occupata a intermittenza
quando una coda di modifiche locali non riusciva più a svuotarsi.

- **Coda pendente riconciliata.** Dopo una scansione completa, i percorsi in
  coda che non risultano più diversi dalla base locale (o la cui cancellazione
  è già registrata come tombstone) vengono rimossi. Prima restavano per
  sempre: il pull si rinviava all'infinito e ogni rinvio innescava una nuova
  scansione completa dell'albero (oltre 160.000 file), con picchi di CPU
  ricorrenti.
- **Niente più scansione completa a ogni rinvio.** Il pull ora segna al
  watcher i percorsi esatti della coda invece del sentinel "percorso
  sconosciuto", che obbligava il push a rileggere tutto l'albero.
- **Rinvio del pull con attesa.** Dopo un `PULL_DEFERRED` non si ritenta più a
  ogni tick: si aspetta 20 secondi oppure il completamento di un push (che
  risveglia subito il pull per applicare le modifiche appena pubblicate).

## 1.26.9 - 2026-11-15

Aggiornamento del client ripensato per essere verificabile, periodico e con
rollback (il vecchio meccanismo aggiornava solo all'avvio, senza controlli di
integrità e senza rete di sicurezza).

- **Manifest + archivio unico.** Sul NAS si pubblica `manifest.json` accanto a
  `client-<versione>.tar.gz`. Il client legge il manifest con **un solo comando
  SSH** (prima interrogava un `version.py` remoto) e scarica l'archivio solo se
  la versione è più nuova. Il vecchio layout a cartella resta supportato, così
  un publisher esistente continua a funzionare.
- **Verifica di integrità.** SHA-256 e dimensione dell'archivio sono confrontati
  prima di estrarre: un download troncato o un file manomesso vengono rifiutati
  invece di essere installati. L'estrazione rifiuta percorsi assoluti, `..`,
  symlink e device file.
- **Rollback automatico.** La versione precedente non viene più cancellata
  subito: resta come backup finché la nuova non conferma l'avvio (finestra
  visibile da 15 s). Se non ci riesce, al secondo tentativo fallito il client
  ripristina da solo la versione funzionante. È la protezione che mancava
  quando un self-update lasciava l'app "sparita" con "già in esecuzione".
- **Controllo periodico, non solo all'avvio.** Un client lasciato aperto per
  giorni non vedeva mai una release nuova: ora controlla ogni 6 ore (e un
  minuto dopo l'avvio) e avvisa con una notifica della tray, senza dialoghi
  modali. La voce **Aggiorna a <versione>…** compare nel menu della tray (oltre
  alla versione cliccabile nella scheda Stato) e apre lo stesso flusso di
  aggiornamento.
- **La cartella degli aggiornamenti non entra più nella sincronizzazione.**
  `.nasbox-client-update` sta dentro `remote_prefix`, quindi senza esclusione
  l'archivio pubblicato sarebbe finito scaricato nella cartella NASBox di ogni
  client e ricaricato a ogni release (con le copie di conflitto che ne
  derivano).
- **Niente più update sopra il repository di sviluppo.** Se la cartella in uso è
  un checkout git, l'aggiornamento automatico si disattiva (sovrascriverebbe il
  repository e il lavoro non committato); `NASBOX_UPDATE_FORCE=1` o
  `client_update_allow_dev_tree` lo forzano.
- **`tools/publish_client.py`.** Prepara archivio e manifest in un comando,
  verifica il bundle estratto (versione inclusa) prima di pubblicarlo e con
  `--upload` lo copia sul NAS.

## 1.26.8 - 2026-11-15

- **Finestra bloccata con un core al 100% durante i trasferimenti.**
  `on_item_started()` ricostruiva l'insieme dei percorsi in coda (30.000
  elementi) e riordinava l'intera lista per **ogni singolo file** avviato:
  38 ms per file, cioè 26 file/s al massimo contro i ~100 di un trasferimento
  reale. Il thread grafico non riusciva a stare dietro alla coda di eventi, la
  finestra smetteva di rispondere e restava con un core al 100% fino alla fine
  del trasferimento. Ora il set dei percorsi è in cache (invalidato solo quando
  la lista viene sostituita) e la tabella viene ridisegnata al massimo ogni
  250 ms, riusando il timer di flush già esistente: **0,7 ms per file**,
  ~1500 file/s. Il blocco si presentava anche a finestra già chiusa nella
  tray, perché era il thread grafico a restare indietro.
- **Anteprima della coda sempre in errore.** Mancava `import stat` in
  `scan_worker.py`: `_manifest_preview()` sollevava `NameError` a ogni passata
  (una per aggiornamento mirror, 40 occorrenze nel log di una sola giornata) e
  la coda ricadeva sempre sul dry-run rsync completo, più lento e più pesante
  sul NAS.
- **Watcher `inotifywait` orfani.** Se il client veniva terminato senza un
  arresto ordinato (crash, SIGKILL, logout, chiusura della sessione) i watcher
  restavano vivi per ore con parent 1, continuando a tenere i watch inotify
  dell'intero albero e a consumare eventi. Ora il figlio riceve
  `PR_SET_PDEATHSIG` e muore insieme al padre; il caso "padre già sparito
  prima del prctl" viene chiuso subito.
- **Errori di trasferimento inutilizzabili.** Il dettaglio accumulava anche il
  chatter di rsync (`100 files...`, `receiving file list ...`, riepiloghi
  `sent ... bytes`) e veniva troncato **dall'inizio**: su un albero grande le
  righe informative esaurivano il budget di 64 KB e l'errore vero — che rsync
  stampa alla fine — veniva buttato via. Ogni trasferimento fallito riportava
  soltanto `[ERROR] - receiving file list ...`, senza alcuna indicazione utile.
  Ora il chatter è filtrato e il budget conserva la coda.

## 1.26.7 - 2026-11-15

- **Cliccare le schede non porta più la CPU al 100%.** `path_is_excluded`
  confrontava ogni percorso con ~60 pattern glob, sul percorso completo e su
  ognuno dei suoi componenti: su una cartella reale erano ~60 milioni di
  `fnmatch` a ogni passata — 22 s di CPU su 100 GB / 63k file, per restituire
  "niente da fare". Era il costo nascosto dietro l'apertura della scheda
  **Trasferimenti** (che ricalcola l'anteprima a partire dal manifest) e dietro
  ogni sweep completo (avvio, "Sincronizza ora", retry dopo un errore).
  I pattern sono ora compilati una volta per insieme in due alternazioni regex
  (una per i nomi, una per i percorsi): stesse regole, **~23x più veloce**
  (22,3 s → 1,5 s sull'albero reale; 111k percorsi valutati in 0,9 s).
  Un test compara la nuova implementazione con quella precedente su 40 percorsi
  × 3 insiemi di pattern.

## 1.26.6 - 2026-11-15

- **Upload in staging ora visibili nel log e nello storico.** Il percorso veloce
  per un lotto di soli file nuovi (upload in una staging privata + publish
  atomico) non emetteva alcun evento per file: i file arrivavano sul NAS ma non
  comparivano in `client.log`, in `events.jsonl`, nella scheda **Log** né nella
  lista dei trasferimenti. Il client sembrava inattivo subito dopo aver
  pubblicato decine di file. Ora ogni file pubblicato viene registrato come un
  normale `UPLOAD`, con dimensione e segnale `transfer_item_done`.

## 1.26.5 - 2026-11-15

- **Impostazioni: scelta della gestione dei conflitti.** La policy introdotta in
  1.26.0 (`conflict_policy`) era raggiungibile solo modificando `client.json` a
  mano. Ora **Impostazioni → Conflitti tra due modifiche** offre *Vince la
  versione più recente (automatico)* e *Chiedi sempre (crea la copia di
  conflitto)*, con la stessa nota esplicativa della scheda Conflitti. Un valore
  non riconosciuto in un file di configurazione ricade sul default sicuro
  `"manual"` invece di scegliere un vincitore in silenzio.
- **MANUALE:** la sezione "Conflitti con cancellazioni" descrive il confronto tra
  data della tombstone NAS e modifica locale (vince la più recente) e rimanda
  alla nuova opzione invece di far modificare il JSON a mano.

## 1.26.4 - 2026-11-15

- **Conflitti: la cancellazione NAS non cancella più modifiche locali più
  recenti.** Se il NAS aveva cancellato un file e la copia locale era stata
  modificata dopo (confronto sulla data della tombstone, non solo sul
  contenuto), il conflitto veniva sempre risolto a favore del NAS: con la nuova
  risoluzione automatica la modifica locale finiva nello storico senza avvisare.
  Ora vince la versione più recente, coerentemente con `conflict_policy`.
- **Conflitti: fine del ciclo infinito che bloccava la sincronizzazione.** Un
  percorso presente in locale e semplicemente assente sul NAS (mai entrato nel
  journal, o con tombstone già rimossa dalla retention) veniva classificato come
  "non regolare" e faceva fallire l'intero batch di adozione a ogni passata: gli
  stessi conflitti venivano rigenerati ogni ~50 s, il client non convergeva mai
  e teneva occupato un core. Ora un percorso assente viene adottato come una
  cancellazione; solo directory/symlink continuano a bloccare.
- **Prestazioni: retry con backoff dopo un errore.** Ogni passata fallita
  forzava una rilettura completa dell'albero (e del baseline) al tick
  successivo, cioè ogni secondo: su una cartella grande significava un core
  occupato in permanenza. Ora i tentativi si distanziano da 5 s fino al
  `poll_interval` configurato, e un "Sincronizza ora" salta comunque l'attesa.
- **Anteprima e pianificazione coerenti:** anche l'anteprima (`ScanWorker`) usa
  `conflict_policy`, quindi la tabella dei trasferimenti non mostra più una
  direzione diversa da quella che il push applicherà.

## 1.26.3 - 2026-11-15

- **Tab Conflitti, avanzamento visibile:** la risoluzione (singola o "Risolvi
  tutto") ora mostra una barra di progressione e un contatore, invece del solo
  testo statico "Risoluzione in corso…":
  - `Risoluzione X di Y…` durante la parte locale;
  - `In attesa del trasferimento in corso…` quando il gate di trasferimento è
    occupato;
  - `Pulizia sul NAS X di Y…` durante la rimozione delle copie remote, con
    avanzamento reale per ogni file.
  La barra resta indeterminata finché non arriva il primo passo e si nasconde
  al termine, lasciando il riepilogo nel messaggio di stato.

## 1.26.2 - 2026-11-15

- **Tab Conflitti:**
  - nuovo pulsante **Risolvi tutto**, che risolve in un colpo tutti i conflitti
    visibili (rispettando il filtro di ricerca), mantenendo l'originale o la
    versione più recente quando l'originale manca;
  - tutte le colonne sono ora ridimensionabili manualmente trascinando il
    bordo dell'intestazione;
  - click sull'intestazione per ordinare per percorso, numero di alternative,
    dimensione reale, data di modifica o dispositivo di origine. L'ordinamento
    usa chiavi numeriche/cronologiche, non il testo formattato, quindi
    `9 KB` non finisce più dopo `10 MB`.

## 1.26.1 - 2026-11-15

- **Riavvio dopo l'aggiornamento:** corretto il caso in cui il client installava
  la nuova versione ma poi non ripartiva. `os.execv()` conserva il PID e il file
  descriptor del lock di istanza: la nuova immagine apriva un secondo fd,
  `flock()` falliva e il client usciva subito con "NASBox è già in esecuzione".
  Il lock viene ora passato esplicitamente alla nuova immagine, che lo adotta
  senza riaprirlo. Il riavvio è centralizzato in `updater.restart()` ed è usato
  da tutti e tre i punti di aggiornamento (avvio, scheda Stato, finestra
  principale).

## 1.26.0 - 2026-11-15

Conflitti "più recente vince" per default e protezione dal churn di sviluppo.

- **Risoluzione automatica dei conflitti:** nuova chiave
  `conflict_policy` (default `"newest"`). Quando entrambe le parti hanno
  modificato lo stesso file, il client tiene la versione più recente al
  percorso originale e lascia quella perdente nello storico `--backup`
  (retention 30 giorni), senza creare una copia `(conflitto da ...)` da
  risolvere a mano. L'ordine di scelta è clock causale, poi mtime (nanosecondi
  solo se entrambi i lati li espongono), infine tie-break deterministico sul
  digest: due client convergono sullo stesso vincitore. `"manual"` ripristina
  il comportamento precedente.
- **`.git` mai sincronizzato:** `.git`, `.hg` e `.svn` sono ora esclusi in
  modo permanente, non solo tramite un pattern rimovibile. Una copia di
  conflitto dentro `.git/refs` produceva ref malformati e rendeva il repository
  inutilizzabile.
- **Esclusioni predefinite più sicure:** cache di test/lint
  (`.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.tox`, `htmlcov`, …) e
  intermedi LaTeX (`.aux`, `.fls`, `.synctex.gz`, `.toc`, `.bbl`, …) non
  vengono più sincronizzati per default.
- **Log:** i conflitti risolti automaticamente appaiono come `CONFLICT_AUTO`
  nel tab Log, con l'indicazione della versione vincente.
- **Pulizia una tantum:** nuovo `tools/clean_conflicts.py` per archiviare le
  copie di conflitto esistenti localmente e spostare le corrispondenti versioni
  NAS nello storico, evitando che il pull successivo le ricrei.

## 1.25.0 - 2026-11-14

System tray interattiva, monitoraggio risorse NAS, risoluzione conflitti potenziata, esclusioni dev e ottimizzazioni performance.

- **System Tray Interattiva**:
  - Tooltip e intestazione dinamica con velocità e percentuale di trasferimento in tempo reale.
  - Menu pausa rapida con preset (30m, 1h, 2h, fino a domani ore 08:00, indefinita).
  - Azione rapida "⚡ Sincronizza adesso".
  - Sottomenu "File recenti" arricchito con "📄 Apri file" e "📁 Mostra nella cartella".
- **Monitoraggio Risorse e Stato NAS**:
  - Nuovo riquadro dedicato in Scheda Stato con stato connessione (LAN/SSH), attività NAS in tempo reale (A riposo, In upload, In sync da altro client).
  - Barra di utilizzo disco NAS e monitoraggio spazio in tempo reale tramite estensione di `--diagnostics`.
  - Barra di avanzamento sincronizzazione visibile durante il trasferimento attivo.
- **Conflict Center Potenziato**:
  - Comparazione affiancata di metadati (dimensione, data/ora, device sorgente) con suggerimento automatico.
  - Nuovi pulsanti espliciti: `Mantieni Originale`, `Usa Alternativa` e `Conserva entrambe` (`keep_both`) per archiviare la copia senza perdita dati.
- **Esclusioni Dev & Ottimizzazione Performance**:
  - Aggiunti `.venv`, `venv`, `__pycache__`, `node_modules`, `*.pyc`, `*.pyo`, `*.swp` a `DEFAULT_SYSTEM_EXCLUDES`.
  - Potatura directory escluse durante la scansione locale (`os.walk`) e skip ricalcolo hash SHA-256 per file identici per mtime e dimensione.
  - Timeout rsync preflight reso dinamico a 180s per prevenire blocchi di cancellazione su NAS a basso consumo.
- **Stabilità Processi**:
  - Collegamento ciclo di vita `inotifywait` al processo client via `prctl(PR_SET_PDEATHSIG, SIGTERM)` su Linux, azzerando i leak di processi orfani.
  - Correzione ResourceWarning per chiusura pipe in `rsync_ops.py`.

## 1.24.1 - 2026-10-23

Manutenzione automatica lato NAS (server 3.23.0).

- Il pass orario del demone ora esegue anche `maintain_state_files`:
  compatta il journal quando supera `JOURNAL_MAX_BYTES` (default 10 MB) **o**
  `JOURNAL_MAX_LINES` (default 50.000 righe), invece di aspettare solo un push
  o un export; ruota `events.jsonl` quando
  supera `EVENTS_MAX_BYTES` (default 5 MB); elimina `server.log.1` e
  `events.jsonl.1` più vecchi di `LOG_RETENTION_DAYS` (default 30 giorni).
- Pulizia dei file temporanei rimasti da un comando interrotto
  (`prune-*`, `fs-want.*`, `fs-match.*`, `manifest.tsv.tmp.*`,
  `transfer-journal.tsv.*`): solo dopo `STATE_TEMP_MAX_AGE_HOURS`
  (default 24 ore), quindi un comando in corso non viene mai toccato.
- `--status`, `--diagnostics` e `--print-config` espongono dimensioni e
  soglie di journal, log ed eventi; `server.conf.example` documenta le nuove
  chiavi.
- Nota: `manifest.tsv` non è un log e non va cancellato/ruotato: è lo
  snapshot compatto del repository (circa 8 MB nel repository attuale). Il
  journal è `transfer-journal.tsv`, normalmente pochi byte dopo la
  compattazione.

## 1.24.0 - 2026-10-23

Questa release rende visibile e gestibile ciò che prima era nascosto, e toglie
lavoro inutile al NAS.

- **Conflitti davvero protetti:** corretto `CONFLICT_GLOB`. Il vecchio pattern
  conteneva spazi e `rsync` non lo applicava come singola regola, quindi
  `--delete-after` dei mirror cancellava le copie di conflitto dopo pochi
  minuti. Ora il pattern è `*conflitto*` e un test esegue davvero `rsync` su
  directory temporanee per impedire il ritorno del bug.
- **Conflict Center 2.0:** tabella con dimensione, data e dispositivo di
  origine; filtro per percorso; versione consigliata preselezionata (contenuto
  più recente); anteprima diff per i file di testo; metadati e messaggi chiari
  per i binari; selezione multipla e "Risolvi selezionati (mantieni
  originale)"; badge nella scheda Conflitti, nella tray e nella card di stato.
- **Risoluzione più sicura e reattiva:** la promozione della versione scelta è
  un rename atomico (niente doppia copia del file), gira in background, usa il
  gate di trasferimento e la pulizia delle copie sul NAS avviene sotto un solo
  lease (`browse_delete_many`) invece di un lock per file. La scansione dei
  conflitti usa `os.walk` con pruning delle directory interne e non riscrive il
  database quando il gruppo non è cambiato.
- **Guardia anti conflict-storm:** dopo 3 conflitti per lo stesso percorso in
  un'ora viene riutilizzato un unico nome stabile, così un albero di build
  continuamente modificato non accumula decine di copie intere. Nuove chiavi
  `conflict_storm_limit` e `conflict_storm_window_seconds`.
- **Cartelle mirror autoritative:** le sottocartelle di destinazione dei mirror
  non vengono più scaricate dal NAS né trasformate in conflitti; la sorgente
  locale è autoritativa. Questo risolve alla radice il caso di una cartella
  LaTeX/Git/build replicata ogni 5 minuti che generava conflitti a ripetizione.
  Le cancellazioni locali continuano a propagarsi solo quando la propagazione
  è attiva.
- **NAS più leggero (server 3.22.0):** `--manifest-export` e `--manifest-get`
  sono ora read-only: rendono la vista manifest+journal in streaming e non
  riscrivono più l'intero manifest a ogni lettura. La compattazione resta
  legata alla soglia del journal e la pulizia dei tombstone ha un watermark
  giornaliero. L'export prende un lock condiviso per coerenza.
- **NAS più leggero (client):** health check adattivo (5 minuti quando il NAS è
  stabile), ricerca aggiornamento server ogni 5 minuti, preview della coda a
  5 minuti quando la finestra non è visibile, riconciliazione completa ogni 2
  ore invece di 15 minuti (forzabile con "Sincronizza tutto ora"), metriche a
  30 secondi di default. Nuovo profilo "NAS-friendly" in Impostazioni.
- **Compressione rsync configurabile:** automatica (disattivata sul percorso
  LAN diretto, attiva su WAN/bastione), sempre attiva o sempre disattiva.
  Meno CPU sul NAS sulle reti veloci.
- **Profili di esclusione rapidi:** LaTeX/documenti, sviluppo/build, Office e
  sistema/NAS, aggiungibili con un clic e removibili singolarmente. Pensati per
  fermare il churn di `.aux`, `.log`, `.idx`, `.synctex.gz`, `.git/`,
  `node_modules/`, `build/` e simili.
- **Stato più leggibile:** la scheda principale mostra solo ciò che serve ogni
  giorno; lock, watcher, coda e pending sono in "Mostra dettagli tecnici". I
  conflitti aperti compaiono come problema azionabile.
- **Cartelle esterne:** nuova nota che spiega il contratto "la sorgente è
  autoritativa" e il comportamento delle cancellazioni.

## 1.23.15 - 2026-10-22

- Corretto il tab **Conflitti** che restava vuoto: le copie di conflitto in cui
  vince la versione locale venivano create solo sul NAS e non scaricate, quindi
  la scansione locale non poteva elencarle. Ora la copia viene mantenuta anche
  in locale (e simmetricamente anche la versione locale perdente), cosi' i
  conflitti sono sempre visibili e risolvibili dal client.
- Nuovo pulsante **"Elimina copie"** nella scheda Conflitti: sposta tutte le
  versioni alternative nello storico senza toccare il file originale.
- La risoluzione e l'eliminazione dei conflitti rimuovono le copie anche dal
  NAS (nello storico NAS), cosi' non ricompaiono con la modalita' archivio.
- I mirror escludono le copie `(conflitto da ...)`: il loro `rsync --delete`
  non le cancella piu' dalla cartella replicata prima che si possano rivedere.

## 1.23.14 - 2026-10-15

- Nuova **cronologia per-file**: dalla scheda Cronologia, "Versioni del file…"
  apre una timeline del file selezionato che unisce le versioni locali e quelle
  sul NAS, con ripristino diretto.
- Nuovo **ripristino da conflitto**: nella scheda Conflitti, "Ripristina
  versione…" apre la cronologia del file in conflitto e ripristina una versione
  precedente come originale; le copie di conflitto vengono spostate nello
  storico locale e la versione ripristinata viene marcata per la sincronizzazione.

## 1.23.13 - 2026-10-15

- Nuova funzionalita': **selezione delle cartelle da sincronizzare** (selective
  sync). Dalla scheda Impostazioni, "Scegli cartelle…" mostra l'albero delle
  sottocartelle della cartella NASBox con caselle da spuntare: le cartelle non
  spuntate restano fuori dalla sincronizzazione in entrambe le direzioni. La
  scelta e' ancorata al percorso (una cartella omonima altrove non viene
  toccata) e convive con i pattern manuali. Escludere una cartella non cancella
  cio' che era gia' sincronizzato.

## 1.23.12 - 2026-10-15

- Nuova funzionalita': **pianificazione della sincronizzazione**. In
  Impostazioni si possono definire finestre ricorrenti (giorni + orario) in cui
  NASBox resta in pausa: non invia, non scarica, non resta in attesa sul NAS e
  non esegue i mirror. Le finestre possono attraversare la mezzanotte (es.
  22:00 → 07:00). La pausa manuale resta indipendente: il pulsante Pausa/Riprendi
  e la tray riflettono solo la pausa esplicita, mentre lo stato mostra la pausa
  programmata con l'orario di ripresa. Utile per tenere il NAS libero di notte o
  durante l'orario di lavoro.

## 1.23.11 - 2026-10-15

Alleggerimento del carico sul NAS e delle prestazioni del client.

- Server 3.21.0: `--file-states-current`/`--file-states` cerca i percorsi
  richiesti nel manifest con un singolo `awk` invece di un ciclo `while read`
  pura-bash sull'intero file. Su un manifest da 67k righe/8,4 MB quella lettura
  costava ~10s per chiamata, eseguita una volta per ogni chunk di sync; ora e'
  al di sotto del secondo e alleggerisce sensibilmente ogni sincronizzazione.
- Server 3.20.0: il pass orario di pruning non esegue piu' `date` una volta per
  file. Il timestamp di ogni versione e' gia' nel nome (suffisso UTC) e viene
  ora convertito in epoch con sola aritmetica intera bash; la scansione di un
  `.sync-trash` a sei cifre passa da minuti a pochi secondi e il carico di
  sistema da tempesta di fork a trascurabile. In piu', il pass registra la
  versione piu' vecchia vista e **salta del tutto la scansione** finche' nessuna
  versione puo' ancora superare la retention (con un limite di sicurezza
  settimanale): su un archivio di due mesi con retention 90 giorni significa non
  scansionare piu' per settimane. La pulizia delle cartelle vuote gira solo
  quando il pass ha davvero rimosso qualcosa, e `--wait-for-revision` non crea
  piu' una subshell a ogni secondo in attesa.
- Client: multiplexing SSH (ControlMaster) per riusare una sola connessione
  autenticata tra i molti comandi brevi di un ciclo. Meno handshake e, sul NAS,
  molti meno processi `sshd`/shell. Disattivabile in Impostazioni.
- Client: quando il change feed conferma che nulla e' cambiato, il pull non
  acquisisce piu' il lease NAS ne' ripete i controlli di sicurezza: elimina il
  costo di controllo piu' alto di un client inattivo. La revisione riportata
  dal feed evita anche una lettura di stato ridondante.
- Client: i chunk push gia' noti come "conservativi" non tentano piu' la
  pianificazione staging speculativa (un lease e un batch di stati remoti in
  meno per chunk).
- Client: corretta una crescita quadratica nel push. La ricerca del candidato
  staging e la risoluzione dei percorsi mancanti rileggevano l'intera baseline
  per ogni percorso; ora la baseline viene letta una volta per pass.
- Client: lo stato SQLite usa una connessione persistente per thread e memorizza
  `device_id`, eliminando un ciclo apri/commit/chiudi per file riconciliato e
  uno per ogni lettura dell'identificativo dispositivo.

## 1.23.6 - 2026-09-19

- Evitato che un cambio asincrono della connessione NAS durante un ciclo di
  push, pull o scansione lasci il ciclo con una connessione `None` o incoerente:
  il cambio viene applicato al ciclo successivo.
- Documentata la procedura sicura per la pulizia delle copie di conflitto,
  evitando la creazione di conflitti annidati mentre un client è attivo.

## 1.23.5 - 2026-09-19

- Risolto il loop dei conflitti in cui la versione NAS vincente veniva
  conservata correttamente, ma non veniva adottata sul file originale locale:
  ora il client scarica/adotta la versione NAS, aggiorna baseline e causalità e
  conserva la versione locale sotto un nome di conflitto.
- I nomi delle copie di conflitto sono deterministici per contenuto: un retry
  dello stesso conflitto non crea copie duplicate senza limite.

## 1.23.4 - 2026-09-01

- Il publish staging riconosce i batch gia' arrivati sul NAS con contenuto
  identico, li considera completati e rimuove il residuo privato senza
  segnalare una pubblicazione ancora in attesa.

## 1.23.3 - 2026-08-31

- Corretto il payload binario del publish staging quando sono abilitate le
  versioni causali, evitando il fallimento del ciclo push.

## 1.23.2 - 2026-08-30

- Aggiunti avvisi urgenti nella tray quando il NAS diventa irraggiungibile o
  il demone server non risponde, con deduplicazione per transizione di stato.
- Aggiunta la notifica di ripristino della connessione NAS e ridotto a un
  minuto il controllo della salute del demone.

## 1.23.1 - 2026-08-30

- Corretto il ciclo push del client: una guardia su un risultato non ancora
  definito causava `push tick failed` a ogni secondo dopo l'avvio.

## 1.23.0 - 2026-08-30

- Aggiunte versioni causali per percorso nel manifest e nel journal, con
  fallback ai protocolli precedenti e al confronto temporale per server legacy.
- Lo scheduler locale applica aging/fairness e pubblica una coda persistente con
  operazione attiva, attese e identificativi diagnostici.
- Aggiunte le modalita' bidirezionale, solo locale, solo NAS e archivio senza
  propagazione delle cancellazioni remote.
- Rename locali di file e directory non ambigui usano un'operazione atomica
  journalizzata sul NAS; gli eventi directory non restano nella coda.
- Migliorata la ripresa dei trasferimenti interrotti con staging deterministico,
  backpressure dell'output e `--append-verify` limitato alle staging private.
- Aggiunti test di causalita', modalita', rename, recovery e trasferimenti grandi.

## 1.22.1 - 2026-08-30

- Reso immediato lo stato della sincronizzazione nel Monitor NAS con un
  indicatore semplice per attività, coda, attesa e assenza di trasferimenti.

## 1.22.0 - 2026-08-30

- Aggiunto il change feed remoto: i client attendono la revisione del manifest
  senza scansionare continuamente l'albero NAS, mantenendo il polling come
  fallback per server o connessioni legacy.
- Le mutazioni remote verificano che il lease globale appartenga al device che
  invia journal, cancellazione, publish staging o operazione Browse.
- Aggiunto il Monitor NAS con metriche read-only aggregate, contatori CPU/I/O,
  grafici rolling e diagnostica di lock, coda, journal e manifest.
- Le staging vecchie non referenziate da transazioni recuperabili vengono
  ripulite con un limite per passaggio, senza interferire con upload attivi.
- Ripristinata la fondazione dei test client, aggiunti contratti di protocollo,
  fixture isolate, test di recovery e un harness di carico senza dati reali.

## 1.21.4 - 2026-08-23

- La preview non puo piu precedere un push locale: una richiesta di sync, una
  modifica rilevata o una coda durevole sospendono il dry-run finche il batch
  reale non e stato elaborato.
- Le scansioni complete salvano i percorsi risolti nella coda durevole; dopo un
  riavvio la preview e il push non possono piu divergere mostrando upload non
  effettivamente pianificati.

## 1.21.3 - 2026-08-23

- I batch di soli file nuovi usano fino a 500 percorsi per ciclo staging,
  riducendo le connessioni SSH e il preflight ripetuto durante le importazioni
  grandi. Percorsi gia presenti o non previsti mantengono blocchi conservativi
  da 100 file.

## 1.21.2 - 2026-08-23

- All'avvio o dopo la ricreazione del watcher, una cartella locale non vuota
  richiede una riconciliazione push durevole. Le differenze gia presenti prima
  dell'avvio non possono piu comparire nella preview senza entrare nella coda
  reale di upload.

## 1.21.1 - 2026-08-23

- Corretto il publish staging su NAS con `stat` a precisione di secondo: i
  timestamp locali sub-secondo vengono confrontati e memorizzati nella
  precisione disponibile, senza lasciare batch privati non pubblicati.

## 1.21.0 - 2026-08-23

- I batch composti esclusivamente da nuovi file vengono caricati nella staging
  privata del NAS senza detenere il lock globale. Il publish finale verifica
  che le destinazioni siano ancora assenti, sposta i file e registra un unico
  journal atomico sotto un lease breve.
- Il publish ha marker durevoli e recovery dopo crash durante marker, move o
  journal; non sovrascrive mai un percorso apparso nel frattempo e conserva la
  staging per retry quando non puo completare in sicurezza.
- La staging e esclusa da sincronizzazione, Browse e retention; quella vuota
  dopo un publish riuscito viene rimossa senza bloccare il trasferimento.

## 1.20.0 - 2026-08-23

- I controlli remoti riusano il digest gia presente nel manifest quando
  dimensione e mtime sul NAS coincidono; una modifica esterna continua invece
  a richiedere SHA-256 completo. I batch di molti file piccoli evitano quindi
  migliaia di hash ridondanti.
- I trasferimenti mirati non eseguono un secondo checksum rsync dopo il piano
  di riconciliazione: forzano soltanto i percorsi gia verificati, senza perdere
  la protezione contro conflitti.
- Un push cede il turno locale fra chunk gia completati, consentendo a pull,
  mirror e anteprime dello stesso PC di avanzare senza attendere tutto il batch.
- La scheda Trasferimenti mostra la fase reale del lavoro. Il lease NAS espone
  anche proprietario, fase e contatori in `--status`/`--print-config`, per
  distinguere attesa, verifica, trasferimento e commit.
- Rafforzati i test di recovery delle rinomine NAS: un crash dopo il journal non
  ripristina o perde il file gia spostato.

## 1.19.5 - 2026-08-23

- Dopo un aggiornamento avviato dal servizio, il client termina con un codice
  che fa ripartire systemd con un processo nuovo. Evita il riavvio incompleto
  del client gia in esecuzione, osservato sul client 122.

## 1.19.4 - 2026-08-23

- La pulizia dello storico locale ora viene eseguita in background: la finestra
  resta utilizzabile e mostra avanzamento durante scansione, cancellazioni e
  rimozione delle directory vuote.

## 1.19.3 - 2026-08-23

- Un percorso assente adottato dopo una cancellazione Browse non viene piu
  interpretato come un upload da registrare, evitando retry ed errori di
  journalizzazione dopo una rimozione remota.

## 1.19.2 - 2026-08-23

- Il pruning NAS non mantiene piu il lock dei trasferimenti durante la scansione
  dello storico: Browse e sincronizzazioni restano disponibili anche con uno
  storico molto grande.
- Le richieste in coda per il lock NAS scadono correttamente invece di restare
  bloccate senza limite; il relativo ticket viene rimosso alla chiusura SSH.
- Una cancellazione locale puo eliminare dal NAS contenuti identici al baseline
  anche se il timestamp NAS differisce; le divergenze di contenuto restano
  protette. Le cancellazioni rifiutate dal NAS non vengono piu ritentate in
  ciclo continuo.

## 1.19.1 - 2026-08-23

- I mirror rifiutano destinazioni pericolose e symlink che puntano fuori dalla
  cartella NASBox, evitando cancellazioni accidentali durante `rsync --delete`.
- Le copie create per risolvere conflitti e gli upload completati prima di un
  crash vengono ora registrati nel journal remoto e distribuiti agli altri PC.
- Le operazioni Browse di eliminazione e rinomina sul server sono recuperabili
  dopo un crash e verificano la scrittura dell'elenco per il journal.
- Il launcher del menu sostituisce correttamente il servizio NASBox nascosto
  nel tray con l'interfaccia visibile.

## 1.19.0 - 2026-08-22

- Le cancellazioni controllate sul server usano transazioni atomiche con
  recovery dopo un crash tra lo spostamento nel cestino e il journal.
- Push, pull e anteprima condividono ora una coda globale sul NAS con ticket a
  priorita', evitando retry indipendenti e ravvicinati tra client diversi.

- La sincronizzazione non si blocca piu quando una cartella cancellata lascia
  una directory vuota sul NAS: quei percorsi vengono ignorati invece di
  interrompere l'intero ciclo con un errore "non e un file regolare".

- Sostituita la licenza proprietaria con la European Union Public Licence
  versione 1.2 (EUPL-1.2).

## 1.18.1 - 2026-08-22

- Aggiunto un lock di istanza locale basato sul kernel: un secondo avvio del
  client, manuale o systemd, termina prima di creare worker o connessioni NAS.

## 1.18.0 - 2026-08-22

- I file candidati al push devono restare stabili per un intervallo locale
  condiviso dal batch; quelli ancora in scrittura restano pending senza usare
  il lock NAS.
- Push, pull, mirror e preview passano da uno scheduler locale a priorita', con
  il lavoro reale favorito rispetto alle anteprime.
- I conflitti hanno gruppi persistenti SQLite e una risoluzione che rimette il
  percorso originale nella coda pending prima della propagazione.
- Il lock remoto pubblica device, host e istante di acquisizione nella stessa
  sessione SSH, senza una seconda interrogazione dopo la contesa.
- Aggiunti failpoint opt-in e test subprocess per verificare crash dopo lo
  spostamento nel cestino.

## 1.17.0 - 2026-08-22

- Push, pull e anteprima coda condividono un cooldown con backoff dopo la
  contesa del lock NAS, evitando retry indipendenti e ravvicinati.
- Lo Stato mostra il numero, l'eta' e l'ultimo motivo di rinvio della coda
  persistente.
- I watcher dei mirror rispettano gli stessi esclusi del trasferimento, incluso
  `.git/` quando configurato.

## 1.16.1 - 2026-08-22

- Gli eventi del watcher relativi solo a directory o percorsi non trasferibili
  vengono rimossi dalla coda pending senza tentare di acquisire il lock NAS,
  evitando cicli di retry ravvicinati durante operazioni Git o simili.

## 1.15.2 - 2026-08-22

- Un pull distruttivo non cancella piu modifiche locali nuove o non ancora
  inviate quando il watcher non ha ancora registrato l'evento: il client
  verifica il baseline locale e rimanda il pull al push.
- Ripristinata e redistribuita la cartella `basicF-ITA` dopo la cancellazione
  erronea causata dalla gara tra push bloccato sul lock NAS e pull remoto.

## 1.16.0 - 2026-08-22

- Le modifiche locali vengono registrate in una coda persistente SQLite e
  restano da inviare anche dopo contesa del lock, errore di rete o riavvio.
- Aggiunto il tab **Conflitti**, che raggruppa le versioni alternative,
  evidenzia i casi da esaminare e permette di scegliere quale mantenere.
  Le versioni scartate finiscono nello storico locale recuperabile.
- Aggiunto `TODO.md` con le funzionalita' di sicurezza ed efficienza da
  rivalutare in seguito, senza introdurle implicitamente nella release.

## 1.15.1 - 2026-08-21

- Il pull verifica i percorsi manifestati prima di usare `--files-from` e
  ripara in modo condizionato le voci stale dopo una cancellazione esterna,
  evitando retry infiniti con rsync codice 23.
- Il tab Log mostra un riepilogo leggibile degli errori rsync e conserva il
  dettaglio completo nel tooltip della riga.

## 1.15.0 - 2026-08-21

- La scheda Stato mostra se il rilevamento modifiche usa `inotify` o la
  scansione periodica, evidenziando il possibile impatto sulle prestazioni.
- I trasferimenti rimandati per contesa del lock NAS sono distinti dagli errori
  inattesi del lock; gli errori vengono inoltre presentati come connessione SSH
  o trasferimento rsync quando il dettaglio lo consente.

## 1.14.1 - 2026-08-21

- Il servizio systemd mantiene il client non interattivo anche quando eredita
  una sessione grafica: la finestra principale resta nascosta e gli errori di
  avvio vengono registrati senza aprire dialoghi modali.

## 1.14.0 - 2026-08-21

- Il servizio systemd del client non mostra dialoghi interattivi durante
  l'avvio e usa automaticamente il backend Qt offscreen quando parte prima
  della sessione grafica.
- Gli aggiornamenti automatici del servizio vengono installati senza conferma
  interattiva e il lock NAS viene mostrato con durata e proprietario quando
  disponibili.
- Il pruning server e' limitato a un numero massimo di versioni per passata,
  evitando che uno storico molto grande monopolizzi il lock per troppo tempo.
- Aggiornato il protocollo server richiesto alla versione 3.11.0.

## 1.13.0 - 2026-08-21

- Il demone server non termina piu trasferimenti validi solo perche il lock
  globale e' vecchio: `flock` viene rilasciato dalla chiusura della sessione SSH.
- Il pruning dello storico rispetta il lock globale e viene rimandato durante
  un trasferimento attivo.
- Cancellazioni, eliminazioni da Sfoglia NAS e rinomine aggiornano il journal
  solo dopo il successo del movimento filesystem, con rollback se il journal non
  puo' essere scritto.
- Sfoglia NAS e mirror esterni condividono il coordinamento con push e pull;
  i mirror usano trasferimenti interrompibili e aggiornamenti ritardati.
- Le importazioni push rilasciano il lock remoto tra i blocchi e i retry dopo
  contesa usano backoff con jitter.
- Aggiornato il protocollo server richiesto alla versione 3.10.0.

## 1.12.1 - 2026-08-19

- Corretto l'errore `name 'updater' is not defined` al click sulla versione
  nell'intestazione: mancava l'import di `updater` in `main_window.py`.

## 1.12.0 - 2026-08-19

- Aggiunta la tab **Cartelle esterne**: cartelle che vivono FUORI dalla
  cartella NASBox (es. la cartella di lavoro) vengono replicate
  automaticamente dentro NASBox, e da lì finiscono sul NAS e sugli altri PC
  tramite il normale ciclo di sincronizzazione.
- Il mirror è a senso unico: la cartella sorgente resta autorevole e le
  modifiche alla copia fatte altrove vengono sovrascritte alla passata
  successiva. Le copie si aggiornano quasi subito (rilevamento inotify) con
  un re-sync periodico di sicurezza ogni 5 minuti.

## 1.11.2 - 2026-08-16

- Reso esplicito il codice di uscita usato da `flock` quando il lock NAS resta
  occupato fino al timeout, evitando che la normale contesa venga interpretata
  come errore tecnico.

## 1.11.1 - 2026-08-16

- La normale attesa del lock NAS occupato da un altro PC non viene piu
  presentata come errore: NASBox spiega che la sincronizzazione e stata
  rimandata, che nessun dato e stato perso e che riprovera automaticamente.
- Gli errori reali di SSH o del supporto lock restano distinti e visibili come
  problemi da correggere.

## 1.11.0 - 2026-08-16

- Aggiunto il wizard grafico completo sul PC per cartella locale, collegamento
  SSH e rilevamento del server su NAS amministrati anche solo da shell.
- La home distingue connessione, coda e trasferimenti, mostra l'ultima
  sincronizzazione e raccoglie i problemi con azioni correttive.
- Unificato il salvataggio delle impostazioni e reso il layout utilizzabile su
  finestre e schermi piu piccoli.
- La pulizia dei conflitti permette di rivedere e selezionare i file e li sposta
  nello storico locale recuperabile invece di eliminarli direttamente.
- Semplificato lo Storico, aggiunti aggiornamento esplicito e distinzione piu
  chiara tra versioni locali e NAS.
- Resi leggibili e completi i filtri del Log; aggiunti ricerca, breadcrumb e
  menu contestuale a Sfoglia NAS.
- Corretto il significato della retention remota `0`: le versioni non vengono
  eliminate automaticamente.
- Aggiunti workflow GitHub per AppImage, pacchetto Debian e bundle di
  aggiornamento client riproducibile.

## 1.10.35 - 2026-08-09

- Ignorate dalla coda le cancellazioni locali di percorsi già inesistenti.

## 1.10.34 - 2026-08-09

- La coda trasferimenti usa manifest e journal NAS invece di due scansioni
  `rsync --dry-run` complete a ogni intervallo.
- I pull senza nuove transazioni journalizzate non rilanciano `rsync`; resta una
  riconciliazione completa periodica per rilevare modifiche NAS legacy o manuali.

## 1.10.33 - 2026-08-09

- Evitate le scansioni complete dell'albero locale durante le anteprime quando
  il watcher ha già rilevato i percorsi modificati.
- Limitato il parallelismo degli hash nelle riconciliazioni complete per evitare
  picchi di CPU e mantenere reattivo il desktop.

## 1.10.32 - 2026-08-09

- Corretto l'errore `Path is not defined` durante l'installazione manuale degli
  aggiornamenti client.

## 1.10.31 - 2026-08-09

- Rese ridimensionabili manualmente tutte le colonne della tabella Log.
- Impostate larghezze iniziali piu leggibili per ora, azione, percorso e
  dettaglio.

## 1.10.30 - 2026-08-08

- Rimossi dalla coda i file appena completati, mantenendo la numerazione
  progressiva originale per i file ancora in attesa.

## 1.10.29 - 2026-08-08

- Ripristinata la numerazione progressiva della coda: ogni file conserva la
  propria posizione, anche quando gli elementi completati vengono spostati in fondo.

## 1.10.28 - 2026-08-08

- Corretta la coda Trasferimenti: la numerazione segue l’ordine visualizzato e
  il progresso non raggiunge il 100% finché l’ultimo file è ancora in corso.

## 1.10.27 - 2026-08-08

- La coda Trasferimenti ordina gli elementi in corso e ancora da trasferire in
  alto, lasciando quelli completati in fondo per rendere visibile l’avanzamento.
- Nella tab Log `Percorso` usa lo spazio principale e `Dettaglio` resta compatto.

## 1.10.26 - 2026-08-08

- Allargata la finestra principale e resa la tabella Log più leggibile: percorso
  e dettaglio ora si dividono lo spazio disponibile.

## 1.10.25 - 2026-08-08

- Il controllo aggiornamenti resta sulla stessa riga della versione e viene
  eseguito automaticamente al massimo una volta ogni ora.
- La cartella `sync-daemon` viene esclusa interamente dalla sincronizzazione,
  evitando conflitti tra i file del pacchetto e le copie sul NAS.

## 1.9.0 - 2026-08-08

- Nuovo tab "Sfoglia NAS": naviga la cartella remota così com'è sul NAS,
  scarica singoli file, rinomina o elimina file/cartelle. Rinomina ed
  eliminazione passano sempre dallo stesso cestino/retention del NAS usato
  dalla sincronizzazione (mai una cancellazione diretta) -- richiede il
  pacchetto server >= 3.8.0 (nuovi comandi `--browse-list`, `--browse-delete`,
  `--browse-rename`).
- rsync ora rileva automaticamente se il NAS usa una versione molto vecchia
  (< 3.2.4) e in quel caso aggiunge `--old-args`, evitando un bug noto di
  corruzione del percorso remoto quando client e NAS hanno versioni rsync
  molto distanti.
- L'installer del client (`install.sh`) ora offre di copiare la chiave SSH
  pubblica sul NAS/bastione in automatico (chiede la password una tantum,
  mai salvata) invece di richiedere `ssh-copy-id` manuale in un altro
  terminale.
- Corretto un loop di retry senza backoff in PullWorker su errori che
  falliscono rapidamente (poteva ripetere un tentativo ogni ~1s invece di
  rispettare l'intervallo configurato).
- Il watcher locale ora rileva anche le scritture in corso (`modify`), non
  solo apertura/chiusura file: evita di avviare un caricamento a metà di una
  scrittura lenta su un file di grandi dimensioni.
- La verifica del volume locale ora blocca anche le sincronizzazioni non
  distruttive se il marker locale manca ma il repository era già associato,
  invece di ignorare silenziosamente il controllo.
- Il calcolo dei checksum per una sincronizzazione completa (es. dopo aver
  copiato una cartella molto grande) ora usa più thread in parallelo invece
  di leggere un file alla volta.
- Riallineato `server/sync-daemon-server.sh` nel repository alla versione
  realmente in uso sul NAS (era rimasto circa due versioni indietro).

## 1.6.0 - 2026-08-07

- Aggiunto il marker UUID del repository sul NAS (`.nasbox-root`), creato dal
  wizard server e verificato dal client.
- Il client registra il filesystem della cartella locale fuori dall'albero
  sincronizzato e blocca le operazioni distruttive se il volume cambia o il
  repository NAS non è verificato.
- Aggiunto il preflight delle cancellazioni e un limite ai batch anomali.
- Aggiunta verifica esplicita delle chiavi host SSH con `known_hosts` dedicato.
- Le cancellazioni locali mantengono un tombstone nello stato di sincronizzazione
  per distinguere assenza, eliminazione e file mai sincronizzati.
- Aggiunta protezione per il conflitto modifica locale/cancellazione remota.

## 1.5.6 - 2026-08-07

- Corretto il dialogo di chiusura nascosto quando NASBox esce dal tray.
- L'uscita interrompe anche la sessione SSH in attesa del lock NAS.

## 1.5.5 - 2026-08-07

- Aggiunta barra di avanzamento percentuale della coda nel riquadro Velocità.

## 1.5.4 - 2026-08-07

- Limitato il rendering della coda a 500 righe per mantenere l'interfaccia reattiva durante sincronizzazioni molto grandi.

## 1.5.3 - 2026-08-07

- Trasferimenti mostra un riepilogo dell'ultimo ciclo completato anche quando la coda termina prima dell'anteprima.

## 1.5.2 - 2026-08-07

- Corretto il blocco della sincronizzazione manuale dovuto al controllo conflitti sull'intero albero locale.
- La coda mostra quando attende il lock NAS occupato da un'altra sincronizzazione.
- Aggiunto stato e avanzamento in tempo reale per i trasferimenti.

## 1.5.1 - 2026-08-07

- La sincronizzazione manuale forza anche il controllo delle modifiche locali.
- Aggiunto feedback visibile durante la richiesta di sincronizzazione e la chiusura dell'app.
- Aggiunta selezione multipla per ripristino ed eliminazione nel cestino locale.
- Aggiunta preparazione del progetto per la pubblicazione su GitHub con licenza source-available non commerciale.

## 1.5.0

- Versione iniziale del client grafico NASBox e del pacchetto server 3.4.0.
