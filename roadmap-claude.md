# NASBox → "Dropbox libero su NAS" — roadmap proposta da Claude

Analisi del 2026-11-26 su client Go 1.15.3 e server Go 3.33.0 (`client-go/`,
`internal/serverd`), letti insieme a `ROADMAP.md`, `TODO.md`, `PARITY.md` e
`AGENT-NOTES.md`. Questo documento **non sostituisce** `ROADMAP.md` (che copre
bene affidabilità del datapath, metadata e storage): lo integra con quello che
manca per passare da "sync tra i miei PC Linux" a "sostituto di Dropbox".

> **Aggiornamento 2026-11-26 — priorità dell'utente.** Ordine scelto: (1) GUI
> client più semplice e usabile, (2) web UI di consultazione e caricamento,
> (3) client Windows. **Non interessano** HTTPS con pairing/token per
> dispositivo (S1): le sezioni S1/S2 restano come riferimento ma fuori
> priorità; per Windows e per la web UI si valuti prima SSH/loopback + reverse
> proxy. Stato: fase 0 parziale (conferma delle cancellazioni, GC/fsck dei chunk
> fatti), vista semplice della GUI fatta; vedi `CHANGELOG.md` › Unreleased.

## 1. Dove siamo (sintesi)

Già solido, da non toccare:

- Sync bidirezionale con vector clock, lease NAS, journal, tombstone, guardie
  anti-cancellazione, conflitti con storico recuperabile.
- Demone Go (~7.300 righe in `serverd`) che parla gli stessi verbi del vecchio
  bash; indice `metadata.sqlite3` con delta reale; `tx-intent.sqlite3` per la
  recovery; store a chunk content-addressed (1 MiB fissi, zlib, dedup di rete).
- Client Go/Fyne in un binario: GUI, tray, sync selettiva, versioni file,
  integrazione file manager, auto-update con rollback, monitor NAS.

Il vero collo di bottiglia per l'obiettivo "Dropbox" non è il motore di sync,
ma **tre cose che Dropbox dà per scontate e NASBox oggi no**:

| Aspettativa Dropbox | Stato NASBox | Gap |
|---|---|---|
| Funziona su Windows/macOS/telefono | Solo Linux (Fyne + inotify; `update.go` accetta un solo GOOS/GOARCH) | **Piattaforme** |
| Un account, non una shell sul server | Ogni client entra via SSH come utente NAS; il demone è un set di verbi SSH | **Identità e accesso** |
| Condividi un link, invita qualcuno | Web UI solo `127.0.0.1:8080`, senza login, sola lettura | **Condivisione** |
| File "online-only", non occupano disco | Sync selettiva per cartelle (tutto o niente per cartella) | **On-demand** |
| Funziona da fuori casa senza pensarci | Jump host configurato a mano (`buzzqw.mooo.com:2223`), rsync/SSH | **Rete WAN** |
| Cronologia e ripristino | C'è, ed è già un punto di forza | — |

## 2. Principi (aggiunte a quelli di `ROADMAP.md`)

1. **SSH resta il trasporto di default, non l'unico.** Per Windows/macOS/mobile
   e per la condivisione serve anche un endpoint HTTPS con credenziali per
   dispositivo. Un solo protocollo di verbi, due trasporti.
2. **Un client mobile non deve poter rompere il repository.** Qualsiasi nuovo
   client passa dai verbi già verificati (lease, precondizioni, journal).
3. **Ogni funzione "cloud" è opt-in e spenta di default** (web esposta, link
   pubblici, cifratura): il NAS domestico non deve diventare un bersaglio per
   un'impostazione dimenticata.
4. **Misurare prima di ottimizzare**: i numeri già raccolti (passata 0,6 s,
   tempo reale 9-12 s) sono la baseline; ogni fase ha un criterio misurabile.

---

## 3. Lato demone server

### S1 — Identità per dispositivo e trasporto HTTPS (priorità massima)

È il sblocco di quasi tutto il resto (Windows/macOS, mobile, link, web).

- Endpoint `nasbox-server --serve-api` (HTTPS, TLS auto-gestito con ACME/DDNS o
  certificato proprio) che espone **gli stessi verbi** di oggi come RPC
  autenticata: `manifest-export`, `chunk-has/put/get`, `staging-publish`,
  `transfer-wait` (come stream lungo), `wait-for-revision` (long-poll/SSE).
- **Credenziali per dispositivo**: token a rotazione, creati con un pairing
  (QR/codice a 8 cifre mostrato dalla web UI o da `nasbox-server --pair`),
  revocabili uno a uno, con elenco dispositivi (`--devices` esiste già: va
  collegato). Nessun accesso shell richiesto sul NAS.
