# Contribuire a NASBox

## Prima di aprire una modifica

- Il client ufficiale è **NASBox Go** (`client-go/`). Il client Python/PyQt6 è
  legacy e ritirato: non accettare nuove funzionalità lì.
- Non includere configurazioni personali, host reali, chiavi SSH, log o file
  sotto `server/state/`.
- Mantieni il server compatibile con bash disponibile su Synology, QNAP e Linux
  generico, e ricorda che il rsync di sistema del NAS può essere 3.1.2.

## Verifiche richieste

```bash
cd client-go && go vet ./... && go test ./...   # include i test end-to-end
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh
```

Per una release, aggiorna `client-go/internal/version/version.go` e il
`CHANGELOG.md`: il tag GitHub (`vX.Y.Z`) deve corrispondere alla versione Go e
la stessa versione va pubblicata nel canale `go-stable`. La procedura completa
è in [README.md](README.md) e in [AGENT-NOTES.md](AGENT-NOTES.md).

Descrivi nella pull request il problema risolto, l'impatto su client e NAS e
come hai verificato la modifica.
