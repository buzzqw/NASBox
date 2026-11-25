# NASBox — note operative (repo, release, client)

Scritte il 2026-11-15 da un agente, dopo aver verificato ogni comando su questa macchina.
Se qualcosa non torna, valgono i comandi in fondo.

## Stato al 2026-11-25 (verificato su dati veri)

- Client Go **1.13.0**, server **3.32.4**; 122 e 134 aggiornati, GUI riavviata.
- Manifest e disco allineati con `--audit-orphans` (3.738 file adottati) e
  `--audit-phantoms --forget` (2.554 righe senza file messe a tombstone): ora
  31.051 righe vive contro 31.050 file, 0 orphan, 0 phantom. Il render non
  esporta mai percorsi interni (`.sync-trash` anche annidata).
- rsync privato 3.5.1 in `server/bin/` (`tools/install_server_rsync.sh`), usato
  dai client con `--rsync-path`; il rsync di sistema resta 3.1.2. `doctor`
  stampa `NAS 3.5.1 privato, sistema 3.1.2`.
- Rilasci fatti: `eb8196e` (1.9.0/3.29.0), `61a5582` (1.10.0/3.30.0),
  `a0cde1c` (server 3.30.1). Canale `go-stable` con 1.10.0 e 1.9.0 (retention 2).
- Il NAS dichiara le proprie capacità (`--capabilities`): 4 core, 987 MB,
  dischi rotanti, rsync 3.1.2, ext4, hatching 4 file alla volta. Il client ne
  deriva tempo reale, risparmio energetico e (lato NAS) il parallelismo.
- `doctor` lo mostra: `tempo reale: attivo · change feed NAS (attesa 55 s) +
  eventi locali` e `capacità NAS: 4 core · 987 MB · dischi rotanti · rsync
  3.1.2 · ext4`.
- Misure sul NAS vero: cancellazione di 300 file da **37,97 s a 10,81 s** (un
  solo append al journal e una sola revisione); rinomina di una cartella da
  30 MB in **una** richiesta con il secondo PC che sposta i file in locale
  (inode invariati, **zero** download); nessuna directory vuota lasciata sul
  NAS; delta del manifest da **6,9 s a 2,2 s**; tempo reale 122→NAS→134 in
  **9-12 s** (prima 16-22 s).
- Delta rsync sulle sostituzioni (già attivo): 200 MB con 1 MB cambiato =
  14.480 byte inviati, 5,8 s contro 19,6 s a file intero. Sui file **nuovi**
  non c'è base: nessun delta possibile senza un archivio a blocchi.
- Difetti aperti: la protezione cartella non è sbloccabile con
  `confirm-delete`; delta del manifest non ancora per client (indice
  revisione→offset); differenza di file tracciati tra 122 (119.066) e 134
  (119.745).

## Dove vive cosa

| Cosa | Percorso |
|---|---|
| Repo ufficiale (unica copia di lavoro) | `/volume1/Varie/sync-daemon` — clone di `git@github.com:buzzqw/NASBox.git` |
| Remoto | GitHub `buzzqw/NASBox`, branch `master` |
| Server usato dai client | `/volume1/Varie/sync-daemon/server/sync-daemon-server.sh` (demone: PID in `server/state/daemon.pid`) |
| Release per i client | `/volume1/NASBox/.nasbox-client-update/` → `manifest.json` + `client-<versione>.tar.gz` |
| Client (nessun repo, solo build) | questo PC e 134/hw2304847: `~/.local/share/nasbox/client`, servizio utente `sync-daemon-client` |

I client **non** hanno una copia del repo: non cercare il codice su un client.

## Pubblicare una release (tutto da qui)

```bash
cd /volume1/Varie/sync-daemon

# 1. modifiche + test (pytest NON è installato su questo NAS: i test si lanciano
#    su un client, oppure `pip install pytest` in un venv)

# 2. versione e changelog
#    client/sync_client/version.py  ->  APP_VERSION = "x.y.z"
#    CHANGELOG.md                   ->  voce "## x.y.z - AAAA-MM-GG"

# 3. commit e push
git add -A
git commit -m "fix(client): <cosa> (x.y.z)"
git push origin master

# 4. pubblica per i client (scrive direttamente nella share)
python3 tools/publish_client.py --notes "<riassunto>" \
        --dest-dir /volume1/NASBox/.nasbox-client-update
```

