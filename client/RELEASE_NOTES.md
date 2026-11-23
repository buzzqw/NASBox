# NASBox 1.28.1

Correzione avvio lento dopo l'aggiornamento 1.28.0.

- Il controllo di integrità automatico **non parte più all'avvio**: la prima
  verifica è rinviata di 30 minuti e comunque solo a client a riposo.
- "Verifica adesso" resta immediata, ma ora controlla un **campione di 500 file**
  tratti dal manifest invece dell'intero albero: molto più veloce e con un
  consumo di memoria ridotto. Per l'audit completo imposta
  `integrity_check_sample_size = 0`.
- La card di garanzia indica quando il dato è un campione.
- Statistics della card calcolate una volta ogni 30 secondi invece di ogni tick.
