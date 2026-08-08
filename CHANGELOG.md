# Changelog

Tutte le modifiche rilevanti di NASBox sono documentate in questo file.

## 1.10.13 - 2026-08-08

- Il pull riconosce come proprie anche le cancellazioni dei file contenuti in
  una directory rimossa da `rsync`, evitando un nuovo annullamento del batch.

## 1.10.12 - 2026-08-08

- Il watcher ignora i file temporanei creati da `rsync` durante un pull, evitando
  che il trasferimento venga annullato e ripetuto continuamente.

## 1.10.11 - 2026-08-08

- I rinvii del pull riportano ora i percorsi locali che hanno causato
  l'interruzione, rendendo visibile la causa del conflitto.

## 1.10.10 - 2026-08-08

- Evitato il ciclo di download ripetuto: il watcher riconosce subito le scritture
  locali generate dal pull come appartenenti al trasferimento in corso.

## 1.10.9 - 2026-08-08

- Il watcher locale rispetta le stesse esclusioni di `rsync`, evitando che i
  bundle client e il codice del demone riattivino continuamente push e pull.

## 1.10.8 - 2026-08-08

- La coda mostra l'avanzamento della fase di preparazione e verifica dei file
  prima dell'avvio del trasferimento vero e proprio.

## 1.10.7 - 2026-08-08

- I numeri laterali della coda sono progressivi per file e restano associati
  all'ordine di evasione anche quando i completati vengono spostati in fondo.

## 1.10.6 - 2026-08-08

- Corretta la colonna Dimensione della coda: ora conserva la dimensione dei
  file anche dopo la riconciliazione del piano di sincronizzazione.

## 1.10.5 - 2026-08-08

- La coda mostra una barra di progresso solo sulla riga del file attualmente
  trasferito.

## 1.10.4 - 2026-08-08

- L'installer copia il client nella directory canonica `~/NASBox/sync-daemon/client`.
- Il launcher e il servizio systemd usano sempre il client installato in quella
  directory persistente.

## 1.10.3 - 2026-08-08

- L'azione `Esci` dalla tray nasconde subito la finestra principale e completa
  lo shutdown dell'applicazione.

## 1.10.2 - 2026-08-08

- La coda Trasferimenti ordina gli upload pendenti in alto e sposta i file
  completati in fondo, dando una progressione visiva senza riordinare ad ogni
  aggiornamento della velocità.

## 1.10.1 - 2026-08-08

- Aggiornato il pacchetto client/server per verificare il flusso di update
  automatico all'avvio.

## 1.10.0 - 2026-08-08

- Il client controlla una versione piu recente nel percorso di avvio o nel NAS
  prima di costruire la finestra, chiede conferma e si aggiorna riavviandosi.
- Il tab Trasferimenti mostra il piano effettivo anche quando la scansione
  dell'anteprima non riesce ad aggiornarsi mentre il lock NAS e occupato.

## 1.9.1 - 2026-08-08

- Il caricamento di un lotto grande (es. dopo aver copiato una cartella
  enorme) ora procede a blocchi da 100 file invece che tutto insieme: i primi
  file arrivano sul NAS entro pochi secondi/minuti invece che solo dopo aver
  verificato l'intero lotto, e un fallimento a metà lascia da rifare solo i
  blocchi rimanenti, non l'intera operazione. Il limite di sicurezza sulle
  cancellazioni resta calcolato sul totale dell'operazione, non per blocco.
- Corretto un bug per cui il tab Trasferimenti poteva restare bloccato su
  "NAS occupato da un'altra sincronizzazione" per l'intera durata di
  un'operazione a blocchi composta solo da cancellazioni (nessun blocco con
  upload effettivi), anche se il lavoro procedeva normalmente.
- Corretto un secondo bug del tab Trasferimenti: il conteggio "coda N/N" era
  sempre uguale a se stesso (e cresceva ogni volta) perché il totale reale
  arrivava da una scansione di anteprima separata che non riesce mai a
  ottenere il lock mentre un caricamento a blocchi è in corso. Ora il totale
  vero arriva direttamente dal caricamento stesso, che lo conosce già in
  anticipo.
- `sync-daemon-server.sh`: il controllo dello stato dei file (`--file-states-current`,
  usato prima di ogni caricamento) ora calcola i checksum in parallelo (8 alla
  volta) invece che uno alla volta -- misurato circa il 30-60% più veloce su
  un NAS reale, a seconda del carico I/O.

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
