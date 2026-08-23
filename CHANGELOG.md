# Changelog

Tutte le modifiche rilevanti di NASBox sono documentate in questo file.

## Unreleased

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
