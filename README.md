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

Installa prima il pacchetto server su un volume dati persistente del NAS, poi
installa il client su ogni PC che deve partecipare alla sincronizzazione.

```bash
# Sul NAS, dopo avere copiato server/ in una cartella persistente
cd server
./install.sh

# Su ogni PC Linux
cd client
./install.sh
```

Il wizard crea o verifica la chiave SSH, configura il collegamento e registra
il client come servizio utente quando systemd e disponibile. La guida completa
e in [MANUALE.md](MANUALE.md).

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
