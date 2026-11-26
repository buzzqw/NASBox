# NASBox — note operative (repo, release, client)

Aggiornate il 2026-11-26. Il client ufficiale è **NASBox Go**; il client Python
è ritirato e il suo canale non va più pubblicato.

## Stato al 2026-11-26 (verificato su dati veri)

- Client Go **1.15.3**, server Go **3.33.0**; 122 e 134 aggiornati e GUI
  riavviata. Entrambi usano il backend `chunks`.
- Canale `go-stable`: 1.15.3 + 1.15.2 (`--keep 2`). Ogni release Go viene
  pubblicata due volte: artifact sulla release GitHub e binario nel canale.
- Manifest e disco allineati con `--audit-orphans` (3.738 file adottati) e
  `--audit-phantoms --forget` (2.554 righe senza file messe a tombstone):
  31.051 righe vive contro 31.050 file, 0 orphan, 0 phantom. Il render non
  esporta mai percorsi interni (`.sync-trash` anche annidata).
- `metadata.sqlite3` (29 MB sul NAS) è l'indice derivato di manifest+journal:
  export delta reale, lookup manifest/file-states e delta di una revisione in
  **10 righe / 925 byte**. L'export completo resta identico al renderer
  precedente (stesso hash ordinato, 87.893 righe).
- `tx-intent.sqlite3` registra le pubblicazioni chunk `PREPARED/READY/COMMITTED`;
  la recovery gira sotto lease in `--transfer-wait`. Commit e journal non vengono
  più troncati dopo un `fsync` riuscito.
- Backend chunk v3: upload streaming a memoria limitata, `--chunk-has` per la
  deduplica di rete, compressione zlib solo quando conviene, file vuoti, digest
  intero, limiti `bandwidth_*`, ripresa download da checkpoint e cancellazione
  via context. Verificato: copia di un file da 8,5 MB senza nuovi oggetti;
  download interrotto a 1 MB ripreso e completato con hash corretto.
- `--restore-batch` è implementato nel demone Go; percorsi e symlink antenati
  sono validati anche nella web UI.
- rsync privato 3.5.1 in `server/bin/` (`tools/install_server_rsync.sh`), usato
  solo dal backend legacy; il rsync di sistema resta 3.1.2.
- Il NAS dichiara le proprie capacità (`--capabilities`): 4 core, 987 MB,
  dischi rotanti, rsync 3.1.2, ext4, hashing 4 file alla volta, chunk v3.
- Difetti aperti: la protezione cartella non è sbloccabile con
  `confirm-delete`; recovery persistente solo per publish (delete/rename/restore
  hanno rollback sincrono ma non ancora intent durevoli); differenza di file
  tracciati tra i due PC; manca GC dei chunk.

## Dove vive cosa

| Cosa | Percorso |
|---|---|
| Repo ufficiale (unica copia di lavoro) | `/volume1/Varie/sync-daemon` — clone di `git@github.com:buzzqw/NASBox.git` |
| Remoto | GitHub `buzzqw/NASBox`, branch `master` |
| Server usato dai client | `/volume1/Varie/nasbox-server/nasbox-server` (Go 3.33.0) |
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

Una release solo server aggiorna `version` in
`client-go/internal/serverd/serverd.go`, compila
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/nasbox-server`, sostituisce
atomicamente `/volume1/Varie/nasbox-server/nasbox-server` (backup `.prev`) e
riavvia `--run-foreground`.

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
