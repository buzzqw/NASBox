# NASBox — User Manual

NASBox keeps a folder in sync across your computers through **your own NAS**. Your
files stay at home: no account, no third-party cloud. Change a file on one PC and
it shows up on the others a few seconds later.

This manual is for people who use NASBox. What changed in each version is in the
[CHANGELOG](CHANGELOG.md).

## Contents

1. [How it works](#1-how-it-works)
2. [Installing on the NAS](#2-installing-on-the-nas)
3. [Installing on a PC](#3-installing-on-a-pc)
4. [The window](#4-the-window)
5. [The tray icon](#5-the-tray-icon)
6. [Files: browse, versions, trash, conflicts](#6-files-browse-versions-trash-conflicts)
7. [Pause and speed limit](#7-pause-and-speed-limit)
8. [Folders you don't want to sync](#8-folders-you-dont-want-to-sync)
9. [The web interface](#9-the-web-interface)
10. [Connecting from outside your home](#10-connecting-from-outside-your-home)
11. [Keeping your data safe](#11-keeping-your-data-safe)
12. [NAS maintenance and notifications](#12-nas-maintenance-and-notifications)
13. [Updates](#13-updates)
14. [Troubleshooting](#14-troubleshooting)
15. [Commands and configuration files](#15-commands-and-configuration-files)

---

## 1. How it works

- Each PC has **one** NASBox folder. Whatever you put in it is copied to the NAS and,
  from there, to your other PCs.
- A small program on the NAS (the *daemon*) keeps the **history** of replaced and
  deleted files, serves the web interface and runs periodic maintenance.
- PCs talk to the NAS over **SSH**. There is no NASBox cloud in between.
- If two PCs edit the same file, the newest version wins by default and the other one
  is **kept** — never silently overwritten.

## 2. Installing on the NAS

You need a Linux NAS reachable over SSH with a key, and a shared folder for the data
(usually `/volume1/NASBox` on Synology).

From the [latest release](../../releases/latest) download the daemon for your NAS —
`nasbox-server-linux-arm64` for most ARM NAS models, `nasbox-server-linux-amd64` for
Intel/AMD — and `server.conf.example`. Then:

```bash
chmod +x nasbox-server-linux-arm64
cp server.conf.example server.conf      # set SHARE_ROOT=/volume1/NASBox
./nasbox-server-linux-arm64 -c server.conf --init-repository   # first time only
./nasbox-server-linux-arm64 -c server.conf --start
./nasbox-server-linux-arm64 -c server.conf --status
```

- Keep the program and `server.conf` on a data volume (not in `/tmp`, which many NAS
  models do not allow to run programs from).
- **Start at boot**: on Synology use *Control Panel → Task Scheduler → Triggered Task →
  Boot-up*, running the `--start` command above.
- For NAS models without a suitable binary, the shell-script installer
  (`server/install.sh`) is still available; it is slower but widely compatible.

## 3. Installing on a PC

From the same release page download the `.deb` package or the `nasbox-go-<version>`
program.

```bash
sudo apt install ./nasbox-go_<version>_amd64.deb     # Debian / Ubuntu
# or, for any distribution and without root:
client-go/install.sh nasbox-go
```

Start **NASBox**. On first run a wizard asks for:

1. the NAS address and user (it finds the NAS by itself on your local network);
2. your password, **once only** — it is used to install the SSH key on the NAS;
3. the local folder to synchronize.

After that it runs by itself and lives in the system tray. On a second PC repeat the
same steps and pick the same NAS.

> **Windows**: an *experimental* build is attached to each release
> (`…-windows-amd64-sperimentale.exe`). It has not been tested thoroughly yet: there
> are no automatic updates, so use it for trials only.

## 4. The window

By default the window shows three areas, designed for everyday use:

| Area | What it is for |
|---|---|
| **Status** | Tells you whether everything is in sync, what changed recently, the NAS free space and the **maximum speed**. When something needs your attention it says so. |
| **Files** | Browse the NAS, download, rename, previous versions, trash and conflicts. |
| **Settings** | Language and theme, NAS connection, folders to skip, version information and updates. |

The top bar always offers **Search**, **Open folder**, **Pause** and **Sync**.

At the bottom of the sidebar is **Advanced tools**. It reveals *Activity* (transfer
queue, waiting items, event log), *NAS* (monitor, devices, storage) and, in
Settings, the *Safety*, *Integrity* and *Advanced* tabs. You can ignore them; if a
message points to one of those screens, it opens by itself.

## 5. The tray icon

Clicking the icon shows a short menu:

- the current **status** and the last 3 synced files (click one to open its folder);
- **Show window** and **Open NASBox folder**;
- **Sync now**;
- **Pause 1 hour**, **Until tomorrow morning**, **More pauses…** — and **Resume**
  when paused;
- **Limit speed**: a switch (ticked = limited) and **More limits…**;
- **Conflicts to review** (only shown when there are some), **Settings** and **Quit**.

## 6. Files: browse, versions, trash, conflicts

- **Browse NAS**: breadcrumb path, sorting, download, rename, delete (goes to the NAS
  trash) and *download folder*. Each file shows a status: **Synced**, **Uploading**,
  **Downloading**, **Conflict**, **NAS only** or **Not synced**. The status is
  indicative (presence and size); a conflict or an in-flight transfer takes
  precedence. Use **Versions** to restore an earlier copy; **Cancel** on the Status
  screen stops the current transfer batch.
- **Previous versions**: every replaced or deleted file is kept. Open *Files →
  History / Trash* to see and restore them; in your file manager you can also
  right-click a file → **NASBox: previous versions**.
- **Conflicts**: when two PCs edit the same file, the newest version wins by default
  and the other stays available. In *Files → Conflicts* you compare versions, pick one
  or keep both.
- **Search**: type a few letters and find the file among everything that is synced;
  one click opens it.

The space used by history on the NAS is bounded by the daemon's retention rules (days
and maximum size): the oldest versions are removed first.

## 7. Pause and speed limit

- **Pause**: from the top bar or the tray. Stop syncing for an hour, until tomorrow
  morning, or for a duration of your choice; it resumes by itself. In Settings you can
  also schedule a recurring pause (for example at night).
- **Maximum speed**: on the *Status* screen (and in the tray menu) choose
  *Unlimited*, 256 KB/s, 512 KB/s, 1, 2, 5 or 10 MB/s, or a custom value. It applies to
  uploads and downloads and takes effect immediately. Separate upload and download
  limits are in *Settings → Advanced*.

## 8. Folders you don't want to sync

In *Settings → Folders & mirrors* you can:

- choose **which NAS folders** are downloaded to this PC (with the size of each) —
  handy on a laptop with little disk space;
- add **exclusion patterns** (for example `*.tmp`); the most common development
  folders (build output, caches, `.git`…) are already excluded the same way on every PC.

Excluding a folder **never deletes** what is already there.

## 9. Consultation mode (ghost)

A PC can be used only to look at what is on the NAS and to transfer individual
files, without keeping a local copy of the whole tree. Start it with:

```bash
nasbox -ghost gui
```

or set `"mode": "ghost"` in `~/.config/sync-daemon/client.json` (useful for
autostart on a dedicated PC). In ghost mode the sync engine never starts: no
watcher, no change feed, no periodic pull and no automatic upload. The window
opens directly on *File → Browse NAS*; the Status, Activity and NAS sections are
hidden.

- The **Go to folder** box opens any shared folder of the NAS tree; it changes
  only what this client shows, not the sync configuration.
- **Download** and **Download folder** copy files into the local folder chosen
  at setup (with both transfer backends).
- **Upload** publishes selected local files into the open NAS folder. New paths
  are journaled as new files; an existing path is replaced with a precondition
  and its previous version is kept in the NAS trash.
- From the shell: `nasbox browse [path]`, `nasbox download <path>` (recursive)
  and `nasbox upload <local-file> [remote-path]`.

The default mode is `normal`: an existing client keeps syncing exactly as before.

## 10. The web interface

The NAS daemon serves a web page so you can use your files from any browser, including
your phone: browse, search, preview (images, PDF, audio, video, text), download, see
previous versions, **upload** files by dragging them in, **rename** files and folders,
**delete** them to the NAS trash, and **create folders**.

Turn it on in `server.conf` on the NAS:

```ini
WEB_LISTEN=0.0.0.0:8080
WEB_USER=yourname
WEB_PASSWORD=a-long-password
```

then restart the daemon (`--restart`) and open `http://your-nas:8080`.

- Without `WEB_PASSWORD` the page is **read-only** and only reachable from the NAS
  itself.
- Files uploaded from the web reach all your PCs like any other file. If the name
  already exists, the new file becomes `name (1).ext`: nothing is ever overwritten.
- **Rename**, **Delete** and **New folder** appear when a password is configured.
  Rename never overwrites: a name collision is refused. Delete moves files and
  folders to the NAS trash and asks for confirmation, showing the number of files
  for a folder; deleted files remain recoverable from history, while a deleted
  folder is kept as a snapshot under the trash. An empty folder created from the
  web appears on the other PCs once it contains at least one file.
- **Share links**: the *Share* button next to a file creates a link (1, 7 or 30 days,
  or no expiry) that anyone can use without a password. *Shared links* lists, copies
  and **revokes** them. Only files can be shared, never folders. `WEB_PUBLIC_URL`
  sets the address written into the links.
- Other options: `WEB_MAX_UPLOAD_MB` (default 4096) and `WEB_TLS_CERT` / `WEB_TLS_KEY`
  for HTTPS.

> **Important**: the page uses plain HTTP. Use it at home, or behind a VPN or an HTTPS
> *reverse proxy*. **Do not open port 8080 to the Internet** without HTTPS: passwords
> and links would travel in the clear.

## 11. Connecting from outside your home

Away from home a PC reaches the NAS over SSH. In *Settings → General → NAS connection
and SSH* you can enter, besides the home address:

- the **outside address** (DNS name or public IP) and the SSH port;
- an optional **jump host** (another SSH server to go through) if the NAS cannot be
  reached directly.

The client picks the right path by itself: the local network when you are home, the
outside address elsewhere. The simplest and safest way to connect from outside is a
VPN (for example WireGuard or Tailscale) to your home network.

## 12. Keeping your data safe

NASBox is built so you don't lose files by mistake:

- **History**: nothing is lost in a replacement or a deletion.
- **Mass-deletion guard**: if too many files — or most of a folder — disappear in a
  short time, syncing **stops** and asks you to confirm (**Confirm deletions** button).
  A missing "sentinel" folder, usually an unmounted disk, always blocks deletions.
- **Cautious fallback**: if a file cannot be read or is still changing, that pass
  leaves it alone.
- **Integrity check**: *Settings → Integrity* verifies that files match what was
  recorded.
- **Interrupted operations**: if a delete, rename or restore is interrupted (for
  example by a power cut), the next sync lease completes or safely undoes it.
  *Status* and `nasbox doctor` show whether recovery work is pending; an ambiguous
  case is left untouched and reported instead of guessing.

NASBox synchronizes; it is **not a backup**. If you delete a file everywhere and empty
the history, no other copy exists. Keep a separate backup of the NAS.

## 13. NAS maintenance and notifications

At regular intervals (`CHECK_INTERVAL_MINUTES` in `server.conf`) the daemon:

- applies the **history retention** rules (days and maximum size);
- frees internal data that is no longer needed and verifies it weekly;
- makes a **daily copy** of its records (it keeps 7) in `state/backup`;
- checks that the files on disk and the daemon's list agree, and tells you if not.

To get a phone alert, set an **ntfy**-compatible address (or any plain-text webhook) in
`server.conf`:

```ini
NOTIFY_URL=https://ntfy.sh/a-long-secret-topic
NOTIFY_DISK_PERCENT=90
```

You get a message (at most one per kind per day) if the disk passes the threshold, if
the check finds damaged data, or if disk and records disagree. Messages contain only
numbers, never file names. All options, with their defaults, are described in
`server.conf.example`.

**Is everything healthy?** `nasbox-server --self-check` prints one `CHECK_*=ok|fail`
line per item (daemon, journal, last pass, free space, chunk quota, stale lock) and
exits non-zero if something needs attention. Point a DSM scheduled task, every 5
minutes, at `tools/nasbox-watchdog.sh`: it runs the check, restarts the daemon when it
is stopped, and logs the result. Without it, a dead daemon stays dead until someone
opens the app.

**Abandoned lock.** If a PC is suspended or loses the network while it holds the
global transfer lock, the lock can stay held. Clients from 1.27.0 send a heartbeat
every 30 seconds; set `SYNC_LOCK_MAX_AGE_MINUTES=120` and the daemon will stop a
`--transfer-wait` process that has been silent that long (the next sync recovers the
interrupted operation from its journal). Older clients are never force-released.

**Web access log.** Every request to the web UI and API is written to
`state/access.log` (timestamp, client, method, path, status, bytes, duration, user).
Share links appear as `/s/[token]` and the query string is dropped, so tokens and
paths with parameters never end up in the file. It rotates by size:
`ACCESS_LOG_MAX_BYTES` (default 5 MB) with `ACCESS_LOG_KEEP` generations
(default 3); `--status` shows the current size.

## 14. Updates

- **PC**: the app checks at startup and every few hours, verifies the detached
  Ed25519 signature of the manifest and the artifact hash, and goes back to the
  previous version by itself if the new one fails to start. A manifest without a
  valid signature is refused. If you installed the `.deb` package, updates come
  through your package manager. Manual check:
  *Settings → About → Check for updates*.
- **NAS**: the daemon can be updated from the app (*Settings → About → Check for
  server update*) after you confirm.

## 15. Troubleshooting

First thing to try:

```bash
nasbox-go doctor
```

It checks the connection, the SSH key, locks on the NAS, the daemon version and the
latest errors, and tells you what to do.

| Symptom | What to check |
|---|---|
| "NAS unreachable" | Is the NAS on? Are you on the network? Does `ssh user@nas` work from a terminal? Check the address and port in *Settings → NAS connection*. |
| "NAS busy" | Another PC is syncing: it is normal and it retries by itself. If it lasts, look at *Advanced tools → NAS*. |
| Syncing stops with a red warning | It is the mass-deletion guard: review the preview and confirm only if it is intended. |
| A file does not sync | It may be excluded, open in another program, or have an invalid name. See *Advanced tools → Activity*. |
| The tray menu does not appear | Some desktops need an extension for status icons (GNOME) or a plugin (XFCE). You can still use the window. |
| The web page keeps asking for the password | Check `WEB_USER` and `WEB_PASSWORD` in `server.conf`; after 5 wrong attempts the block lasts 10 minutes. |

If you need help, open an issue on the project page and attach the output of
`nasbox-go doctor` (it contains no passwords).

## 16. Commands and configuration files

```
nasbox-go gui                      open the app (single instance)
nasbox-go sync                     one sync pass
nasbox-go doctor [--json]          diagnostics
nasbox-go versions <file>          previous versions of a file
nasbox-go status | show            status / bring the window to the front
nasbox-go update-check | update-install
nasbox-go server-update-check | server-update-install
nasbox-go integrate [--remove]     file-manager menu entry
```

| What | Where |
|---|---|
| PC configuration | `~/.config/sync-daemon/client.json` |
| PC state, log and trash | `~/.local/state/sync-daemon/` |
| NAS configuration | `server.conf` (next to the program) |

On the NAS the useful commands are `--status`, `--print-config`, `--self-check`, `--restart`, `--stop`
and `--run-once` (runs maintenance right now).
