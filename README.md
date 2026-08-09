# NASBox

NASBox sincronizza una cartella tra piu PC tramite un NAS, usando SSH e rsync.
Il client grafico PyQt6 gestisce caricamenti, scaricamenti, coda, storico e
cestino; il piccolo demone sul NAS applica la retention dello storico.

## Funzionalita

- Una cartella NASBox per ogni PC, sincronizzata root-to-root con il NAS.
- Trasferimenti SSH/rsync, supporto LAN, WAN diretta e bastione SSH.
- Coda con anteprima, velocita live, pausa e sincronizzazione manuale.
- Storico locale e remoto, ripristino e pulizia del cestino locale.
- Retention sul NAS indipendente dai client accesi.
- Marker persistente del repository e blocco delle cancellazioni se il volume
  non è quello verificato.
- Verifica esplicita delle chiavi host SSH e limite ai batch anomali di
  cancellazione.

## Requisiti

Client Linux:

- Python 3.11 o superiore
- PyQt6, rsync e OpenSSH
- `inotify-tools` consigliato per rilevare subito le modifiche locali

NAS:

- bash, rsync, OpenSSH e le utility standard `find`, `stat`, `date` e `flock`

## Installazione

Il checkout Git ufficiale sul NAS è `/volume1/Varie/sync-daemon`. La cartella
sincronizzata è separata e deve rimanere `/volume1/NASBox`: non spostarla e non
usarla come checkout del codice.

```bash
# Prima installazione sul NAS
git clone https://github.com/buzzqw/NASBox.git /volume1/Varie/sync-daemon
cd /volume1/Varie/sync-daemon
sudo ./server/install.sh

# Aggiornamento successivo del sorgente sul NAS
git -C /volume1/Varie/sync-daemon pull --ff-only

# Su ogni PC Linux, da una copia temporanea della cartella client/
scp -r <utente>@<nas>:/volume1/Varie/sync-daemon/client /tmp/nasbox-client
cd /tmp/nasbox-client
./install.sh
```

Il wizard server chiede `SHARE_ROOT`: usa `/volume1/NASBox`. Il wizard client
crea o verifica la chiave SSH, configura il collegamento e registra il client
come servizio utente quando systemd è disponibile. La guida completa è in
[MANUALE.md](MANUALE.md).

## Sviluppo e test

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
PYTHONPATH=client .venv/bin/python -m unittest discover -s client/tests -v
```

## Sicurezza

Non pubblicare `server/server.conf`, file sotto `server/state/`, chiavi SSH,
log o configurazioni client personali. Questi file sono esclusi dal repository
da `.gitignore`. Leggi [SECURITY.md](SECURITY.md) prima di segnalare una
vulnerabilita.

## Licenza

NASBox e source-available ma **non open source**. La [licenza](LICENSE)
consente uso personale, didattico e di valutazione non commerciale. Uso in
azienda, rivendita, hosting o qualunque altro uso commerciale richiedono una
licenza commerciale autorizzata per iscritto dal titolare del copyright.
