# NASBox Go — parità con il client Python

Audit eseguito il 2026-11-24 confrontando il client Python 1.35.2
(`client/sync_client`, ~36.000 righe) con il client Go 0.18.0
(`client-go`, ~22.000 righe incluse le nuove funzioni). L'obiettivo è che il Go
copra *almeno* le funzionalità Python prima di sostituirlo nel canale stabile.

## Metodo

1. Confronto modulo per modulo: motore di sync, protocollo SSH/rsync, stato
   SQLite, classificazione differenze, conflitti, cestino, mirror, selettivo,
   audit, aggiornamenti, UI.
2. Confronto delle chiavi `client.json`: il Go usa gli stessi nomi e lo stesso
   file, quindi un client può passare dall'uno all'altro senza migrazioni.
3. Confronto delle stringhe i18n: le chiavi non usate dal Go indicavano le
   schermate mancanti (storage, dupclean, log, preview, setup, tray).
4. Port dei test Python più significativi (storage insights, protocollo
   metriche, schedule, change feed) nella suite Go.

## Parità funzionale al 2026-11-24

| Area | Python 1.35.2 | Go 0.18.0 | Note |
|---|---|---|---|
| Sync bidirezionale, push/pull/archive | sì | sì | stesso `sync_mode`, stessi guard |
| Fail-closed su file illeggibile/instabile | sì | sì | `scanLocal` interrompe la passata |
| Riconciliazione baseline + causal clock | sì | sì | stesso schema SQLite |
| Rinomina locale riconosciuta | sì | sì | `classify` + `RenameV2` |
| Guardia cancellazioni cumulativa e per cartella | sì | sì | sentinelle, ratio, finestra |
| Journal, staging atomico, restore batch | sì | sì | stessi verbi server |
| Change feed NAS | sì | **sì (0.18.0)** | `--wait-for-revision`, delta manifest |
| Manifest delta | sì | sì | fallback full automatico |
| Lock globale NAS + lease | sì | sì | owner file e handshake |
| Conflitti (newest/manual, storm, keep-both) | sì | sì | + dettaglio versioni |
| Storico locale (cestino) | sì | sì | lista, ripristino, prune |
| Storico NAS | sì | parziale | ripristino remoto manuale; manca l'elenco unificato |
| Browse NAS (lista/delete/rename/download) | sì | sì | stessi verbi |
| Mirror locali | sì | sì | autoritativi, cancellabili |
| Selettivo per cartelle | sì | sì | stesso formato |
| Esclusioni per pattern | sì | **sì (0.18.0)** | editor nelle impostazioni |
| Audit integrità schedulato | sì | sì | stesso report JSON |
| Metriche NAS | sì | **sì (0.18.0)** | CPU/I-O/rete/disco/uptime/journal |
| Storage insights (cartelle/duplicati/cestino) | sì | **sì (0.18.0)** | nuovo package `storage` |
| Pulizia guidata duplicati | sì | **sì (0.18.0)** | spostamento nel cestino, file "Mantieni" protetto |
| Log filtrabile e ricerca | sì | **sì (0.18.0)** | categorie Python + ricerca |
| Anteprima modifiche strutturata | sì | **sì (0.18.0)** | dialogo con cartelle/anomalie/esempi |
| Pausa programmata (giorni, notturna) | sì | **sì (0.18.0)** | corretta perdita dei giorni; più finestre |
| Pausa temporizzata (`pause_until`) | sì | **sì (0.18.0)** | 15m/30m/1h/4h/8h/personalizzata |
| Setup al primo avvio | sì | **sì (0.18.0)** | cartella locale, SSH, rilevamento NAS |
| Tray: notifiche e menu completo | sì | **sì (0.18.0)** | conflitti/storico/log/impostazioni/undo |
| i18n it/en | sì | sì | stessa tabella |
| X11 e Wayland | sì (Qt) | sì (Fyne) | selezione backend |

## Funzioni ancora da completare (priorità)

1. **Aggiornamento del server dal client.** Python scopre
   `sync-daemon-server-<versione>.sh` accanto allo script attivo, lo copia,
   riavvia e verifica (`discover_remote_server_update`,
   `update_remote_server_script`). Il Go sa solo verificare la versione minima.
   Oggi sul NAS non ci sono script versionati nuovi, quindi la funzione non è
   operativa; va portata prima di distribuire server con aggiornamento in-place.
2. **Storico NAS unificato.** Il Go espone il ripristino remoto manuale
   (percorso + suffisso) e lo storico locale; manca il pannello che unisce
   locale e remoto per file/cartella con ripristino multiplo.
3. ~~Coda trasferimenti dettagliata~~ — **completata in 0.19.0**: file
   corrente, file N di M, percentuale, velocità, ETA e storico dei file.
4. **Grafici metriche.** Python disegna lo storico CPU/carico; il Go mostra solo
   il campione corrente e i rate calcolati.
5. **Accessibilità e tooltip.** Fyne 2.7 non ha tooltip nativi né
   `SetAccessibleName`: il Go usa la barra suggerimenti e contrasto alto.
6. **Click singolo sulla tray.** (0.19.0: menu tray completo con stato,
   recenti e pausa; resta la distinzione click/destra non esposta da Fyne.) La preferenza `tray_single_click` è letta ma il
   backend tray non espone la distinzione click/destra; il menu resta il
   comportamento effettivo.
7. **Note di rilascio nell'updater.** Il manifest Go non porta un changelog
   strutturato; il client mostra solo le note testuali.

## Scelte intenzionali diverse

- Il Go richiede server ≥ 3.25.0 e journal/change feed disponibili: non
  reimplementa i fallback legacy del Python per server vecchi.
- Il lock e la riconciliazione restano identici; il Go non introduce un nuovo
  demone né un indice sul NAS.
- Il canale di aggiornamento resta separato (`manifest-go.json`), così Python e
  Go possono coesistere e il rollback è indipendente.

## Verifiche

```bash
cd client-go
go vet ./...
go test ./...
```

Copertura aggiunta in 0.18.0: `storage` (scansione, limiti, duplicati,
spostamento nel cestino), `metrics` (protocollo, rate CPU/disco/rete, soglie
salute), `schedule` (giorni, finestre notturne, normalizzazione), `remote`
(change feed), `diagnostics` (filtri log), UI (storage, log, anteprima,
pianificazione).
