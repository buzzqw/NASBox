# NASBox — note operative (repo, release, client)

Aggiornate il 2026-11-26. Il client ufficiale è **NASBox Go**; il client Python
è ritirato e il suo canale non va più pubblicato.

## Stato al 2026-11-26 (verificato su dati veri)

- Client Go **1.14.1**, server **3.32.4**; 122 e 134 aggiornati, GUI riavviata
  (su 134 la GUI non era partita, è stata riavviata con `DISPLAY=:0`).
- Canale `go-stable`: 1.14.1 + 1.14.0 (`--keep 2`). Ogni release Go viene
  pubblicata due volte: artifact sulla release GitHub e binario nel canale.
- Manifest e disco allineati con `--audit-orphans` (3.738 file adottati) e
  `--audit-phantoms --forget` (2.554 righe senza file messe a tombstone):
  31.051 righe vive contro 31.050 file, 0 orphan, 0 phantom. Il render non
  esporta mai percorsi interni (`.sync-trash` anche annidata).
- rsync privato 3.5.1 in `server/bin/` (`tools/install_server_rsync.sh`), usato
  dai client con `--rsync-path`; il rsync di sistema resta 3.1.2. `doctor`
  stampa `NAS 3.5.1 privato, sistema 3.1.2`.
- Il NAS dichiara le proprie capacità (`--capabilities`): 4 core, 987 MB,
  dischi rotanti, rsync 3.1.2, ext4, hashing 4 file alla volta. Il client ne
  deriva tempo reale, risparmio energetico e parallelismo.
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
  revisione→offset); differenza di file tracciati tra i due PC.

## Dove vive cosa

| Cosa | Percorso |
|---|---|
| Repo ufficiale (unica copia di lavoro) | `/volume1/Varie/sync-daemon` — clone di `git@github.com:buzzqw/NASBox.git` |
| Remoto | GitHub `buzzqw/NASBox`, branch `master` |
| Server usato dai client | `/volume1/Varie/sync-daemon/server/sync-daemon-server.sh` |
| Canale client Go | `/volume1/NASBox/.nasbox-client-update-go/` → `manifest-go.json` + `nasbox-go-<versione>` |
| Client (nessun repo, solo binario) | `~/.local/bin/nasbox-go` + `~/.config/autostart/nasbox-go.desktop` |
| Config / stato / log / storico | `~/.config/sync-daemon/client.json`, `~/.local/state/sync-daemon/` |

I client **non** hanno una copia del repo: non cercare il codice su un client.

## Pubblicare una release Go (obbligatoria ad ogni nuova versione)

Il NAS non ha Go: si compila su un client, poi si pubblica dal NAS.

```bash
# 1. su un client con Go 1.27, dal sorgente aggiornato
cd client-go
go vet ./... && go test ./...        # include gli end-to-end
go build -trimpath -ldflags='-s -w' -o /tmp/nasbox-go-X.Y.Z ./cmd/nasbox

# 2. versione + changelog (unica fonte della versione)
#    client-go/internal/version/version.go -> AppVersion = "X.Y.Z"
#    CHANGELOG.md -> voce "## X.Y.Z - AAAA-MM-GG"

# 3. commit, tag_con_la_versione_go, push
git add <file voluti>
git commit -m "feat(client): ... (X.Y.Z)"
git tag vX.Y.Z
git push origin master --tags

# 4. il workflow .github/workflows/package.yml verifica che il tag sia uguale a
#    packaging/version-go.sh, compila il binario Go e il .deb e li allega alla
#    release GitHub. Senza il tag non c'è release GitHub.

# 5. copia il binario sul NAS e pubblica nel canale (scrive nella share)
rsync -a /tmp/nasbox-go-X.Y.Z andres@192.168.1.119:/volume1/Varie/sync-daemon/
ssh andres@192.168.1.119
cd /volume1/Varie/sync-daemon
python3 tools/publish_go_client.py --binary ./nasbox-go-X.Y.Z \
        --dest-dir /volume1/NASBox/.nasbox-client-update-go \
        --notes "<riassunto>" --keep 2
rm -f ./nasbox-go-X.Y.Z
```

Il tag deve essere `vX.Y.Z` con la **versione Go**, non quella Python: il
workflow lo controlla e fallisce se non corrispondono. Senza il passaggio 5 i
client già installati non vedono nulla: la release GitHub da sola non basta.

Una release solo server aggiorna `VERSION` in `server/sync-daemon-server.sh` e
la voce del CHANGELOG, poi si distribuisce lo script sul NAS; i client la
annunciano e la installano solo dopo conferma.

## Come arrivano gli aggiornamenti ai client

- controllo all'avvio e ogni 6 ore dal canale `go-stable`, senza dialoghi per
  quelli automatici; il dialogo manuale è *Controlla aggiornamenti*.
- binario verificato con SHA-256 e dimensione prima dell'installazione;
  `requires_server` del manifest controllato con la versione del NAS.
- se il nuovo binario non completa l'avvio due volte, torna a quello precedente
  (stato in `~/.local/bin/nasbox-go.update-state.json`, log in
  `~/.local/state/sync-daemon/client.log`).
- il client in esecuzione va riavviato per usare il binario appena installato.

## Verifiche rapide

```bash
# coerenza manifest/binario (deve stampare True) e versione del canale
python3 -c "import json,hashlib;m=json.load(open('/volume1/NASBox/.nasbox-client-update-go/manifest-go.json'));p='/volume1/NASBox/.nasbox-client-update-go/'+m['artifact'];print(m['version'],m['sha256']==hashlib.sha256(open(p,'rb').read()).hexdigest())"

# stato del server e dei client
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --status
nasbox-go version
nasbox-go doctor
```

## Trappole già prese (non ripeterle)

- `scp` verso il NAS **non** funziona: il subsystem SFTP è disabilitato. Usa
  `rsync` o `ssh ... 'cat file' > file`.
- Non lanciare il client dal repo: gira da `~/.local/bin/nasbox-go` con autostart.
- Il NAS sincronizza `/volume1/NASBox`: non metterci sorgenti, binari di release
  o file di stato dell'updater.
- Compilare con `-trimpath -ldflags='-s -w'`: il binario non stripped è ~41 MB
  contro ~30 MB.
- La GUI passa il client Go diretto all'updater: `remote.Client` deve
  implementare `ServerVersion()`, altrimenti `requires_server` fallisce e il
  dialogo di conferma sembra non fare nulla.
- Vincoli del NAS (DS420j): rsync **3.1.2** (vietate le opzioni più recenti,
  es. `--mkpath`, c'è un test); rsync via SSH solo dentro le share
  (`/volume1/...`); `staging-publish` crea solo file nuovi; il lease
  (`--transfer-wait`) si rilascia chiudendo stdin.
- Il client Python e il suo canale `manifest.json` sono ritirati:
  `tools/publish_client.py` non va più usato per le release.
