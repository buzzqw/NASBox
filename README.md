# NASBox — il tuo cloud, sul tuo NAS

NASBox sincronizza una cartella tra più PC tramite un NAS, usando SSH e rsync.
I dati restano sul tuo storage: nessun account, nessun servizio cloud di terze
parti.

> **Il programma ufficiale è NASBox Go** (`client-go/`, Go + Fyne, un solo
> binario per X11 e Wayland). Il vecchio client Python/PyQt6 è **legacy,
> ritirato**: non riceve più aggiornamenti e il suo canale non viene più
> pubblicato. Serve solo per la migrazione dei dati, che restano gli stessi.

| Componente | Versione corrente |
|---|---|
| Client Go (ufficiale) | **1.14.1** |
| Server NAS (`server/sync-daemon-server.sh`) | **3.32.4** |

---

## English

### Official client

NASBox Go is the only supported client. It is a single Fyne binary
(`nasbox-go`) for Linux/X11 and Linux/Wayland. The legacy PyQt6 client is
retired: its update channel is no longer published and no new work targets it.
Configuration, local state, history and trash are shared with the old client,
so migrating does not move any data.

The NAS runs a small, shell-only daemon. The clients talk to it over SSH and
perform the transfers with rsync; the daemon owns the journal, the manifest,
the remote history and the global transfer lease. Nothing needs Python on the
NAS.

### Features

- Continuous bidirectional sync with immediate change detection (inotify) and
  a NAS change feed; several PCs share one NASBox root and are serialized by a
  lease on the NAS.
- Nothing is silently lost: replaced and deleted files go to NAS history
  (`.sync-trash`) and local trash. "Versioni del file" lists and restores them,
  also from the file manager context menu.
- Conflicts: newest wins by default, or resolve them manually; the losing copy
  stays recoverable. A Conflict Center shows metadata, a text diff and a
  recommended version.
- Deletion guards (mass deletion, disappearing folders, sentinels, fail-closed
  on unreadable files) and confirmation of held deletions.
- GUI with status home, live transfers (file, speed, ETA), recent activity,
  NAS monitor with charts, "Controlla il sistema", timed tray pauses and
  selective sync of NAS folders.
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

- Linux PC (X11 or Wayland) with `ssh`, `rsync` ≥ 3.1 and, preferably,
  `inotify-tools`.
- NAS reachable over SSH with a key, with `bash`, `flock`, `sha256sum`,
  `rsync` (even 3.1.2, as on Synology DSM) and standard shell utilities.
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
It can also be updated from the client (*Cerca aggiornamento server*) after
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
     --notes "<riassunto>" --keep 2
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

---

## Italiano

### Il client ufficiale

L'unico client supportato è **NASBox Go**: un solo binario Fyne per Linux/X11 e
Wayland. Il vecchio client Python/PyQt6 è ritirato: il suo canale di
aggiornamento non viene più pubblicato e non riceve più modifiche.
Configurazione, stato locale, storico e cestino sono gli stessi, quindi migrare
non sposta alcun dato.

Sul NAS gira un piccolo demone solo shell. I client parlano via SSH ed eseguono
i trasferimenti con rsync; il demone gestisce journal, manifest, storico remoto
e il lease globale di trasferimento. Sul NAS non serve Python.

### Funzionalità

- Sync bidirezionale continua con rilevamento immediato (inotify) e change feed
  del NAS; più PC condividono una radice NASBox, coordinati da un lease sul NAS.
- Nessun file perso in silenzio: versioni sostituite e cancellazioni finiscono
  nello storico sul NAS (`.sync-trash`) e nel cestino locale. *Versioni del
  file* le elenca e ripristina, anche dal menu del file manager.
- Conflitti: vince la versione più recente, oppure risolvi manualmente; la
  versione perdente resta recuperabile. Il Centro conflitti mostra metadati,
  diff testuale e versione consigliata.
- Protezioni: cancellazioni di massa, cartelle che spariscono, sentinelle,
  fail-closed su file illeggibili e conferma delle cancellazioni trattenute.
- Interfaccia con stato iniziale, trasferimenti in tempo reale (file, velocità,
  tempo stimato), attività recente, monitor NAS con grafici, *Controlla il
  sistema*, pause temporizzate nella tray e sync selettiva delle cartelle NAS.
- Integrazione desktop: voce *NASBox: versioni precedenti* in Thunar, Nautilus,
  Nemo, Dolphin e Caja, più emblema di stato sulla cartella NASBox.
- Aggiornamenti automatici del client con verifica e rollback; aggiornamento
  del server dalla GUI dopo conferma.
- `nasbox-go doctor` mostra server, rsync (locale e NAS), lock, capacità e gli
  errori delle ultime 24 ore.
- Le capacità del NAS rilevate all'avvio (CPU, memoria, dischi, rsync,
  filesystem, change feed) regolano tempo reale, parallelismo dell'hash e
  risparmio energetico. Sul NAS si può installare un rsync privato 3.5.1;
  quello di sistema 3.1.2 resta il ripiego.

### Requisiti

- PC Linux (X11 o Wayland) con `ssh`, `rsync` ≥ 3.1 e, consigliato,
  `inotify-tools`.
- NAS raggiungibile via SSH con chiave, con `bash`, `flock`, `sha256sum`,
  `rsync` (anche 3.1.2, come su Synology DSM) e utility shell standard.
- Go 1.27+ con CGO serve solo per compilare il client.

### Installare e aggiornare il client

Scarica `nasbox-go-<versione>` (linux-amd64) o il pacchetto `.deb` dalla release
GitHub della versione scelta, poi:

