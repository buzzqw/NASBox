# NASBox backlog

Aggiornato il 2026-08-30 dopo la release client 1.23.1 e server 3.17.0.
Le voci sotto descrivono il lavoro ancora utile senza confondere le funzioni
gia' implementate con quelle solo da verificare in laboratorio.

## Completato

- [x] Coordinamento del lease NAS tramite sessione SSH persistente e verifica
  del device proprietario nei comandi mutanti server.
- [x] Verifica SHA-256 live dei file durante la riconciliazione e conferma push.
- [x] Protezione del pull da modifiche locali concorrenti, inclusi set dei
  percorsi scritti dal pull protetto da lock e snapshot.
- [x] Publish staging con verifica dell'impronta, transazione recuperabile e
  conservazione delle modifiche locali avvenute durante l'upload.
- [x] Journal e revisione manifest con append temporaneo, rollback e recovery
  dopo crash tra commit journal e pubblicazione della revisione.
- [x] Garbage collection prudente delle staging abbandonate, limitata per pass
  e senza rimuovere transazioni ancora recuperabili.
- [x] Change feed remoto basato su `manifest.revision`, con fallback polling.
- [x] Monitor NAS read-only con CPU, memoria, swap, disco, rete, lock, coda,
  staging, journal, manifest e indicatori grafici.
- [x] Stato semplice nel Monitor NAS: nessuna attivita', sincronizzazione in
  corso, coda, staging in attesa, NAS non raggiungibile.
- [x] Protezione dalle cancellazioni massive gia' presente: limite configurabile
  `max_delete_files`, safety block, cestino/storico e log dell'operazione.
- [x] Versioni causali per percorso con fallback compatibile ai server legacy.
- [x] Scheduler locale con aging/fairness, identificativi e coda persistente
  visibile nello stato client.
- [x] Modalita' bidirezionale, solo locale, solo NAS e archivio.
- [x] Rename locali conservativi di file e directory con journal atomico e
  recovery; gli eventi di directory non restano pending.
- [x] Ripresa dei trasferimenti interrotti, output limitato e backpressure; la
  verifica end-to-end multi-GB resta da eseguire.
- [x] Suite client/server, fixture isolate, crash recovery, contratti di
  protocollo e harness locale di carico.

## Prossimo ciclo

### Priorita' alta

- [x] Versioni causali per percorso. Aggiungere revisioni o version vector per
  distinguere una cancellazione vecchia da una copia locale nuova e ignorare
  tombstone stantie.
- [x] Scheduler locale con priorita', fairness/aging e snapshot della coda;
  push/pull mantengono thread I/O separati.
- [x] Coda persistente visibile nello stato UI, con operazione attiva e attese.
- [x] Verifica di stabilita' dei file prima del push. Non trasferire file ancora
  aperti o in scrittura e riprovare quando la loro impronta si stabilizza.
- [ ] Test end-to-end con due client e NAS simulato o share di laboratorio:
  dati, journal, manifest, lock, retry e stato UI dopo riavvio.

### Affidabilita' dei trasferimenti

- [ ] Legare in modo esplicito il lease remoto al processo `rsync`, oppure
  terminare il trasferimento quando muore la sessione SSH che possiede il lock.
  La sessione persistente attuale riduce il rischio, ma il legame di processo
  deve essere verificato durante crash reali.
- [x] Registrare e verificare in modo atomico il completamento di rename Browse:
  DELETE della sorgente e PUT della destinazione, anche per directory.
- [x] Riconoscere rename e directory oltre alla riconciliazione dei file regolari.
- [x] Migliorare ripresa e backpressure dei trasferimenti interrotti tra hashing,
  rete e disco.
- [ ] Valutare trasferimenti a blocchi e riuso dei blocchi gia' presenti per file
  grandi, senza compromettere la verifica SHA-256 finale.

### Modalita' di sincronizzazione

- [x] Aggiungere modalita' per cartella: bidirezionale, solo locale, solo NAS e
  archivio senza propagazione delle cancellazioni.
- [ ] Definire chiaramente ereditarieta' delle esclusioni e comportamento dei
  cambi di modalita' su dati gia' presenti.
- [ ] Mantenere sempre una copia recuperabile per conflitti, delete e cambio
  modalita'.

## Da verificare, non da riscrivere

- [ ] Eseguire test dedicati alla protezione dalle cancellazioni massive:
  batch sotto soglia, batch sopra soglia, conferma, retry e recupero dal cestino.
- [ ] Verificare il comportamento del lease se il processo client o `rsync`
  viene terminato bruscamente.
- [ ] Verificare journal, manifest e staging dopo crash durante copia, rename,
  commit e recovery.
- [ ] Verificare nomi con tab, newline, caratteri di controllo e Unicode in ogni
  percorso Browse, journal, manifest e conflitto.

## Test operativi

- [ ] Preparare una share NAS di laboratorio e due configurazioni client
  disposable, separate dalla produzione.
- [ ] Coprire modifica A verso NAS e B, client offline, perdita rete durante
  trasferimento, modifiche concorrenti, rename, delete concorrenti e crash.
- [ ] Coprire riavvio NAS durante commit, disco quasi pieno e recupero dopo
  riavvio client.
- [ ] Eseguire soak test di 72 ore con create/modify/delete/rename casuali,
  restart periodici e interruzioni di rete.
- [ ] Raccogliere ogni minuto latenza, lock wait/duration, throughput, CPU,
  memoria, swap, iowait, I/O, rete, journal, manifest, staging, trash, coda e
  processi SSH/rsync.
- [ ] Considerare il test fallito con perdita dati, drift manifest, file live
  parziale, lease orfano, coda o staging non limitati.

## Rinviato

- [ ] Refresh automatico periodico del Tab Conflicts. Oggi il tab aggiorna alla
  visualizzazione, con il pulsante Aggiorna e dopo le operazioni di risoluzione.
- [ ] Valutazione separata di Syncthing come alternativa completa, senza
  modificare NASBox in produzione.

## Fuori scope attuale

- Gestione utenti, ACL, web UI, mobile, link condivisi e collaborazione remota.
- Migrazione automatica a un altro motore di sincronizzazione.
