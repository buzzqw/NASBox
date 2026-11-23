# NASBox 1.31.0

L'audit completo ora cerca **problemi di sincronizzazione**, non solo differenze
di contenuto:

- rileva code `pending` con percorsi già cancellati su entrambi i lati (i falsi
  allarmi che abbiamo visto), file del baseline presenti sul NAS ma spariti in
  locale ed errori del journal;
- distingue le voci di sola informazione da quelle che richiedono intervento;
- mostra il **motivo** concreto dello stato "Attenzione" nella Panoramica e
  nella scheda Stato;
- conta e segnala i percorsi del manifest che sono esclusi dalla sync, così le
  differenze fantasma non ricompaiono.
