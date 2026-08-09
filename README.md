# NASBox

## English

NASBox synchronizes one folder between multiple PCs through a NAS using SSH
and rsync. The PyQt6 client manages uploads, downloads, queues, history and
trash; the small NAS daemon applies remote history retention.

### Features

- One NASBox folder per PC, synchronized root-to-root with the NAS.
- SSH/rsync transfers with LAN, direct WAN and SSH bastion support.
- Transfer queue with preview, live speed, pause and manual synchronization.
- Local and remote history, restore and local trash cleanup.
- NAS-side retention independent of connected clients.
- Persistent repository marker and deletion safeguards when the NAS volume is
  not the verified one.
- Explicit SSH host-key verification and protection against anomalous delete
  batches.

### Requirements

Client Linux:

- Python 3.11 or newer.
- PyQt6, installed from `requirements.txt` (`PyQt6>=6.5,<7`).
- rsync and OpenSSH.
- `inotify-tools` is recommended for immediate local change detection.

NAS:

- bash, rsync, OpenSSH and standard utilities such as `find`, `stat`, `date`
  and `flock`.
- No Python or external Python packages are required on the NAS.

### Installation

The canonical Git checkout on the NAS is `/volume1/Varie/sync-daemon`. The
synchronized data root is separate and must remain `/volume1/NASBox`; it is
not a source checkout.

First installation on the NAS:

```bash
git clone https://github.com/buzzqw/NASBox.git /volume1/Varie/sync-daemon
cd /volume1/Varie/sync-daemon
sudo ./server/install.sh
```

When prompted, set `SHARE_ROOT` to `/volume1/NASBox`.

Update the source and restart the daemon:

```bash
git -C /volume1/Varie/sync-daemon pull --ff-only
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --restart
```

Install the client on each Linux PC from a temporary copy of `client/`:

```bash
scp -r <user>@<nas>:/volume1/Varie/sync-daemon/client /tmp/nasbox-client
cd /tmp/nasbox-client
./install.sh
```

The installer creates or verifies SSH keys, configures the connection and
registers the client as a user service when systemd is available. The client
runtime is installed under `~/NASBox/sync-daemon`; it must not contain `.git`
and must not be used as a source repository.

Useful checks:

```bash
systemctl --user status sync-daemon-client.service
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --status
```

### Development and Tests

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
PYTHONPATH=client .venv/bin/python -m unittest discover -s client/tests -v
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh client/install.sh
```

### Security

Do not publish `server/server.conf`, files under `server/state/`, SSH keys,
logs or personal client configurations. These files are excluded by
`.gitignore`. Read [SECURITY.md](SECURITY.md) before reporting a vulnerability.

### License

NASBox is source-available but **not open source**. The [license](LICENSE)
permits personal, educational and evaluation use only. Commercial use requires
a written commercial license from the copyright holder.

---

## Italiano

NASBox sincronizza una cartella tra piu PC tramite un NAS, usando SSH e rsync.
Il client grafico PyQt6 gestisce caricamenti, scaricamenti, coda, storico e
cestino; il piccolo demone sul NAS applica la retention dello storico remoto.

### Funzionalita

- Una cartella NASBox per ogni PC, sincronizzata root-to-root con il NAS.
- Trasferimenti SSH/rsync con supporto LAN, WAN diretta e bastione SSH.
- Coda trasferimenti con anteprima, velocita live, pausa e sincronizzazione
  manuale.
- Storico locale e remoto, ripristino e pulizia del cestino locale.
- Retention sul NAS indipendente dai client accesi.
- Marker persistente del repository e protezione dalle cancellazioni quando il
  volume NAS non e quello verificato.
- Verifica esplicita delle chiavi host SSH e protezione dai batch anomali di
  cancellazione.

### Requisiti

Client Linux:

- Python 3.11 o superiore.
- PyQt6, installato da `requirements.txt` (`PyQt6>=6.5,<7`).
- rsync e OpenSSH.
- `inotify-tools` consigliato per rilevare subito le modifiche locali.

NAS:

- bash, rsync, OpenSSH e utility standard come `find`, `stat`, `date` e
  `flock`.
- Sul NAS non servono Python o pacchetti Python esterni.

### Installazione

Il checkout Git canonico sul NAS e `/volume1/Varie/sync-daemon`. La cartella
sincronizzata e separata e deve rimanere `/volume1/NASBox`: non e un checkout
del sorgente.

Prima installazione sul NAS:

```bash
git clone https://github.com/buzzqw/NASBox.git /volume1/Varie/sync-daemon
cd /volume1/Varie/sync-daemon
sudo ./server/install.sh
```

Quando richiesto, impostare `SHARE_ROOT` a `/volume1/NASBox`.

Aggiornamento del sorgente e riavvio del demone:

```bash
git -C /volume1/Varie/sync-daemon pull --ff-only
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --restart
```

Installare il client su ogni PC Linux partendo da una copia temporanea di
`client/`:

```bash
scp -r <utente>@<nas>:/volume1/Varie/sync-daemon/client /tmp/nasbox-client
cd /tmp/nasbox-client
./install.sh
```

L'installer crea o verifica le chiavi SSH, configura il collegamento e registra
il client come servizio utente quando systemd e disponibile. Il runtime del
client viene installato in `~/NASBox/sync-daemon`; non deve contenere `.git` e
non deve essere usato come repository del sorgente.

Controlli utili:

```bash
systemctl --user status sync-daemon-client.service
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --status
```

### Sviluppo e test

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
PYTHONPATH=client .venv/bin/python -m unittest discover -s client/tests -v
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh client/install.sh
```

### Sicurezza

Non pubblicare `server/server.conf`, i file sotto `server/state/`, chiavi SSH,
log o configurazioni client personali. Questi file sono esclusi da
`.gitignore`. Leggere [SECURITY.md](SECURITY.md) prima di segnalare una
vulnerabilita.

### Licenza

NASBox e source-available ma **non open source**. La [licenza](LICENSE)
consente uso personale, didattico e di valutazione non commerciale. L'uso
commerciale richiede una licenza commerciale autorizzata per iscritto dal
titolare del copyright.
