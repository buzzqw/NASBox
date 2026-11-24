# NASBox Go — ridisegno dell'interfaccia

## Principi applicati

1. **Chiarire prima di abbellire.** La domanda a cui deve rispondere la prima schermata è «è tutto sincronizzato? cosa devo fare?». Il banner di Stato risponde con tre elementi sempre presenti: *stato* (semantico), *motivo* (testo dell'ultimo evento) e *prossima azione* (pulsante che porta alla schermata giusta).
2. **Gerarchia, non accumulo.** Le dieci tab piatte sono state raggruppate in cinque aree di navigazione con sotto-tab secondarie. Ogni funzione resta raggiungibile, ma non compete per l'attenzione.
3. **Progressive disclosure.** Le operazioni rare (audit, anteprima, undo, mirror, selettivo, aggiornamenti) vivono nelle sezioni appropriate; la Stato mostra solo le azioni principali e i KPI.
4. **Feedback immediato e guidato.** Pulsante primario per «Sincronizza», importanza *danger* per le eliminazioni, stati disabilitati finché non si seleziona un elemento, conferme per eliminazione/ripristino/risoluzione conflitti, riga di stato in basso e suggerimenti al passaggio del mouse.
5. **Responsive.** `Border` (header / sidebar / footer / contenuto), `VScroll` per ogni area, griglie adaptive per le azioni e testi lunghi sempre con `TextWrapWord`. La dimensione minima del contenuto è ~641×537 px, quindi la finestra funziona sia a 900×640 sia a 1280×800.
6. **Coerenza i18n.** Tutte le stringhe nuove passano da `i18n.T`; sono state aggiunte solo le chiavi mancanti (prefisso `ui.`) in `internal/i18n/strings.json`, senza toccare le chiavi esistenti. La lingua viene rilevata all'avvio da `language` (`auto` segue `LANG`).

## Mappa delle schermate

| Area | Contenuto |
|---|---|
| **Stato** | Banner semantico + prossima azione; Sincronizza / Pausa-Riprendi / Audit / Anteprima / Annulla; KPI (file tracciati, conflitti, undo, audit); dettagli configurazione e backend |
| **Attività** | Tre elenchi indipendenti: *Recenti* (azioni/decisioni), *Trasferimenti* (progressi ed esiti), *Log* (eventi tecnici completi) |
| **File** | Sotto-tab *Sfoglia NAS* (navigazione, aggiorna, elimina, rinomina), *Conflitti* (elenco + risoluzione), *Storico* (ripristino) |
| **NAS** | Sotto-tab *Monitor* (disco, carico, memoria, lock, coda) e *Dispositivi* |
| **Impostazioni** | Sotto-tab *Generale* (lingua, tema, banda, percorsi), *Cartelle e mirror* (mirror + selettivo), *Informazioni* (versione, update-check) |

L'header mostra sempre il titolo dell'area, un *pill* di stato colorato e le due azioni frequenti. Il banner contiene stato, motivo e prossima azione in testo; l'azione di navigazione è un pulsante stabile nella griglia («Vai ai dettagli») che porta alla sezione corretta, evitando etichette che cambiano e rischiano di essere tagliate. Il footer mostra l'ultimo evento e il suggerimento contestuale al passaggio del mouse (i tooltip nativi non esistono in Fyne 2.7).

## Colore e tipografia

Tema custom (`theme.go`) che eredita font, icone e metriche da Fyne e sostituisce solo la palette:

- **Primario / informazione:** blu `#2563EB` (light) / `#60A5FA` (dark).
- **OK:** verde `#059669` / `#34D399`.
- **Attenzione:** ambra `#D97706` / `#FBBF24`.
- **Blocco / offline:** rosso `#DC2626` / `#F87171`.
- **Superfici:** slate chiaro (`#F6F8FB`, card bianche) e slate scuro (`#0F172A`). Separatori sottili `#E2E8F0` / `#334155`.

Gli stessi colori semantici sono usati da banner, pill, bordi delle card e pulsanti, così lo stato si legge senza dover leggere il testo. La gerarchia tipografica usa label in **grassetto** per titoli e valori, *corsivo* per testi secondari, e `canvas.Rectangle` con angoli arrotondati (12 px) per le superfici. Il tema chiaro/scuro si cambia dalla sidebar o dalle impostazioni e viene applicato immediatamente ricostruendo la vista.

## Cosa è migliorato rispetto al vecchio UI

- Da 10 tab affollate a 5 aree navigabili con sotto-tab; la Stato è ora una vera dashboard.
- Stato con semantica esplicita (avvio / ok / in corso / attenzione / blocco / offline / pausa) e navigazione a un clic verso il dettaglio.
- Card, margini, separatori, spaziature regolari e colori semantici invece di VBox di label e pulsanti grezzi.
- Azioni primarie/secondarie/pericolo, disabilitazione fino a selezione, conferme per operazioni distruttive, feedback testuale immediato.
- Layout a `Border` + scroll verticale: nessun overflow a 900×640; testi lunghi non allargano la finestra.
- Tutte le nuove stringhe tradotte; nessun testo hardcoded nelle nuove sezioni.

## Compromessi e note

- **X11/Wayland:** `selectBackend` è stato lasciato invariato; la selezione avviene prima di `app.NewWithID` e Fyne 2.7 supporta entrambi i backend.
- **Tooltip:** Fyne 2.7 non espone un widget tooltip né `SetAccessibleName`. Al loro posto c'è una barra dei suggerimenti in basso aggiornata dall'hover dei pulsanti, e nomi/etichette descrittivi. L'accessibilità è quindi affidata a contrasto, dimensioni e testi espliciti.
- **Coda trasferimenti:** il `Runner` Go non espone ancora uno snapshot della coda (solo eventi), quindi la tab Trasferimenti mostra gli ultimi eventi con la nota che si aggiorna durante/dopo la sincronizzazione. La logica di sync non è stata modificata.
- **Tema «auto»:** non esiste un indicatore affidabile di tema di sistema via Fyne 2.7 su Linux; `auto`/valori sconosciuti restano sul tema chiaro, dark esplicito è disponibile.
- **Aggiornamenti:** il pulsante controlla la disponibilità e lo segnala nel log; l'installazione resta il comando `nasbox update-install` (non è stata introdotta una nuova API).
- Il backend dati, il protocollo SSH/rsync, i lock e la riconciliazione non sono stati toccati: l'UI continua a usare solo `Runner.Events()`, `SyncNow()`, `SetPaused()`, `IsPaused()` e i package esistenti.