```bash
# installazione utente: ~/.local/bin, voce nel menu, avvio automatico
client-go/install.sh nasbox-go

# migrazione dal vecchio client Python (stessa config/state, nessuna copia)
client-go/install.sh --migrate nasbox-go

# Debian/Ubuntu (gli aggiornamenti passano dal gestore pacchetti)
sudo apt install ./nasbox-go_<versione>_amd64.deb
```

I due client non devono mai girare insieme: condividono configurazione e lock
di istanza. Configurazione: `~/.config/sync-daemon/client.json`; stato, log e
cestino: `~/.local/state/sync-daemon/`.

Installato in `~/.local/bin`, il client controlla il canale `go-stable` all'avvio
e ogni 6 ore, verifica SHA-256 e dimensione e torna alla versione precedente se
il nuovo binario non parte due volte. Controllo manuale:

```bash
nasbox-go update-check
nasbox-go update-install        # richiede il riavvio della GUI
nasbox-go doctor
```

Il canale è `/volume1/NASBox/.nasbox-client-update-go/` sul NAS
(`manifest-go.json` + `nasbox-go-<versione>`); non usa mai il manifest Python.

### Installare e aggiornare il server NAS

Il checkout canonico è solo sul NAS:

```bash
git clone https://github.com/buzzqw/NASBox.git /volume1/Varie/sync-daemon
cd /volume1/Varie/sync-daemon
sudo ./server/install.sh          # impostare SHARE_ROOT=/volume1/NASBox
./server/sync-daemon-server.sh --restart
./server/sync-daemon-server.sh --print-config
```

La versione del server è `VERSION` in cima a `server/sync-daemon-server.sh`.
Può essere aggiornata anche dal client (*Cerca aggiornamento server*) dopo
conferma. La cartella dati sincronizzata è `/volume1/NASBox` e non è un checkout
del sorgente.

### Riga di comando

```
nasbox-go gui                      interfaccia (una sola istanza)
nasbox-go sync                     una passata di sync
nasbox-go versions <file>          versioni del file (usato dal file manager)
nasbox-go status | show            stato / mostra la finestra
nasbox-go doctor [--json]          diagnosi
nasbox-go integrate [--remove]     voci nel menu del file manager
nasbox-go update-check | update-install
nasbox-go server-update-check | server-update-install
nasbox-go preview | audit | undo | remote-history | remote-restore | prune-trash
```

### Release (versione Go in **ogni** release)

Una release ha due parti obbligatorie: gli artifact della release GitHub e il
canale `go-stable` sul NAS. Saltarne una significa che i client non vedono
l'aggiornamento.

1. Aggiorna `client-go/internal/version/version.go` e aggiungi una sezione in
   cima a `CHANGELOG.md`.
2. Committa, poi taggia con la versione Go:
   `git tag vX.Y.Z && git push origin master --tags`. Il workflow `package.yml`
   verifica che il tag corrisponda a `packaging/version-go.sh`, compila il
   binario Go e il `.deb` e li allega alla release GitHub **ad ogni tag**. Gli
   artifact Python legacy non vengono più pubblicati.
3. Pubblica lo stesso binario nel canale dal NAS (il NAS non ha Go: compila e
   copia prima il binario):

   ```bash
   cd /volume1/Varie/sync-daemon
   python3 tools/publish_go_client.py --binary ./nasbox-go-X.Y.Z \
     --dest-dir /volume1/NASBox/.nasbox-client-update-go \
     --notes "<riassunto>" --keep 2
   ```

4. Verifica digest e versione, poi lascia aggiornare i client:

   ```bash
   python3 -c "import json,hashlib;m=json.load(open('/volume1/NASBox/.nasbox-client-update-go/manifest-go.json'));p='/volume1/NASBox/.nasbox-client-update-go/'+m['artifact'];print(m['version'],m['sha256']==hashlib.sha256(open(p,'rb').read()).hexdigest())"
   ```

Una release solo server aggiorna invece `VERSION` nello script del server; i
client vengono avvisati e installano solo dopo conferma.

### Sviluppo e test

```bash
cd client-go
go vet ./... && go test ./...        # include test end-to-end con ssh/rsync/server veri
NASBOX_UI_CAPTURE=/tmp/shots go test ./internal/ui -run TestCaptureSoftware
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh
```

I test end-to-end usano una share NAS temporanea e PC simulati. Vedi
`client-go/README.md` per i vincoli del protocollo e
[`tests/LOAD_TESTS.md`](tests/LOAD_TESTS.md) per il load test.

### Sicurezza

Non committare mai `server/server.conf`, file sotto `server/state/`, chiavi SSH,
log o configurazioni client personali (già esclusi da `.gitignore`). Leggere
[SECURITY.md](SECURITY.md) prima di segnalare una vulnerabilità.

### Supporta il progetto

NASBox è software libero e open source sviluppato nel tempo libero. Se ti è
utile, puoi sostenere lo sviluppo offrendo un caffè.

<div align="center">

[![Dona con PayPal](https://img.shields.io/badge/Dona-PayPal-00457C?style=for-the-badge&logo=paypal)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=azanzani@gmail.com&item_name=Support+NASBox+Project) [![Ko-fi](https://img.shields.io/badge/Ko--fi-Support%20me-FF5E5B?style=for-the-badge&logo=ko-fi&logoColor=white)](https://ko-fi.com/buzzqw)

</div>

### Licenza

NASBox è distribuito con la European Union Public Licence, versione 1.2
(EUPL-1.2). La [licenza](LICENSE) è una licenza copyleft libera e open source
che consente uso, studio, modifica e distribuzione secondo i suoi termini.
