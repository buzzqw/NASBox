# NASBox 1.28.11

Il client è ora più intelligente nella gestione delle cancellazioni:

- il manifest scaricato dal pull viene condiviso con il planner del push per
  5 minuti: una scansione completa non rimette più in coda migliaia di
  cancellazioni già applicate sul NAS;
- i messaggi `PULL_DEFERRED` identici non intasano più il log: vengono
  registrati quando il blocco cambia o ogni 5 minuti.