- Il lease oggi è un `flock` tenuto da una sessione SSH aperta: con HTTPS
  diventa un **lease con scadenza e heartbeat** (rilascio automatico se il
  client sparisce). Va progettato con cura perché è il punto più delicato:
  riusare il file owner già esistente e testare con kill del client.
- Rate limit e lockout sui token; log di audit (chi, quando, cosa) in
  `events.jsonl`.

Criterio: un client di prova (anche solo uno script) sincronizza una cartella
senza alcuna chiave SSH, con revoca che ha effetto entro un minuto.

### S2 — Lock più fine: dal lease globale al lease per sottoalbero

Oggi un solo lease serializza **tutti** i dispositivi (il "NAS occupato" del
vecchio audit). Con più utenti o con il telefono che carica foto, diventa il
limite. Passi:

1. Misurare durata e contesa del lease per fase (hash, I/O, journal) — già in
   `PERF`, serve solo aggregarle in `--metrics`.
2. Tenere il lease **solo per la commit** (journal+revisione), non per
   l'upload: i chunk si caricano fuori lock (sono immutabili e content-
   addressed, idempotenti) e il publish li referenzia. Con il backend `chunks`
   questo è già quasi possibile: è il cambiamento a più alto rendimento.
3. Solo se serve ancora: lease per prefisso di percorso.

### S3 — Storage: GC, CDC, quota, integrità (ripresa da `ROADMAP.md` fase 4)

- **GC mark-and-sweep dei chunk** (manca ed è bloccante: lo store cresce senza
  limite). Con periodo di grazia e dry-run; prima di tutto un `--chunk-fsck`
  che verifichi hash e presenza.
- **Chunking content-defined** (FastCDC o simile, 512 KiB–4 MiB) al posto dei
  chunk fissi da 1 MiB: un inserimento all'inizio di un file non rompe la
  dedup. Versionare il formato (`chunk_format`) con capability negoziata e
  migrazione lazy; i vecchi snapshot restano leggibili.
- **Quota per utente/cartella** e avviso a soglia (oggi c'è solo
  `TRASH_MAX_MB` globale).
- **Scrub periodico** (rilettura e verifica SHA-256 a rotazione, in finestra
  notturna per non svegliare i dischi) e report nel Monitor NAS.
- Backup online dello stato (`metadata`, `tx-intent`, journal) con test di
  ripristino: è l'unica parte che non si ricostruisce dai dati.
- Opzionale ma coerente con "libero": **cifratura at-rest per chunk**
  (AES-GCM, chiave derivata da passphrase, capability versionata). Richiede
  decidere subito se i nomi di file restano in chiaro nel manifest.

### S4 — Condivisione e web

La web UI attuale è minimale e senza autenticazione (corretto tenerla su
loopback). Per diventare un "dropbox.com personale":

- Login (utente+password con argon2, poi TOTP/passkey), sessioni, CSRF.
- **Link di condivisione**: token casuale, scadenza, password opzionale,
  limite download, sola lettura; revoca da elenco. Download via `/api/download`
  già esistente con Range per i video.
- **Cartelle condivise tra utenti NAS** con ACL (lettura/scrittura) a livello
  di sottoalbero; "richiesta di caricamento" (link che permette solo upload).
- Anteprime (immagini, PDF) generate dal server e cache su disco; ricerca per
  nome e, più avanti, per contenuto.
- Cestino e versioni navigabili da browser (i dati ci sono già in
  `.sync-trash`).
- Esposizione sicura: reverse proxy (Caddy/Synology) documentato, mai porta
  aperta senza TLS; rifiutare di ascoltare su `0.0.0.0` senza TLS+login.

### S5 — Multi-utente e multi-repository

Oggi `repository_id` esiste ma il modello è "un utente, un albero". Per
famiglia/piccolo team: più repository sullo stesso demone, utenti con quote e
ACL (S1+S4), cartelle condivise come repository montati nell'albero di un altro
utente. Va deciso presto lo schema dei percorsi (prefisso per repository) perché
tocca manifest, journal e client.

### S6 — Osservabilità e operatività

- Endpoint `/metrics` Prometheus opzionale e pagina "Stato" nella web UI
  (lock wait, revisione, chunk orfani, scrub, dispositivi, ultimo backup).
- Notifiche (email/ntfy/Telegram) per: disco quasi pieno, scrub con errori,
  dispositivo che non sincronizza da N giorni, cancellazione di massa bloccata.
