# NASBox

[![License: EUPL-1.2](https://img.shields.io/badge/License-EUPL--1.2-blue.svg)](LICENSE)
[![Python 3.11+](https://img.shields.io/badge/Python-3.11%2B-blue.svg)](https://www.python.org/)
[![Platform](https://img.shields.io/badge/Platform-Linux-lightgrey.svg)](#)
[![Donate](https://img.shields.io/badge/Donate-PayPal-00457C.svg)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=azanzani@gmail.com&item_name=Support+NASBox+Project)

## What is NASBox? / Cos'è NASBox?

**NASBox is your own private Dropbox.** You have a NAS at home (Synology,
QNAP, or any Linux box) and multiple PCs — desktop, laptop, maybe one at work.
NASBox keeps one folder perfectly in sync across all of them, through your NAS.
No cloud, no subscription, no third party touching your files. Everything goes
over SSH+rsync.

Files you drop in the NASBox folder appear on all your other PCs within seconds.
Delete a file, and its previous version is kept in `.sync-trash` for as long as
you want. Two people edit the same file at the same time? Both versions are
kept, labelled as conflicts, so no data is ever lost.

The NAS is the hub: clients never talk to each other directly. If you're outside
your home network, a bastion (jump host) routes the connection automatically.

---

**NASBox è il tuo Dropbox privato.** Hai un NAS a casa (Synology, QNAP o un
qualsiasi Linux) e più PC — fisso, portatile, magari uno al lavoro. NASBox
tiene una cartella perfettamente sincronizzata su tutti, passando dal tuo NAS.
Nessun cloud, nessun abbonamento, nessun dato in mano a terzi. Tutto viaggia
su SSH+rsync.

I file che metti nella cartella NASBox compaiono su tutti gli altri PC in pochi
secondi. Cancelli un file? La versione precedente resta in `.sync-trash` per
il tempo che decidi tu. Due persone modificano lo stesso file nello stesso
momento? Entrambe le versioni vengono conservate, etichettate come conflitti,
così nessun dato va mai perso.

Il NAS è il perno centrale: i client non parlano mai tra loro direttamente.
Se sei fuori casa, un bastione (jump host) instrada la connessione in automatico.

---

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
- Python 3.11 or later (`python3 --version`)
- PyQt6 (`pip install --user PyQt6` or your distro's package)
- `rsync` and `ssh` (pre-installed on virtually every Linux)
- `inotify-tools` strongly recommended for instant local change detection
  (`apt install inotify-tools` / `pacman -S inotify-tools`)

**NAS (Synology, QNAP, or any Linux box):**
- bash, rsync, OpenSSH, coreutils (`find`, `stat`, `date`, `awk`, `flock`)
  — all pre-installed on every NAS that can serve as an rsync target
- No Python, no external packages needed on the NAS

## Install / Installazione

### 1. Prepare the NAS (one time only)

Copy the `server/` folder to a **persistent data volume** on your NAS.
On Synology this means `/volume1/…`, not `/root/` or `/etc/` (those get reset
on firmware updates).

```bash
# Example — adjust the path to your NAS
scp -r server/ andres@192.168.1.119:/volume1/NASBox/sync-daemon-server/

# SSH into the NAS
ssh andres@192.168.1.119
cd /volume1/NASBox/sync-daemon-server

# Run the interactive installer as root so it can register the systemd service
sudo ./install.sh
```

The installer will:
- Ask for `SHARE_ROOT` — the NASBox folder path on the NAS (e.g. `/volume1/NASBox`)
- Ask for retention in days (how long deleted/overwritten files stay in `.sync-trash`)
- Create the `.nasbox-root` repository marker (clients refuse to sync without it)
- Register the daemon as a **systemd system service** for auto-start at boot
  (or a Synology `rc.d` hook, or a QNAP Task Scheduler entry, depending on
  platform)

If you run without `sudo`, the installer still configures everything and starts
the daemon, but warns that it can't register the system service — you'll need
to re-run `sudo ./install.sh` later, or start the daemon manually after each
NAS reboot with `./sync-daemon-server.sh --start`.

Verify it's running:

```bash
./sync-daemon-server.sh --status
# Should show: "Demone: IN ESECUZIONE (PID …)"
```

### 2. Install the client on each PC

```bash
# Clone or copy the repository to your PC
cd NASBox/client

# Run the interactive installer
./install.sh
```

The wizard does everything in one pass:

1. **SSH key** — generates an `ed25519` key pair if you don't already have one,
   then copies the public key to the NAS (`ssh-copy-id`). This is mandatory:
   NASBox uses SSH key authentication exclusively, never passwords.
2. **NAS connection** — asks for the NAS LAN host/IP, SSH user, and port.
   Optionally: a WAN host for direct outside access, and a bastion (jump host)
   for when you're away from home. See [§7 of the manual](MANUAL.md#7-the-bastion-case-nas-not-reachable-from-outside).
3. **NASBox folder** — asks where to put the synced folder on this PC
   (default: `~/NASBox`). Created if it doesn't exist.
4. **Launcher** — creates a `.desktop` entry so NASBox appears in your app menu.
5. **Auto-start** — if systemd is available, registers a user service so the
   client starts automatically at login.

When the wizard finishes, the client launches and starts syncing immediately.

### 3. Verify everything works

```bash
# Check the client is running
systemctl --user status sync-daemon-client.service

# Open the GUI
python3 ~/NASBox/sync-daemon/client/main.py

# Look at the Status tab — it should show "✅ Syncing — connected to <NAS>"
# Drop a test file in your NASBox folder and watch it appear on the NAS
```

### 4. Repeat on every PC

Run `client/install.sh` on each PC you want to keep in sync. Point them all
to the same NAS and the same `remote_prefix`. Everything else is automatic.

### Keeping the client up to date

At every startup the client checks for a newer version:
- In `client-update/` or `.update/` next to the installed `client/` folder
- On the NAS, in `.nasbox-client-update` under `remote_prefix`

If a newer version is found, it asks for confirmation, replaces itself
atomically, and restarts. To push an update: copy the new `client-update/`
folder to one of these locations.

### Keeping the server up to date

Copy the new `server/` folder to the NAS, overwriting the `.sh` files (do NOT
overwrite `server.conf`), then:

```bash
./sync-daemon-server.sh --restart
./sync-daemon-server.sh --status   # confirm it's running with the new version
```

The client detects outdated servers automatically and notifies you.

---

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
