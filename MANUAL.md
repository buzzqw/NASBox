# NASBox — Manual

Multi-PC file sync through a NAS acting as hub: each PC runs a graphical client
that keeps a single local folder — the **NASBox folder** — synced with the NAS;
the NAS runs a lightweight daemon that enforces history retention. All connected
PCs see, within a few tens of seconds at most, the files others put in it.

The model is deliberately Dropbox-like: **one** folder per PC, no share lists
to add/modify/remove by hand. Fewer things to configure means fewer ways to
break things.

## Index

1. [Architecture overview](#1-architecture-overview)
2. [Requirements](#2-requirements)
3. [Client installation (PC)](#3-client-installation-pc)
4. [Server package installation (NAS)](#4-server-package-installation-nas)
5. [SSH keys (mandatory)](#5-ssh-keys-mandatory)
6. [NAS connection (Settings tab)](#6-nas-connection-settings-tab)
7. [The bastion case (NAS not reachable from outside)](#7-the-bastion-case-nas-not-reachable-from-outside)
8. [The NASBox folder](#8-the-nasbox-folder)
9. [Bandwidth limit](#9-bandwidth-limit)
10. [Sync frequency](#10-sync-frequency)
11. [Pause and timed pause](#11-pause-and-timed-pause)
12. [Transfer queue](#12-transfer-queue)
13. [Maintenance: conflict cleanup](#13-maintenance-conflict-cleanup)
14. [Tray icon](#14-tray-icon)
15. [Log](#15-log)
16. [History / Trash](#16-history--trash)
17. [Configuration files and paths](#17-configuration-files-and-paths)
18. [Server daemon commands (NAS)](#18-server-daemon-commands-nas)
19. [Troubleshooting](#19-troubleshooting)

---

## 1. Architecture overview

```
   PC client A ──┐                                    ┌── PC client B
   (on LAN)       │                                    │   (outside home)
                  │                                    │
                  ▼                                    ▼
            ┌───────────┐  direct SSH+rsync       ┌────────────────┐
            │    NAS    │◄───────────────────────┤  Bastion PC    │◄── SSH (ProxyJump)
             │192.0.2.10 │  SSH+rsync (bastion      │ public          │
             │           │  sees NAS on LAN)        │bastion.example │
            └───────────┘                        └────────────────┘
```

- **Client** (`client/`): Python + PyQt6 graphical app. Keeps one local folder
  (default `~/NASBox`) synced root-to-root with one folder on the NAS: detects
  local changes almost in real time and sends them to the NAS (push), and
  periodically downloads what other PCs have sent in the meantime (pull).
- **Server** (`server/`): standalone bash daemon installed on the NAS. It does
  not participate in file transfers (clients do that via SSH/rsync directly) —
  it only enforces history retention, independently of which clients are on or
  off. It is also the single source of truth for the NASBox folder path and
  retention: clients read it from there instead of retyping it on each one.
- **History/trash**: every time a file is overwritten or deleted, the previous
  version does not disappear: it is renamed with a high-precision UTC timestamp
  and moved to `.sync-trash/`.

## 2. Requirements

**Client PC:**
- Python 3.11+
- PyQt6 (`pip install --user PyQt6`)
- `rsync`, `ssh` (almost always already present)
- `inotify-tools` (**recommended, not mandatory**): provides `inotifywait`,
  which lets the client detect local changes instantly instead of polling every
  2 seconds. Without it, the client still works (polling fallback) but with a
  detection delay of a few seconds.

**NAS (Synology, QNAP, or generic Linux):**
- `bash`, `rsync`, `ssh`/`sshd`, coreutils (`find`, `stat`, `date`, `awk`,
  `flock`) — already present on any NAS that can serve as an rsync target.
- No additional dependencies: no Python, no external packages.

## 3. Client installation (PC)

```bash
cd client
./install.sh
```

The script is an **end-to-end wizard**: when it finishes the client is ready
to sync, not just installed. In order:
1. checks/installs SSH key if missing
2. asks for NAS connection details
3. sets up the NASBox folder path
4. creates a `.desktop` launcher
5. optionally registers a systemd user service

If you skip the guided setup (or the NAS was unreachable at the time), the
first app launch still asks where to put the NASBox folder, and the rest
(NAS connection, bandwidth, retention, exclusions) is configured from the
**Settings** tab at your leisure.

### Client auto-update

Before building the main window, the client looks for a newer version:
- in `client-update/` next to the launcher or next to the `client/` folder
- in `.update/` inside the `client/` folder
- on the NAS, in `.nasbox-client-update` under `remote_prefix`
If it finds a newer version, it asks for confirmation, replaces the client
atomically, and restarts it.

Repeat this installation on **every PC** you want to participate in the sync.

## 4. Server package installation (NAS)

Copy the entire `server/` folder to a **persistent data volume** on the NAS,
e.g.:
- Synology: `/volume1/NASBox/sync-daemon-server/`
- QNAP: `/share/CACHEDEV1_DATA/.../sync-daemon-server/`

Then, via SSH on the NAS:

```bash
cd /volume1/NASBox/sync-daemon-server
./install.sh
```

The installer is a **wizard** that:
1. detects the platform (Synology/QNAP/generic Linux)
2. creates `server.conf` from `server.conf.example` if missing
3. asks for `SHARE_ROOT` and retention
4. initializes the `.nasbox-root` repository marker
5. registers the daemon for auto-start

The daemon is a single always-running process with its own internal timer — no
cron / Task Scheduler involved.

## 5. SSH keys (mandatory)

NASBox uses SSH+rsync for every transfer. Password authentication is not
supported: the key must be authorized on the NAS beforehand.

`install.sh` already generates an `ed25519` key and helps copy it to the NAS.
If you already have a key and want to use it, skip the generation step: the
installer detects existing keys.

## 6. NAS connection (Settings tab)

Settings → **NAS connection**: LAN host, optional direct WAN host, SSH user,
port. "Verify SSH keys…" shows host key fingerprints for explicit acceptance.

"Detect from NAS" connects, checks the server daemon is running, and reads the
remote path and retention directly from it — nothing to copy by hand.

## 7. The bastion case (NAS not reachable from outside)

If the NAS is not directly reachable from the internet but you have a machine
that's publicly reachable AND can see the NAS on its own LAN, configure it as
**bastion** in Settings. The client uses it automatically (SSH ProxyJump) only
when direct access to the NAS is not available.

## 8. The NASBox folder

A single folder per PC, mirrored root-to-root with the NAS. You don't add
"shares": there's only one. The NAS is the hub — the model is hub-and-spoke,
not peer-to-peer. Clients never talk to each other directly.

## 9. Bandwidth limit

Settings → **Bandwidth limit**: upload and download limits in KB/s, 0 =
unlimited. Applied starting with the next sync cycle.

## 10. Sync frequency

Settings → **Sync frequency**: how often the client checks the NAS for changes
made by other PCs. Local changes are sent almost immediately (as soon as
detected). This interval only affects downloading others' changes.

## 11. Pause and timed pause

From the **Status** tab or the tray icon: "Pause" suspends uploads and
downloads until you resume. "Pause for…" resumes automatically after the chosen
duration. A transfer in progress is interrupted immediately.

## 12. Transfer queue

**Transfers** tab: list of what's waiting to be uploaded (↑), downloaded (↓),
or deleted (🗑), with size.

The list is a **preview of the current queue** calculated via a non-writing
check; it's not the operations history. It updates during and after syncing.

- **Live speed**: two indicators ("↑ Upload" / "↓ Download") show real-time
  speed while a transfer is in progress, read directly from rsync's progress
  output.
- Items are removed from the queue **as soon as they complete**, one by one.
- **Search**: live filename filter above the table.
- **Status messages**: the progress bar and activity label indicate exactly what
  the program is doing at every moment: building the plan, comparing with the
  NAS, updating the preview, transferring files. Elapsed seconds appear next to
  each phase.
- During a preview scan (`rsync --dry-run`) that can last minutes on very large
  folders, the bar shows "Queue: updating preview…" instead of pretending
  nothing is happening.

## 13. Maintenance: conflict cleanup

When two versions of the same file are modified at the same time on different
PCs, NASBox keeps both to avoid data loss: one stays in place, the other is
renamed with ` (conflitto da <device> <token>)` in the filename. The Log shows
them as `CONFLICT` actions.

These files persist until deleted by hand. In the **Settings** tab, below
**About**, there is a **Maintenance** section with:

- **Scan for conflicts**: scans the NASBox folder and counts files with
  `(conflitto da ` in their name.
- **Delete found conflicts**: enabled only after a scan. Confirms with a
  warning: the deletion is permanent, not through the trash. Before proceeding,
  check the file contents: they may contain changes you want to keep.

In the future, conflicts will be managed with automatic retention; for now they
must be removed manually.

## 14. Tray icon

The app stays active in the background when you close the window (tray icon
near the clock). Right-click menu: open folder, recent files, pause/resume,
open main window, quit.

## 15. Log

**Log** tab: chronological list of every upload, download, deletion, error,
conflict, and server event. Filter by action type and search by path or detail.

## 16. History / Trash

### Local trash

Every time a file is overwritten or deleted during a sync, the previous version
is moved to `.sync-trash/` inside the NASBox folder. The **History / Trash**
tab lists the local trash and allows restoring or permanently deleting.

### NAS history

The NAS also keeps its own `.sync-trash/` managed by the server daemon with
independent retention. The History tab can display and restore files from the
NAS history as well.

## 17. Configuration files and paths

| Path | Content |
|------|---------|
| `~/.config/sync-daemon/client.json` | Client configuration |
| `~/.config/sync-daemon/known_hosts` | SSH host keys |
| `~/.local/state/sync-daemon/sync_state.db` | Common baseline (SQLite) |
| `~/.local/state/sync-daemon/client.log` | Transfer log |
| `~/NASBox/.sync-trash/` | Local trash |
| `<server_path>/server.conf` | Server daemon config |
| `<server_path>/state/` | Server runtime (log, PID, journal, manifest) |

## 18. Server daemon commands (NAS)

```bash
./sync-daemon-server.sh --start              # Start daemon in background
./sync-daemon-server.sh --stop               # Stop it
./sync-daemon-server.sh --restart            # Stop + start
./sync-daemon-server.sh --run-foreground     # Loop in foreground (debug)
./sync-daemon-server.sh --run-once [--dry-run]   # Single pruning pass
./sync-daemon-server.sh --status             # Status + history/retention
./sync-daemon-server.sh --print-config       # KEY=VALUE configuration
./sync-daemon-server.sh --journal-compact    # Compact journal into manifest
./sync-daemon-server.sh --manifest-export    # Export current manifest
./sync-daemon-server.sh --manifest-get PATH  # Read state of one path
./sync-daemon-server.sh --file-states        # Batch file/tombstone state
./sync-daemon-server.sh --checked-delete     # Conditional deletion
./sync-daemon-server.sh --init-repository    # Create .nasbox-root marker
./sync-daemon-server.sh -c FILE              # Use alternate server.conf
```

Configuration in `server.conf`:

```ini
SHARE_ROOT=/volume1/NASBox        # The NASBox folder on the NAS
RETENTION_DAYS=90                 # Retention days for .sync-trash (0 = never)
TOMBSTONE_TTL_DAYS=0              # Days after which tombstones are removed (0 = never)
SYNC_LOCK_MAX_AGE_MINUTES=10      # Minutes after which a stale lock is forced (0 = never)
CHECK_INTERVAL_MINUTES=60         # How often the daemon checks and prunes
LOG_MAX_BYTES=5242880             # Log rotation
JOURNAL_MAX_BYTES=10485760        # Auto journal compaction
```

**Tombstones (TOMBSTONE_TTL_DAYS):** when a file is deleted, the manifest keeps
a tombstone to prevent an offline client from resurrecting the file. With
TTL > 0, tombstones older than TTL days are removed from the manifest. At 0
(default) they are kept forever.

**Stale locks (SYNC_LOCK_MAX_AGE_MINUTES):** if a client dies while holding the
sync lock (SSH connection dropped, crash), the `flock` process on the NAS can
stay alive and block all other clients. With MAX_AGE > 0, the daemon finds and
kills locks older than MAX_AGE minutes on each pruning pass. At 0 the cleanup
is disabled.

## 19. Troubleshooting

**"NAS unreachable" in Status tab**
- Try manually: `ssh -p <port> <user>@<lan_host>` from the same PC.
- If using a bastion, also try: `ssh -p <bastion_port> <user>@<bastion>`
  and from there `ssh <user>@<nas_lan_host>`.
- Check that the SSH key is authorized on both.

**"NASBox folder not configured" in Status tab**
- Click "Change location…" in the "NASBox folder" box and choose one.

**"Detect from NAS" fails**
- Verify "Server script on the NAS" points to the exact path of
  `sync-daemon-server.sh` (not the folder, the file).
- Try manually `ssh <user>@<host> <path>/sync-daemon-server.sh --print-config`.

**"Server update required" notification**
- The `server/` package version on the NAS is older than what this client
  expects. Copy the newer `server/` folder to the NAS, overwriting `.sh` files
  (leave `server.conf` alone), then `./sync-daemon-server.sh --restart`.

**"sync lock not acquired on NAS" in Log**
- Means the client cannot acquire the remote lock within 45 seconds. Usually
  a stale lock left by a crashed client.
- On the NAS: `ps aux | grep sync-transfer` — if you see `flock` processes
  older than a few minutes, kill them manually (`kill <PID>`).
- The server daemon 3.9.1+ automatically cleans stale locks if
  `SYNC_LOCK_MAX_AGE_MINUTES > 0` (default 10 minutes).
- Also check that the server daemon is running (`./sync-daemon-server.sh --status`).

**Files not syncing but connection appears active**
- Check the **Log** tab, filter `ERROR`: almost always a permissions problem
  on the remote folder or the NAS being out of space.
- Verify with "Detect from NAS" that the "NASBox folder on the NAS" really
  matches `SHARE_ROOT` on the NAS.
- Also check Settings → "Exclude from sync": a pattern that's too broad (e.g.
  `*` by mistake) excludes everything without any error.

**Server daemon doesn't restart after NAS reboot**
- `./sync-daemon-server.sh --status` to check if it's running.
- If using systemd: `systemctl status sync-daemon-server`.
- On Synology without systemd: check that
  `/usr/local/etc/rc.d/sync-daemon-server.sh` exists and is executable.

**Preview what retention would delete**
- Always use `--run-once --dry-run` on the NAS first: it lists what would be
  removed without deleting anything.
