# NASBox 1.28.12

Controlli e confronti più veloci:

- il conteggio delle cancellazioni proposte usa il manifest già in cache
  invece di lanciare un rsync `--dry-run` su tutto l'albero (si ricade su rsync
  solo se la cache è fredda);
- il confronto della scansione locale è a passata singola con `os.scandir`,
  senza costruire un dizionario di stat per tutti i file.
