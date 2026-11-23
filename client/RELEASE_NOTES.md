# NASBox 1.30.1

Corretto il falso allarme "Attenzione": audit e verifica checksum ora ignorano
le cartelle escluse dalla sincronizzazione (es. `sync-daemon/`), che generavano
centinaia di differenze inesistenti.

- Gli audit passati con sole differenze escluse vengono automaticamente
  considerati puliti.
- La Panoramica mostra ora una riga "Motivo:" con la causa concreta dello stato
  di attenzione.
