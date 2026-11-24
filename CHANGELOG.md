# Changelog

Tutte le modifiche rilevanti di NASBox sono documentate in questo file.

## Unreleased

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
