# NASBox — your cloud, on your NAS

NASBox keeps a folder in sync across your computers through **your own NAS**.
Your files stay on your hardware: no account, no subscription, no third-party
cloud. Think of it as a self-hosted Dropbox.

- **Sync that just works** — change a file on one PC, it shows up on the others
  within seconds. Works over your home network or over the Internet through SSH.
- **Nothing is lost by accident** — every replaced or deleted file is kept in a
  history you can browse and restore, on the PC and on the NAS.
- **Simple window, powerful when needed** — a clean *Status / Files / Settings* view
  by default; queue, logs and NAS monitor are one click away under *Advanced tools*.
- **Browser access** — browse, search, preview, download, upload, rename, delete
  and create folders from any browser, and share a file with a link that expires.
- **Safe by default** — guards against mass deletions, conflict handling that never
  overwrites silently, and fail-closed behaviour when something looks wrong.

> User manual: [MANUAL.md](MANUAL.md). Release notes: [CHANGELOG.md](CHANGELOG.md).

## What you get

| | |
|---|---|
| **Desktop app** | Linux (X11 and Wayland), one binary. System tray with recent files, pause and speed limit. File-manager entry "NASBox: previous versions". Windows build: experimental. |
| **NAS daemon** | One small binary (ARM64 and x86-64) or the shell script. Keeps history, serves the web interface, runs nightly maintenance and sends optional notifications. |
| **Web interface** | Served by the NAS daemon: browse, search, preview, upload, download, previous versions, expiring share links. |

## Quick start

### 1. On the NAS

Download `nasbox-server-linux-arm64` (most Synology/QNAP ARM boxes) or
`nasbox-server-linux-amd64` from the [latest release](../../releases/latest), then:

```bash
chmod +x nasbox-server-linux-*
cp server.conf.example server.conf          # set SHARE_ROOT=/volume1/NASBox
./nasbox-server-linux-arm64 -c server.conf --init-repository
./nasbox-server-linux-arm64 -c server.conf --start
```

Start it at boot with your NAS's task scheduler (on Synology: *Control Panel →
Task Scheduler → triggered task → boot-up*, running the `--start` command).
The shell-script installer (`server/install.sh`) remains available for NAS models
without a suitable binary.

### 2. On each PC

Download the `.deb` or the `nasbox-go-<version>` binary from the same release:

```bash
sudo apt install ./nasbox-go_<version>_amd64.deb      # Debian / Ubuntu
# or, for any distribution (no root needed):
client-go/install.sh nasbox-go
```

Start **NASBox**. The first-run wizard asks for the NAS address and user, sets up
the SSH key for you (the password is needed only once) and chooses the local folder.
That is all: drop files in the folder and they sync.

### 3. From a browser (optional)

Set `WEB_LISTEN`, `WEB_USER` and `WEB_PASSWORD` in the NAS `server.conf` and open
`http://<nas>:8080`. **Use it on your home network or behind a VPN / HTTPS reverse
proxy**; do not expose the port to the Internet without TLS.

## Requirements

- PC: Linux with `ssh`; change detection uses the built-in filesystem watcher.
- NAS: reachable over SSH with a key; Linux ARM64 or x86-64.
- Optional: `rsync` ≥ 3.1 is used only by the legacy transfer backend; the default `chunks` backend does not need it.

## Everyday use

- **Status at a glance**: the Status screen and the tray icon tell you whether
  everything is in sync, what changed recently and what needs your attention.
- **Pause** from the tray for an hour or until tomorrow; **limit the speed** with
  one click.
- **Restore** an older version or an accidentally deleted file from *Files →
  History*, or right-click a file in your file manager.
- **Conflicts** are resolved automatically in favour of the newest version by
  default; the other copy is always kept and can be restored.

## Updates

The desktop app checks for updates at startup and every few hours, verifies them
and rolls back automatically if a new version fails to start. The NAS daemon can
be updated from the app after your confirmation.

## Command line

```
nasbox-go gui                      open the app (single instance)
nasbox-go sync                     run one sync pass
nasbox-go doctor [--json]          check the setup (server, locks, errors)
nasbox-go versions <file>          previous versions of a file
nasbox-go status | show            show the status / bring the window forward
nasbox-go update-check | update-install
```

Run `nasbox-go` without arguments for the complete list.

## Security

- Traffic between the PC and the NAS goes through SSH; there is no NASBox cloud.
- The web interface is read-only until you set a password. Writes require the
  password, and failed logins are rate-limited. Share links are random, expire,
  can be revoked, and always download as attachments.
- Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Building from source

```bash
cd client-go
go vet ./... && go test ./...
go build -trimpath -ldflags='-s -w' -o nasbox-go ./cmd/nasbox
```

Go 1.27+ and a CGO toolchain are needed for the desktop app; the NAS daemon
(`./cmd/nasbox-server`) builds with `CGO_ENABLED=0`. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Roadmap

Planned next: a smoother Windows client, a deduplicated history to save NAS space,
folder management and renaming from the web interface, and a simpler conflict
screen. Suggestions are welcome as issues.

## Support the project

NASBox is free software. If it is useful to you, you can support its development:

[![Donate with PayPal](https://img.shields.io/badge/Donate-PayPal-00457C?style=for-the-badge&logo=paypal)](https://www.paypal.com/donate/?business=azanzani%40gmail.com&item_name=NASBox)

## License

See [LICENSE](LICENSE).
