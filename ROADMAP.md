# NASBox verso un cloud libero per NAS

NASBox è già una base locale-first solida: sincronizzazione bidirezionale,
vector clock per percorso, lease NAS, storico versioni e chunk content-addressed.
Non è ancora un equivalente Dropbox completo: il passaggio al demone Go ha
lasciato alcuni divari di affidabilità, scalabilità e sicurezza.

Questo documento definisce il percorso senza riscrivere il motore esistente.

## Principi

- **I dati restano sul NAS**: nessun servizio esterno obbligatorio.
- **Il filesystem resta autorevole per i byte**: SQLite gestisce metadati,
  transazioni, indici e change feed.
- **Compatibilità prima del taglio**: manifest e journal TSV restano una
  proiezione recuperabile durante la migrazione.
- **Mai perdere un commit**: un errore di manutenzione dopo journal/revisione non
  deve provocare rollback dei file.
- **Capability, non solo versioni**: client e server negoziano le funzionalità
  effettivamente disponibili.
- **Offline è una condizione normale**: coda persistente, retry idempotente e
  ripresa dopo crash.

## Fase 1 — metadata indicizzata e delta reale

Stato: implementata in `Unreleased`.

- Indice SQLite derivato `server/state/metadata.sqlite3`.
- Schema versionato, WAL e checkpoint del journal.
- Delta manifest basato su `updated_revision`, con fallback completo per cursori
  troppo vecchi.
- Snapshot manifest/revisione coerente sotto lock.
- Lookup indicizzati per manifest, chunk e file states.
- Migrazioni transazionali del SQLite client, compatibili con Python.
- `Observe` atomica e cursore client che non può avanzare oltre i dati letti.

Accettazione:

- export pieno e renderer legacy producono le stesse righe;
- una delta di una revisione contiene solo i percorsi cambiati;
- la compattazione non invalida i delta già disponibili;
- DB indice mancante/corrotto viene ricostruito o usa il fallback TSV;
- crash tra journal e revisione viene riparato al retry.

## Fase 2 — affidabilità del datapath

Priorità bloccanti prima di una release stabile:

1. Streaming dei chunk a memoria limitata; niente buffer dell'intero file.
2. Supporto esplicito dei file vuoti.
3. Rollback completo per nuovi file nelle pubblicazioni batch.
4. Verifica del SHA-256 dell'intero file dopo il download.
5. Precondizioni contro la creazione concorrente dello stesso percorso.
6. Capability `chunk_transfer_v1` e gate nel bootstrap del client.
7. Marker di intento e recovery Go per publish/delete/rename, equivalenti al
   server bash.
8. `--restore-batch` e parità dei verbi mutanti essenziali.
9. Validazione server di traversal, percorsi interni e symlink antenati.
10. Firmare gli artefatti di aggiornamento e verificare sempre il checksum.

Accettazione: test `SIGKILL` in ogni punto della pubblicazione, file vuoti e
multi-GB, disco pieno, rete intermittente e due client concorrenti.

## Fase 3 — offline e multi-device

- Outbox SQLite persistente per operazioni locali e ripresa dopo crash.
- Routing LAN/WAN/jump host usato da GUI, sync, update e trasferimenti.
- Lease scheduler per tutte le mutazioni, non solo per gli upload.
- Lock di istanza condiviso da tutti i comandi mutanti.
- Vector clock propagato su delete e rename.
- Osservazione locale allineata alla baseline dopo pull e rename remoto.
- Conferma completa delle guardie di cancellazione, incluse cartelle nidificate.

Accettazione: un client spento non fa perdere eventi; due client modificano lo
stesso percorso offline e ottengono un conflitto deterministico e recuperabile.

## Fase 4 — storage e osservabilità

- Reference counting e garbage collection mark-and-sweep dei chunk.
- Quota chunk e limite delle operazioni di manutenzione sotto lock.
- Indice delle versioni trash per quota e history senza walk completi.
- Backup online dello stato SQLite e test di ripristino.
- Metriche persistenti: latenza, lock wait, throughput, errori, drift e chunk
  orfani.
- Soak test di 72 ore con restart, kill e rete intermittente.

## Fase 5 — esperienza Dropbox libera

- Selective sync con placeholder/on-demand download ed eviction locale.
- Web UI con TLS, account, ACL e link in scadenza.
- Per-device credentials e revoca, senza dare accesso shell all'intero account NAS.
- Client headless/service e piattaforme Windows/macOS.
- Conflict center completo: diff, metadati, selezione multipla e storico.
- Aggiornamenti firmati e distribuzione atomica.

## Ispirazione da Kopia

Kopia è un sistema di backup, non un motore di sync continua. È utile come
riferimento per il livello storage e manutenzione:

- **Chunking content-defined**: confini dei chunk basati sul contenuto, così un
  inserimento all'inizio di un file non invalida la deduplica di tutto il resto.
- **Snapshot manifest immutabili**: un manifest compatto descrive puntualmente
  interi snapshot, invece di ricostruire lo stato da molte copie `.sync-trash`.
- **Mark-and-sweep GC**: oggetti raggiungibili dagli snapshot attivi; eliminazione
  solo dopo verifica e con periodo di grazia.
- **Manutenzione verificabile**: health check, compattazione controllata e
  riparazione esplicita dei blob danneggiati.
- **Compressione, encryption ed error correction opzionali** per chunk, con
  capability negoziate e formato versionato.
- **Storage astratto**: locale, SFTP o object storage dietro la stessa interfaccia.

Da non copiare: il modello di backup non sostituisce journal, lease e conflict
handling di NASBox. La sincronizzazione continua resta il nucleo del progetto.

## Cosa non fare

- Non introdurre un database client-server pesante finché SQLite + WAL basta.
- Non rendere SQLite autorevole per i byte dei file.
- Non eliminare manifest/journal prima di aver verificato backup, recovery e
  compatibilità sul NAS reale.
- Non dichiarare supporto di una funzionalità solo dal numero di versione.
