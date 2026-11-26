# NASBox — your cloud, on your NAS

NASBox synchronizes a folder between multiple PCs through a NAS over SSH and
rsync. Your data stays on your own storage: no account, no third-party cloud
service.

> **The official program is NASBox Go** (`client-go/`, Go + Fyne, a single binary
> for X11 and Wayland). The old PyQt6/Python client is **legacy and retired**: it
> receives no further updates and its update channel is no longer published.
> It is only useful for migrating data, which stays in the same place.

The staged plan for a self-hosted, Dropbox-like NAS cloud is in
[`ROADMAP.md`](ROADMAP.md) (storage, metadata, reliability) and
[`roadmap-claude.md`](roadmap-claude.md) (what is missing to replace Dropbox:
simpler GUI, web browse/upload, Windows client, on-demand files).

| Component | Current version |
|---|---|
| NASBox Go client (official) | **1.19.0** |
| NASBox Go server (`client-go/cmd/nasbox-server`) | **3.36.0** |

### Official client

NASBox Go is the only supported client. It is a single Fyne binary
(`nasbox-go`) for Linux/X11 and Linux/Wayland. The legacy PyQt6 client is
retired: its update channel is no longer published and no new work targets it.
Configuration, local state, history and trash are shared with the old client,
so migrating does not move any data.

The NAS runs the NASBox Go daemon. The default datapath is the chunk protocol:
only missing content-addressed chunks are transferred, with deduplication between
files and versions. The daemon owns the SQLite metadata index, journal, manifest,
remote history and global transfer lease. The shell daemon and rsync remain a
compatibility fallback for older installations.

### Features

- Continuous bidirectional sync with immediate change detection (inotify) and
  a NAS change feed; several PCs share one NASBox root and are serialized by a
  lease on the NAS.
- Nothing is silently lost: replaced and deleted files go to NAS history
  (`.sync-trash`) and local trash. "File versions" lists and restores them,
  also from the file manager context menu.
- Conflicts: newest wins by default, or resolve them manually; the losing copy
  stays recoverable. A Conflict Center shows metadata, a text diff and a
  recommended version.
- Deletion guards (mass deletion, disappearing folders, sentinels, fail-closed
  on unreadable files) and confirmation of held deletions.
- GUI with a **simple view by default** (Home, Files, Settings: sync state,
  recent activity, NAS space, file browser, versions/trash). Queue, log, NAS
  monitor, devices, storage and counters live behind "Advanced tools" in the
  sidebar. Also live transfers (file, speed, ETA), "Check system" diagnostics,
  timed tray pauses and selective sync of NAS folders.
- **Web UI** (served by the NAS daemon, `WEB_LISTEN`): browse, search, preview,
  download and upload from any browser, including versions of a file. Uploads
  go through the same transactional publish as the PCs, so every device receives
  them. Read-only without a password; writes need `WEB_PASSWORD`.
- Tray menu with recent files, timed pauses and a one-click bandwidth limit
  (also on the Home screen).
- Server maintenance: `--chunk-gc` (garbage collection of unreferenced chunks,
  24 h grace) and `--chunk-fsck [--repair]` (SHA-256 verification of the chunk
  store).
- Desktop integration: "NASBox: versioni precedenti" in Thunar, Nautilus, Nemo,
  Dolphin and Caja, plus a status emblem on the NASBox folder.
- Automatic client updates with verification and rollback; the server can be
  updated from the client after confirmation.
- `nasbox-go doctor` reports server, rsync (local and NAS), locks, capabilities
  and the last 24 hours of errors.
- NAS capabilities are detected at bootstrap (CPU, memory, disks, rsync,
  filesystem, change feed) and drive realtime mode, hashing parallelism and
  power saving. A private rsync 3.5.1 can be installed on the NAS; the system
  rsync 3.1.2 remains the fallback.

### Requirements

- Linux PC (X11 or Wayland) with `ssh` and, preferably, `inotify-tools`.
  `rsync` ≥ 3.1 is needed only for the legacy transfer backend.
- NAS reachable over SSH with a key and the NASBox Go server binary. The shell
  fallback additionally requires `bash`, `flock`, `sha256sum`, `rsync` (even
  3.1.2, as on Synology DSM) and standard shell utilities.
- Go 1.27+ and a CGO-capable toolchain are needed only to build the client.

### Install and update the client

Download `nasbox-go-<version>` (linux-amd64) or the `.deb` package from the
GitHub release of the chosen version, then:

```bash
# user install: ~/.local/bin, desktop entry, autostart
client-go/install.sh nasbox-go

# migrate from the retired Python client (same config/state, no data copy)
client-go/install.sh --migrate nasbox-go

# Debian/Ubuntu package (updates go through the package manager)
sudo apt install ./nasbox-go_<version>_amd64.deb
```

