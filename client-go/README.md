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
| 0 | Struttura modulo, i18n embed, classificatore portato | **in corso** |
| 1 | Motore sync, SQLite, manifest/journal, lock, guardia, undo | da fare |
| 2 | UI nativa (tab, tray, notifiche) X11 + Wayland, parità con PyQt6 | da fare |
| 3 | Canale di aggiornamento separato, rollout graduale | da fare |
| 4 | Rimozione del client Python | da fare |

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