- Un solo pacchetto di installazione per il NAS (Synology SPK + docker-compose
  per altri NAS/Linux) con aggiornamento e rollback del binario già esistenti.
- Soak test di 72 ore (già in TODO) come gate per ogni release "stabile".

---

## 4. Lato client e interfaccia

### C1 — Piattaforme (priorità massima, insieme a S1)

Senza Windows e macOS non è "un sostituto di Dropbox" per nessun altro oltre
all'autore.

1. **Windows** prima di macOS: Go+Fyne cross-compilano, ma serve CGO (già nota
   come limite) → runner CI dedicati. Sostituire `inotify` con `fsnotify`
   nativo (già usato), gestire percorsi/maiuscole/caratteri riservati,
   servizio di avvio, installer MSI/NSIS firmato.
2. Rimuovere la dipendenza da `ssh`/`rsync` installati: sui client non-Linux
   il trasporto deve essere l'API HTTPS di S1 (solo backend `chunks`).
   Questo semplifica anche Linux (niente rsync 3.1.2 vs 3.5 da gestire).
3. **Client headless** (`nasbox-go daemon`) per server/NAS secondari e per
   Docker; la GUI diventa un front-end separato dello stesso demone locale (il
   socket IPC esiste già).
4. Build arm64 (Raspberry/Chromebook/Mac M-series) e AppImage/Flatpak.
5. Aggiornamenti: **firma degli artefatti** (minisign/cosign) oltre al sha256,
   come già indicato nella fase 2 di `ROADMAP.md`.

### C2 — File on-demand ("smart sync")

È la funzione che più fa percepire Dropbox come "infinito".

- Fase A (a basso rischio): sync selettiva **per file/pattern/dimensione**
  ("non scaricare file > 500 MB", "tieni solo cartelle X") oltre che per
  cartella; indicatori nella GUI dei file non scaricati.
- Fase B: **placeholder** come file sparsi/stub con eviction LRU a soglia di
  spazio e rehydration all'apertura. Su Linux via FUSE (`go-fuse`), su
  Windows via Cloud Files API (la strada giusta), su macOS File Provider. È
  grande: farlo solo dopo C1 e con una cartella-pilota.
- Emblemi per singolo file (oggi solo cartella radice e conflitti): stato
  "sincronizzato / in corso / online-only / in conflitto" nel file manager.

### C3 — Mobile (Android prima di iOS)

- Il caso d'uso reale è: **backup automatico delle foto** + navigazione e
  download/upload di file + condivisione di link. Non serve un sync
  bidirezionale completo al primo rilascio.
- App Android (Kotlin, o Fyne mobile solo se sufficiente) che parla l'API di S1
  con pairing via QR; upload in background con WorkManager, solo Wi-Fi/ricarica
  configurabili, deduplica tramite `chunk-has`.
- iOS solo dopo, e con aspettative chiare (limiti del background).

### C4 — Rete: LAN/WAN trasparente

- **Autodiscovery** (mDNS/avahi) in LAN: già abbozzato in `internal/discover`,
  da completare e mostrare nel setup.
- Routing automatico LAN → hostname DDNS/HTTPS → relay opzionale, scelto dal
  client senza che l'utente tocchi jump host. Backoff e ripresa dopo cambio rete
  (Wi-Fi → tethering).
- Limiti di banda per fascia oraria e "metered connection" (già in
  `bandwidth_*`, manca la UI e la regola automatica).
- Opzionale: integrazione con Tailscale/WireGuard documentata come strada
  consigliata per l'accesso remoto senza esporre porte.

### C5 — Interfaccia e flusso quotidiano

- **Onboarding in 3 passi** (trova NAS → pairing → scegli cartelle) senza
  digitare mai un percorso SSH; oggi il setup chiede cartella locale, SSH,
  rilevamento NAS.
