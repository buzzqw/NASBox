# NASBox backlog

Funzionalita' da rivalutare dopo la correzione della coda locale e la gestione
esplicita dei conflitti.

## Priorita' alta

- Versioni causali per percorso: revisioni o version vector per distinguere
  cancellazioni successive da copie locali nuove e tombstone stale.
- Scheduler unico per push e pull: mantenere thread separati per il lavoro I/O,
  ma centralizzare decisioni, priorita' e transizioni di stato.
- Quarantena delle cancellazioni di massa: proposta, conferma, tombstone e
  rimozione definitiva separati, con blocco automatico per cambiamenti anomali.

## Efficienza

- Indice locale e remoto piu' ricco per ridurre scansioni complete e ricalcolo
  SHA-256 sul NAS.
- Trasferimenti a blocchi e riuso dei blocchi gia' presenti per file grandi.
- Ripresa piu' granulare dei trasferimenti interrotti e backpressure tra I/O,
  hashing e lock NAS.

## Affidabilita' e gestione

- Rilevamento esplicito di rename e cartelle, oltre alla riconciliazione dei soli
  file regolari.
- Modalita' per cartella: bidirezionale, solo locale, solo NAS e archivio senza
  propagazione delle cancellazioni.
- Versioning e storico con gruppi di conflitto e operazioni di risoluzione piu'
  ricche, mantenendo sempre una copia recuperabile.
- Verifica di stabilita' dei file aperti o ancora in scrittura prima del push.

## Sicurezza e operativita'

- Test automatici di crash durante copia, rename, commit del journal e ripresa.
- Diagnostica per dispositivo: proprietario dell'operazione, eta' della coda,
  revisioni e motivo preciso di ogni rinvio.
- Valutazione di Syncthing come alternativa completa, con una migrazione pilota
  separata e senza modificare il repository NASBox in produzione.
