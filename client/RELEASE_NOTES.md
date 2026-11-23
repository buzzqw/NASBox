# NASBox 1.29.0

Certificato di coerenza: una verifica completa, rara e verificabile.

- Audit completo automatico ogni 7 giorni (configurabile), eseguito in
  background a bassa priorità, senza prendere il lock di sincronizzazione.
- Confronto a blocchi da 250 file: niente più rsync singolo sull'intero albero.
- Report persistente con data, durata, file confrontati, differenze e revisione
  del manifest; vengono conservati gli ultimi 5 audit.
- Nella scheda Stato: esito dell'ultimo audit e pulsante "Audit completo ora"
  con avanzamento.
- Se l'audit trova differenze, non modifica nulla: segnala solo i percorsi.
