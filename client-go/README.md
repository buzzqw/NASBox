# NASBox — il tuo cloud, sul tuo NAS

NASBox sincronizza una cartella dei tuoi PC con il tuo NAS, come fanno i
servizi di cloud storage, ma con i dati che restano a casa tua: SSH e rsync
verso il NAS, nessun servizio esterno, nessun account.

Questo è il **client definitivo** (Go + Fyne, un unico binario per X11 e
Wayland). Il client Python resta disponibile solo per la migrazione.

## Cosa fa

- **Sync bidirezionale continua** con rilevamento immediato delle modifiche
  (inotify), più PC sullo stesso NAS, coordinati da un lease sul NAS.
- **Mai perdere un file**: ogni versione sostituita o cancellata finisce nello
  storico (sul NAS in `.sync-trash`, sul PC nel cestino locale); "Versioni del
  file" le elenca tutte e le ripristina con un clic, anche dal menu del file
  manager.
- **Conflitti gestiti**: vince la versione più recente (o scegli tu); la
  versione perdente resta recuperabile.
- **Protezioni**: blocco delle cancellazioni di massa, sentinelle, guardia per
  cartelle che spariscono, fail-closed su file illeggibili.
- **Interfaccia** con stato chiaro, trasferimenti in tempo reale (file,
  velocità, tempo stimato), attività recente, tray con pause temporizzate,
  monitor del NAS con grafici, "Controlla il sistema".
- **Sync selettiva** sull'albero delle cartelle del NAS, con dimensioni.
- **Integrazione desktop**: voce "NASBox: versioni precedenti" nel clic destro
  di Thunar, Nautilus, Nemo, Dolphin e Caja; emblema di stato sulla cartella.
- **Aggiornamenti automatici** del client (con rollback) e aggiornamento del
  server NAS dal client, dopo conferma.

## Requisiti

- PC Linux (X11 o Wayland) con `ssh`, `rsync` ≥ 3.1 (consigliato
  `inotify-tools`).
- NAS raggiungibile via SSH con chiave, `bash`, `flock`, `sha256sum`, `rsync`
  (anche 3.1.2 come sui Synology) e lo script `server/sync-daemon-server.sh`
  ≥ 3.25.0.

## Installazione

```bash
./install.sh nasbox-go             # per l'utente: ~/.local/bin, menu, avvio automatico
./install.sh --migrate nasbox-go   # come sopra e disattiva il client Python
./install.sh --uninstall
```

Pacchetto Debian/Ubuntu: `packaging/build-deb-go.sh <binario>` produce
`dist/nasbox-go_<versione>_amd64.deb` (in quel caso gli aggiornamenti passano
dal gestore pacchetti).

### Migrare dal client Python

Configurazione (`~/.config/sync-daemon/client.json`), stato
(`~/.local/state/sync-daemon/`), storico e cestino sono **gli stessi**: non
serve migrare dati. Basta fermare il client Python e avviare il Go
(`./install.sh --migrate`). I due client non possono girare insieme (lock di
istanza comune). Per tornare indietro: riattivare `sync-daemon-client.service`.

## Riga di comando

```
nasbox-go gui                      interfaccia (una sola istanza: riaprire porta in primo piano)
nasbox-go sync                     una passata di sync (elenca i file trasferiti)
nasbox-go versions <file>          versioni del file nella GUI (usato dal file manager)
nasbox-go status | show            stato / mostra la finestra
nasbox-go doctor [--json]          diagnosi (server, lock, rsync locale e del NAS, errori)
nasbox-go integrate [--remove]     menu del file manager
nasbox-go update-check | update-install
nasbox-go server-update-check | server-update-install
nasbox-go preview | audit | undo | remote-history | remote-restore | prune-trash
```

## Sviluppo

```bash
go vet ./... && go test ./...      # include i test end-to-end
NASBOX_UI_CAPTURE=/tmp/shots go test ./internal/ui -run TestCaptureSoftware
```

I **test end-to-end** (`cmd/nasbox/e2e_test.go`) eseguono il binario vero,
rsync e lo script server vero su una share temporanea con più PC simulati
(ssh finto locale): creazione, modifica con storico, cancellazioni propagate,
conflitti, cancellazioni di massa, modifiche fatte direttamente sul NAS,
aggiornamento del server, ripristino dallo storico e sync selettiva.

Vincoli da rispettare (verificati sul NAS reale): il rsync del NAS è 3.1.2,
quindi nessuna opzione introdotta dopo (es. `--mkpath`); `staging-publish`
crea solo file nuovi; il lease si rilascia chiudendo stdin.

Lo stato del confronto con il client Python è in [`PARITY.md`](PARITY.md),
il design dell'interfaccia in `internal/ui/DESIGN.md`.

## Canale di aggiornamento Go

Il client Go **non legge mai** `manifest.json` del Python. Usa
`manifest-go.json` dentro `.nasbox-client-update-go/` (config
`client_update_remote_path_go`). Pubblicazione:

```bash
cd /volume1/Varie/sync-daemon
python3 tools/publish_go_client.py \
    --binary /tmp/nasbox-go-0.1.0 \
    --dest-dir /volume1/NASBox/.nasbox-client-update-go \
    --notes "prima build Go"
```

Verifica dal client: `nasbox-go update-check`. La verifica sha256 e della
dimensione avviene prima di rendere utilizzabile l'artifact.

**Ad ogni nuova versione**: aggiornare `internal/version/version.go` e il
CHANGELOG, taggare `vX.Y.Z` (il workflow `.github/workflows/package.yml`
allega binario e `.deb` alla release GitHub) **e** pubblicare lo stesso binario
nel canale `go-stable` con `publish_go_client.py --keep 2`. La release GitHub
da sola non aggiorna i client installati: serve il canale. Procedura completa
nel [README principale](../README.md).

## Display: X11 e Wayland

`go_display_backend` (`auto`, `x11`, `wayland`) seleziona `FYNE_BACKEND` prima
di creare la finestra. Con `auto` viene scelto `wayland` se è impostato
`WAYLAND_DISPLAY`, altrimenti `x11`. Entrambi i backend sono supportati dal
toolkit Fyne; il client non usa Qt o WebView.

## Struttura

- `internal/i18n` — tabella traduzioni identica al client Python (embed).
- `internal/classify` — classificazione differenze (rename vs delete, anomalie
  per cartella), portata dal Python 1.33.0.
- `internal/version` — versione indipendente del client Go.

## Test

```bash
export PATH=/tmp/opencode/toolchain/go/bin:$PATH
cd client-go && go test ./...
```
