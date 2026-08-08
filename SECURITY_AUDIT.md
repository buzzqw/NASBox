# Security Audit Notes

Audit statico eseguito il 2026-08-08. Le criticita qui elencate sono state
registrate su richiesta e **non sono state corrette**.

## Finding 1 - Alta, condizionata

Il servizio server systemd esegue come root uno script che puo risiedere in un
volume dati modificabile dall'utente SSH.

- Riferimenti: `server/install.sh:300-317`, `MANUALE.md:147-162`.
- Impatto: modifica del pacchetto con possibile esecuzione di codice come root.

## Finding 2 - Alta, condizionata

Il client usa `StrictHostKeyChecking=accept-new` prima del pinning esplicito.

- Riferimenti: `client/install.sh:202-203,230-251`,
  `client/sync_client/rsync_ops.py:268-283`.
- Impatto: MITM al primo collegamento, intercettazione della password durante
  `sshpass` e manipolazione dei trasferimenti.

## Finding 3 - Alta, condizionata

Il server accetta nel journal path non validati, inclusi `../`, e il client non
valida i path ricevuti dal manifest remoto.

- Riferimenti: `server/sync-daemon-server.sh:263-274`,
  `client/sync_client/rsync_ops.py:815-835`.
- Impatto: avvelenamento del manifest tra client, traversal e denial of
  service. Riprodotto con `../../outside.txt` pubblicato nel manifest.

## Finding 4 - Media, condizionata

Le operazioni server verificano i symlink prima dell'uso del path, ma senza
protezione atomica dalla sostituzione concorrente (TOCTOU).

- Riferimenti: `server/sync-daemon-server.sh:349-364,517-552,642-714`.
- Impatto: operazioni su percorsi fuori da `SHARE_ROOT` in presenza di un
  attaccante concorrente.

## Finding 5 - Media

La directory e i file di stato server vengono creati senza una policy di
permessi restrittiva.

- Riferimenti: `server/sync-daemon-server.sh:67-84,141-151,906-907`.
- Impatto: esposizione a utenti locali di log, nomi file, digest e metadati.
- Nel workspace auditato `server/state/` e `server.conf` risultano rispettivamente
  `755` e `644`.

## Finding 6 - Media, supply chain

`PyQt6` non e bloccato con hash/versione esatta e le GitHub Actions usano tag
mobili.

- Riferimenti: `requirements.txt:1`, `.github/workflows/test.yml:14-19`.
- Impatto: possibile introduzione di codice compromesso durante installazione o
  CI.
