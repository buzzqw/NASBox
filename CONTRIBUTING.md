# Contribuire a NASBox

## Prima di aprire una modifica

- Non includere configurazioni personali, host reali, chiavi SSH, log o file
  sotto `server/state/`.
- Mantieni il client compatibile con Python 3.11 o superiore.
- Mantieni il server compatibile con bash disponibile su Synology, QNAP e Linux
  generico.
- Per ogni modifica al client o al server, incluse quelle generate dall'AI,
  incrementa sempre la versione del componente modificato prima di distribuirla
  e copia sempre i file aggiornati anche nei bundle usati per l'aggiornamento
  automatico (`client-update/` e `client/.update/`). Gli script server
  versionati vanno invece messi direttamente nella cartella `server/`, accanto
  allo script attivo. Aggiorna il bundle client se cambia il client, gli script
  versionati in `server/` se cambiano gli script server, entrambi se la
  modifica coinvolge entrambi.

## Verifiche richieste

```bash
PYTHONPATH=client python3 -m unittest discover -s client/tests -v
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh client/install.sh
```

Descrivi nella pull request il problema risolto, l'impatto su client e NAS e
come hai verificato la modifica.
