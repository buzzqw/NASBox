# NASBox 1.28.10

Completa la correzione del loop di "PULL_DEFERRED": oltre a rimuovere dalla
coda le cancellazioni già applicate, ora viene aggiornato anche il baseline
locale (tombstone), così la scansione successiva non le rileva più come
modifiche e non le rimette in coda.
