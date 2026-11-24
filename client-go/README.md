# NASBox Go — migrazione parallela

Questo client Go **non sostituisce ancora** quello Python: è una migrazione
parallela. Il client Python resta quello di produzione e pubblicato nel canale
`stable` finché il Go non raggiunge parità funzionale.

## Principi

1. **NAS stupido, PC intelligente**: il NAS ARM da 1 GB esegue solo
   `sync-daemon-server.sh` (bash, `flock`, TSV, `mv`). Nessun database, nessun
   demone nuovo, nessun indice in memoria.
2. **Parità del formato dati**: config JSON, SQLite locale, manifest/journal,
   cestino e protocollo NUL del server restano gli stessi del client Python.
3. **Nessuna sorpresa all'utente**: Python e Go possono coesistere; il
   passaggio avverrà con un nuovo canale di distribuzione e rollback.

## Requisito GUI: X11 e Wayland

La UI nativa verrà costruita con un toolkit che supporta **entrambi** i backend
di display su Linux (Fyne/Gio con backend X11 e Wayland selezionabile via
`FYNE_BACKEND`/`GIO_BACKEND`, con rilevamento automatico). Non useremo Qt via
binding né WebView, per evitare dipendenze pesanti e garantire un singolo
binario.

| Fase | Contenuto | Stato |
|---|---|---|
| 0 | Struttura modulo, i18n embed, classificatore portato | completata |
| 1 | Config compatibile, SQLite condiviso, parser manifest, anteprima CLI, guardia, undo, ssh/rsync, lock | completata |
| 2 | UI nativa Fyne (10 tab: Stato, Trasferimenti, Log, Conflitti, Storico, Sfoglia NAS, Cartelle esterne, Selettivo, Monitor NAS, Impostazioni) con backend X11 + Wayland, tray e motore continuo | completata (Tier A) |
| 3 | Canale di aggiornamento separato `manifest-go.json`, download/verifica/install con backup | completata |
| 4 | Tier B: Browse NAS (list/delete/rename), mirror, selective sync, metriche/dispositivi, bandwidth/schedule, lock istanza, setup repository, cambio lingua | completata |
| 5 | Ridisegno UX/UI: 5 aree, design system semantic light/dark, banner guidato con wash semantici chiari (non pieni), card e layout responsive; palette meno blu e pulsanti sempre azionabili con feedback | completata (0.5.2) |
| 6 | Funzioni complete: journal upload, rename atomico, guardia cumulativa + conferma, esclusioni/selective, browse download, keep-both, storico remoto, audit, controllo server, prune, cancel | completata (0.6.0) |
| 7 | UI ricostruita su funzioni complete: navigazione nativa con selezione visibile, contrasto, tutte le azioni collegate (conferma, cancel, download, keep-both, storico remoto, audit, server-check, update) | completata (0.7.0) |
| 7b | Fix UX: tooltip nel footer, tre liste attività distinte, chiave repository locale corretta (file tracciati), form rename grande, dispositivi con nome/IP, aggiunta mirror guidata, bordi visibili | completata (0.8.0) |
| 7c | Icona tray storica + animazione, zero sfondi grigi, evidenziazione navigazione con marcatore, browse a doppio clic sulle cartelle, storico filtrabile, hint non troncato | completata (0.9.0) |
| 8 | Rollout graduale e rimozione del client Python | da fare |

Il documento di design dell'interfaccia è in `internal/ui/DESIGN.md`.

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

Verifica dal client: `nasbox update-check`. La verifica sha256 e della
dimensione avviene prima di rendere utilizzabile l'artifact.

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
