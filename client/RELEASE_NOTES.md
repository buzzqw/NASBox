# NASBox 1.28.2

Correzione urgente: il metodo `entry_count()` era stato definito nella classe
sbagliata, provocando un errore al secondo inello scheda Stato.

- `entry_count()` ora è correttamente un metodo di `SyncStateStore`.
- L'updater rimuove le directory `__pycache__` dopo l'aggiornamento: i file
  del bundle hanno mtime normalizzato e i vecchi `.pyc` potevano essere
  riutilizzati al posto del nuovo codice.