Both clients must never run at the same time: they share the configuration and
the instance lock. Config: `~/.config/sync-daemon/client.json`; state, log and
trash: `~/.local/state/sync-daemon/`.

Installed from `~/.local/bin`, the client checks the `go-stable` channel at
startup and every 6 hours, verifies SHA-256 and size, and rolls back if the new
binary fails to start twice. Manual check:

```bash
nasbox-go update-check
nasbox-go update-install        # requires a restart of the GUI
nasbox-go doctor
```

The channel is `/volume1/NASBox/.nasbox-client-update-go/` on the NAS
(`manifest-go.json` + `nasbox-go-<version>`); it never uses the retired Python
manifest.

### Install and update the NAS server

The canonical checkout lives on the NAS only:

```bash
git clone https://github.com/buzzqw/NASBox.git /volume1/Varie/sync-daemon
cd /volume1/Varie/sync-daemon
sudo ./server/install.sh          # set SHARE_ROOT=/volume1/NASBox
./server/sync-daemon-server.sh --restart
./server/sync-daemon-server.sh --print-config
```

The server version is `VERSION` at the top of `server/sync-daemon-server.sh`.
It can also be updated from the client (*Check for server update*) after
confirmation. The synchronized data root is `/volume1/NASBox` and is not a
source checkout.

### Command line

```
nasbox-go gui                      GUI (single instance)
nasbox-go sync                     one sync pass
nasbox-go versions <file>          file history (used by the file manager)
nasbox-go status | show            status / bring the window to front
nasbox-go doctor [--json]          diagnostics
nasbox-go integrate [--remove]     file-manager menu entries
nasbox-go update-check | update-install
nasbox-go server-update-check | server-update-install
nasbox-go preview | audit | undo | remote-history | remote-restore | prune-trash
```

### Releases (Go version on **every** release)

A release has two mandatory parts: the GitHub release artifacts and the
`go-stable` channel on the NAS. Skip neither one: clients only see the channel.

1. Bump `client-go/internal/version/version.go` and add a section at the top of
   `CHANGELOG.md`.
2. Commit, then tag with the Go version: `git tag vX.Y.Z && git push origin master --tags`.
   The `package.yml` workflow checks that the tag matches `packaging/version-go.sh`,
   builds the Go binary and the `.deb`, and attaches both to the GitHub release
   on **every** tag. The legacy Python artifacts are no longer published.
3. Publish the same binary to the channel from the NAS (the NAS has no Go, so
   build/copy the binary first):

   ```bash
   cd /volume1/Varie/sync-daemon
   python3 tools/publish_go_client.py --binary ./nasbox-go-X.Y.Z \
     --dest-dir /volume1/NASBox/.nasbox-client-update-go \
     --notes "<summary>" --keep 2
   ```

4. Verify digest and version, then let the clients update:

   ```bash
   python3 -c "import json,hashlib;m=json.load(open('/volume1/NASBox/.nasbox-client-update-go/manifest-go.json'));p='/volume1/NASBox/.nasbox-client-update-go/'+m['artifact'];print(m['version'],m['sha256']==hashlib.sha256(open(p,'rb').read()).hexdigest())"
   ```

A server-only release bumps `VERSION` in the server script instead; clients are
notified and install it only after confirmation.

### Development and tests

```bash
cd client-go
go vet ./... && go test ./...        # includes end-to-end tests with real ssh/rsync/server
NASBOX_UI_CAPTURE=/tmp/shots go test ./internal/ui -run TestCaptureSoftware
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh
```

The end-to-end tests exercise a temporary NAS share with simulated PCs. See
`client-go/README.md` for protocol constraints and
[`tests/LOAD_TESTS.md`](tests/LOAD_TESTS.md) for the load harness.

### Security

Never commit `server/server.conf`, files under `server/state/`, SSH keys, logs
or personal client configuration (already excluded by `.gitignore`). Read
[SECURITY.md](SECURITY.md) before reporting a vulnerability.

### Support the Project

NASBox is free and open-source software built in spare time. If it is useful to
you, consider supporting its development with a coffee.

<div align="center">

[![Donate with PayPal](https://img.shields.io/badge/Donate-PayPal-00457C?style=for-the-badge&logo=paypal)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=azanzani@gmail.com&item_name=Support+NASBox+Project) [![Ko-fi](https://img.shields.io/badge/Ko--fi-Support%20me-FF5E5B?style=for-the-badge&logo=ko-fi&logoColor=white)](https://ko-fi.com/buzzqw)

</div>

### License

NASBox is licensed under the European Union Public Licence, version 1.2
(EUPL-1.2). The [license](LICENSE) is a free and open-source copyleft licence
that permits use, study, modification and distribution under its terms.
