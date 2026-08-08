# NASBox

[![License: EUPL-1.2](https://img.shields.io/badge/License-EUPL--1.2-blue.svg)](LICENSE)
[![Python 3.11+](https://img.shields.io/badge/Python-3.11%2B-blue.svg)](https://www.python.org/)
[![Platform](https://img.shields.io/badge/Platform-Linux-lightgrey.svg)](#)
[![Donate](https://img.shields.io/badge/Donate-PayPal-00457C.svg)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=azanzani@gmail.com&item_name=Support+NASBox+Project)

**NASBox** syncs a folder across multiple PCs through a NAS, using SSH and rsync.
A PyQt6 graphical client handles uploads, downloads, queue, history and trash;
a small daemon on the NAS enforces history retention.

---

**NASBox** sincronizza una cartella tra più PC tramite un NAS, usando SSH e rsync.
Il client grafico PyQt6 gestisce caricamenti, scaricamenti, coda, storico e
cestino; un piccolo demone sul NAS applica la retention dello storico.

---

## Features / Funzionalità

- One NASBox folder per PC, synced root-to-root with the NAS
- SSH/rsync transfers, LAN, direct WAN, and SSH bastion support
- Queue with live preview, real-time speed, pause, and manual sync
- Local and remote history, restore, and local trash cleanup
- NAS-side retention independent of which clients are online
- Persistent repository marker with safety checks against wrong volumes
- Explicit SSH host key verification and anomalous batch delete limits
- Conflict file detection and manual cleanup from the Settings tab
- Tombstone GC and stale lock cleanup (server 3.9.1+)

---

## Requirements / Requisiti

**Client (Linux):**
- Python 3.11+
- PyQt6, rsync, OpenSSH
- `inotify-tools` recommended for instant local change detection

**NAS:**
- bash, rsync, OpenSSH, and standard utilities (`find`, `stat`, `date`, `flock`)

## Install / Installazione

Install the server package on a persistent NAS data volume first, then install
the client on each PC.

```bash
# On the NAS — after copying server/ to a persistent folder
cd server
./install.sh

# On each Linux PC
cd client
./install.sh
```

The wizard creates or verifies the SSH key, sets up the connection, and
registers the client as a user service when systemd is available.
Full guide in [MANUALE.md](MANUALE.md) (Italian) / [MANUAL.md](MANUAL.md) (English).

At startup the client also checks for a newer bundle in the startup path or
on the NAS and, on confirmation, updates and restarts itself.

## Dev & Test / Sviluppo e test

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
PYTHONPATH=client .venv/bin/python -m unittest discover -s client/tests -v
```

## Security / Sicurezza

Never publish `server/server.conf`, files under `server/state/`, SSH keys, logs,
or personal client configurations. These are excluded from the repository by
`.gitignore`.

## Disclaimer / Limitazione di responsabilità

**This software is provided "as is", without warranty of any kind.** The author
assumes no responsibility for data loss, corruption, or any other damage
resulting from its use. You are solely responsible for verifying that the
software meets your needs and for maintaining backups of your data.

**Questo software è fornito "così com'è", senza alcuna garanzia.** L'autore non
si assume alcuna responsabilità per perdita di dati, corruzione o qualsiasi
altro danno derivante dal suo utilizzo. Sei l'unico responsabile della verifica
che il software soddisfi le tue esigenze e del mantenimento di copie di backup
dei tuoi dati.

## License / Licenza

NASBox is licensed under the **EUPL-1.2** (European Union Public License).
See [LICENSE](LICENSE) for the full text.

NASBox è rilasciato sotto licenza **EUPL-1.2** (European Union Public License).
Vedi [LICENSE](LICENSE) per il testo completo.

## Author / Autore

**Andres Zanzani**

[![GitHub](https://img.shields.io/badge/GitHub-buzzqw%2FNASBox-blue?logo=github)](https://github.com/buzzqw/NASBox)

## Support / Supporto

If you find NASBox useful, you can support the project:

Se trovi NASBox utile, puoi supportare il progetto:

[![Donate with PayPal](https://img.shields.io/badge/Donate-PayPal-blue.svg)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=azanzani@gmail.com&item_name=Support+NASBox+Project)