- **Home "cosa è successo"**: attività recente raggruppata per dispositivo e
  per persona, con "annulla" per le ultime cancellazioni/spostamenti (le
  versioni ci sono; manca l'azione a un clic).
- **Ricerca** unificata (nome, tipo, data, dispositivo) anche su file non
  scaricati, via indice del manifest sul server.
- **Condividi** dal menu del file manager e dalla GUI: crea link (S4), copia
  negli appunti, mostra chi ha accesso.
- Conflict Center già buono: aggiungere merge testuale a 3 vie per file di
  testo e miniature/diff per immagini; "risolvi dopo" con promemoria.
- Aggiustare i difetti aperti **prima di aggiungere funzioni**: sblocco della
  protezione "cartella" da `confirm-delete`, differenza 119.066 vs 119.745
  percorsi tracciati, recovery durevole per delete/rename/restore.
- Accessibilità e i18n: tooltip (limite Fyne), tema scuro/chiaro del sistema,
  stringhe in en/it già pronte, aggiungere de/fr/es solo con contributi.

### C6 — Sicurezza lato client

- Credenziali nel keyring di sistema (Secret Service/Keychain/Credential
  Manager), non in `client.json`.
- "Cancella dispositivo da remoto" (revoca token + rimozione cartella locale
  opzionale) e blocco dell'app con PIN, per portatili e telefoni.
- Protezione ransomware: rilevamento di modifiche massicce con cambio di
  estensione → pausa automatica e snapshot read-only del NAS (le guardie di
  cancellazione esistono, questa è la versione per le *modifiche*).

---

## 5. Sequenza proposta

Ogni fase è rilasciabile da sola e ha un criterio di uscita misurabile.

| Fase | Contenuto | Criterio di uscita |
|---|---|---|
| **0 — Chiudere i buchi** (1-2 sett.) | Sblocco protezione cartella; differenza conteggio 122/134; GC chunk + `--chunk-fsck`; recovery durevole delete/rename/restore; firma artefatti | Nessun difetto aperto in `AGENT-NOTES.md`; GC testato con `SIGKILL` |
| **1 — Trasporto HTTPS e dispositivi** (S1, parte di S2) | API HTTPS, pairing, token revocabili, lease con scadenza, upload fuori lock | Client di prova senza SSH; due client + upload da 5 GB senza "NAS occupato" |
| **2 — Windows + headless** (C1) | Client Windows con installer, demone headless, arm64 | Un PC Windows sincronizza per 7 giorni senza intervento |
| **3 — Condivisione** (S4, C5-condividi) | Login web, link con scadenza, anteprime, ricerca | Link condiviso scaricabile da fuori LAN dietro TLS; nessuna porta aperta senza login |
| **4 — On-demand e mobile** (C2, C3) | Selettivo per file/dimensione, poi placeholder; app Android con backup foto | Cartella-pilota online-only; 10.000 foto caricate senza duplicati |
| **5 — Team e storage avanzato** (S3 CDC/cifratura, S5, S6) | Multi-utente, quote, ACL, CDC, cifratura opzionale, notifiche, Prometheus | Due utenti con cartella condivisa e quote; soak 72 h superato |

Se si deve scegliere **una sola cosa** dopo la fase 0: **S1 + C1 (HTTPS per
dispositivo + client Windows)**. Sblocca amici/famiglia, mobile e link, ed
elimina la dipendenza da SSH/rsync che oggi è il punto più fragile e meno
portabile.

## 6. Rischi e cose da non fare

- **Non riscrivere il motore di sync**: vector clock, journal e guardie sono la
  parte matura. Cambiare solo il trasporto e il modo di prendere il lease.
- **Non esporre la web UI su Internet prima di S4** (login, TLS, rate limit).
  Fino ad allora: Tailscale/WireGuard o SSH tunnel.
- **Il lease con scadenza è il rischio tecnico maggiore** (split-brain se un
  client lento ritorna dopo la scadenza): ogni mutazione deve portare la
  precondizione di revisione, il server rifiuta se il lease non è più suo.
- **Placeholder/on-demand sono costosi** (FUSE/Cloud Files/File Provider sono
  tre implementazioni diverse): non iniziare prima di avere Windows stabile.
- **Cifratura**: decidere presto il formato; aggiungerla dopo la CDC costa una
  migrazione dello store.
- **NAS debole** (DS420j: 4 core ARM, 987 MB RAM, dischi rotanti): thumbnail,
  scrub e hashing vanno schedulati in finestre e con priorità bassa; già
  disponibile `--capabilities` per adattarsi.
- Mantenere **backup 3-2-1 indipendente** del NAS: sincronizzare non è fare
  backup, e la documentazione deve dirlo.

## 7. Metriche da seguire

- Tempo "salvo un file → visibile su un altro PC" (oggi 9-12 s in LAN).
- Percentuale di operazioni che finiscono in `LOCK_DEFERRED`.
- Dimensione dello store chunk vs dati logici (efficacia di dedup/GC).
- Tempo di onboarding di un nuovo dispositivo (obiettivo < 3 minuti).
- Dispositivi attivi per piattaforma e tasso di auto-update riuscito.
- Errori di integrità trovati dallo scrub (obiettivo: zero silenziosi).