`--upload` esiste, ma richiede la configurazione di un client (SSH verso il NAS):
dal NAS usa `--dest-dir`. Il publisher verifica da solo il bundle estratto
(versione + struttura) prima di pubblicarlo, e sostituisce la release precedente
(tiene solo l'ultima).

## Come arrivano gli aggiornamenti ai client

- controllo all'avvio e ogni 6 ore, notifica in tray, voce "⬆️ Aggiorna a x.y.z"
- l'archivio è verificato con SHA-256 prima dell'estrazione; percorsi assoluti,
  `..`, symlink e device file vengono rifiutati
- se la nuova versione non completa l'avvio due volte, il client ripristina da solo
  la versione precedente (log: `~/.local/state/sync-daemon/client.log`)

## Verifiche rapide

```bash
# coerenza manifest/archivio (deve stampare True)
python3 -c "import json,hashlib;m=json.load(open('/volume1/NASBox/.nasbox-client-update/manifest.json'));print(m['sha256']==hashlib.sha256(open('/volume1/NASBox/.nasbox-client-update/'+m['artifact'],'rb').read()).hexdigest())"

# stato del server
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --status

# stato di un client (da eseguire sul client)
systemctl --user status sync-daemon-client.service
```

## Trappole già prese (non ripeterle)

- `scp` verso il NAS **non** funziona: il subsystem SFTP è disabilitato
  ("subsystem request failed on channel 0"). Usa `rsync` o `ssh ... 'cat file' > file`.
- Il client non va più lanciato dal repo: gira da `~/.local/share/nasbox/client`
  come servizio utente (`NASBOX_SERVICE=1`, finestra in tray).
- `sync-daemon` è escluso dalla sincronizzazione: non finisce nella share.
- Lo sha256 dell'archivio dipende dal Python che lo costruisce (3.8 qui, 3.14 sui
  client): è riproducibile sulla stessa macchina, non tra macchine diverse.
  Per questo la release si pubblica **solo da qui**, così manifest e archivio
  vengono generati insieme e restano coerenti.

## Client Go (canale separato)

Il NAS non ha Go installato: il binario si compila su un client con Go 1.27.

```bash
# su un client, nel repo (o in una copia sincronizzata del repo)
cd client-go
go vet ./...
go test ./...
go build -trimpath -ldflags='-s -w' -o /tmp/nasbox-go-<versione> ./cmd/nasbox

# porta il binario sul NAS, poi pubblica dal NAS
rsync -a /tmp/nasbox-go-<versione> andres@192.168.1.119:/volume1/Varie/sync-daemon/
ssh andres@192.168.1.119
cd /volume1/Varie/sync-daemon
python3 tools/publish_go_client.py --binary ./nasbox-go-<versione> \
        --dest-dir /volume1/NASBox/.nasbox-client-update-go \
        --notes "<riassunto>"
rm -f ./nasbox-go-<versione>
```

- La versione sta in `client-go/internal/version/version.go`; la matrice di
  parità con il client Python e le lacune residue in `client-go/PARITY.md`.
- `go test ./...` include i **test end-to-end** (`cmd/nasbox/e2e_test.go`):
  binario, rsync e `server/sync-daemon-server.sh` veri su una share
  temporanea, ssh finto. Vanno lanciati prima di ogni rilascio.
- Revisione grafica senza display:
  `NASBOX_UI_CAPTURE=/tmp/shots go test ./internal/ui -run TestCaptureSoftware`.
- Vincoli del NAS (DS420j): rsync **3.1.2** (vietate le opzioni più recenti,
  es. `--mkpath`, c'è un test); rsync via SSH solo dentro le share
  (`/volume1/...`); `staging-publish` crea solo file nuovi; il lease
  (`--transfer-wait`) si rilascia chiudendo stdin.
- Dalla 0.21.0 i client Go si aggiornano da soli (avvio + ogni 6 ore); il
  manifest include la sezione del CHANGELOG della versione.
- Pacchetti: `client-go/install.sh` (installazione utente, `--migrate` dal
  Python) e `packaging/build-deb-go.sh <binario>`.
- Il canale Go è indipendente: `manifest-go.json` e `nasbox-go-<versione>` in
  `.nasbox-client-update-go/`, mai `manifest.json` del Python.
- Verifica: `nasbox-go update-check` sul client deve proporre la nuova versione;
  il client Go in esecuzione va riavviato per applicarla.

