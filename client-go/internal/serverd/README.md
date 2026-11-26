# serverd — demone NAS in Go

Sostituto in-house di `server/sync-daemon-server.sh`. Il client Go resta
invariato: il demone accetta gli stessi verbi SSH e produce gli stessi formati.

## Milestone 0 — shadow mode read-only (completata)

Verbi implementati: `--print-config`, `--capabilities`, `--status`,
`--journal-status`, `--diagnostics`, `--devices`, `--metrics`,
`--manifest-export` (pieno e delta), `--history-list`, `--browse-list`,
`--file-states-current`.

`tools/compare_server_go.py` confronta i due demoni sullo stato reale:
11/11 comandi uguali dopo la normalizzazione di valori dinamici e dell'ordine
delle righe (il renderer bash usa un array associativo `awk`).

## Milestone 1 — datapath a chunk (completata)

Nuovi verbi, non ancora usati dai client:

- `--chunk-stats`
- `--chunk-snapshot-create DEVICE PATH [PATH...]`
- `--chunk-snapshot-list`
- `--chunk-restore ID PATH DEST`

Lo store content-addressed vive in `STATE_DIR/chunks` (override
`CHUNK_STORE_DIR`), con chunk da 1 MiB (`CHUNK_SIZE`). Gli oggetti sono
immutabili e scritti con rename atomico; uno snapshot è un JSON con gli hash
dei chunk che compongono ogni file.

Prova su dati reali: due snapshot degli stessi file (CLAUDE.md + PDF da 31 MB)
hanno prodotto 32 chunk, 31.648.751 byte, senza oggetti nuovi al secondo
snapshot; il ripristino è risultato byte-identico (sha256 uguale).

## Milestone successive

2. verbi mutanti sotto lease (journal, delete, rename, staging) con E2E;
3. retention/loop del demone e disattivazione dello script bash;
4. web UI nello stesso binario.
