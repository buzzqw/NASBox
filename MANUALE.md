# NASBox — Manuale

Sincronizzazione file multi-PC tramite un NAS che fa da hub: ogni PC ha un
client grafico che tiene sincronizzata un'unica cartella locale — **la
cartella NASBox** — con il NAS; il NAS gira un demone leggero che applica la
retention dello storico. Tutti i PC collegati vedono, con un ritardo di al
più qualche decina di secondi, i file che gli altri ci mettono dentro.

Il modello è deliberatamente quello di Dropbox: **una** cartella per PC,
niente elenchi di condivisioni da aggiungere/modificare/rimuovere a mano.
Meno cose da configurare vuol dire anche meno modi di fare danni: la vecchia
versione permetteva più cartelle condivise indipendenti (anche annidate una
dentro l'altra), e rimuoverne una dalla lista poteva propagare una
cancellazione altrove in modi non ovvi. Con una cartella sola per PC, quel
problema semplicemente non può più presentarsi.

## Indice

1. [Architettura in breve](#1-architettura-in-breve)
2. [Requisiti](#2-requisiti)
3. [Installazione del client (PC)](#3-installazione-del-client-pc)
4. [Installazione del pacchetto server (NAS)](#4-installazione-del-pacchetto-server-nas)
5. [Chiavi SSH (obbligatorio)](#5-chiavi-ssh-obbligatorio)
6. [Connessione al NAS (tab Impostazioni)](#6-connessione-al-nas-tab-impostazioni)
7. [Il caso del bastione (NAS non raggiungibile da fuori)](#7-il-caso-del-bastione-nas-non-raggiungibile-da-fuori)
8. [La cartella NASBox](#8-la-cartella-nasbox)
   - [Cartelle esterne (mirror)](#cartelle-esterne-mirror)
9. [Limite di banda](#9-limite-di-banda)
10. [Frequenza di sincronizzazione](#10-frequenza-di-sincronizzazione)
11. [Pausa e pausa a tempo](#11-pausa-e-pausa-a-tempo)
12. [Coda dei trasferimenti](#12-coda-dei-trasferimenti)
13. [Icona nella tray](#13-icona-nella-tray)
14. [Log](#14-log)
15. [Storico / Cestino](#15-storico--cestino)
16. [File e percorsi di configurazione](#16-file-e-percorsi-di-configurazione)
17. [Comandi del demone server (NAS)](#17-comandi-del-demone-server-nas)
18. [Risoluzione problemi](#18-risoluzione-problemi)

---

## 1. Architettura in breve

```
   PC client A ──┐                                    ┌── PC client B
   (in LAN)      │                                    │   (fuori casa)
                 │                                    │
                 ▼                                    ▼
           ┌───────────┐  SSH+rsync diretto     ┌────────────────┐
           │    NAS    │◄───────────────────────┤  PC bastione   │◄── SSH (ProxyJump)
            │192.0.2.10 │  SSH+rsync (bastione    │ pubblico       │
            │           │  vede il NAS in LAN)    │bastion.example │
           └───────────┘                        └────────────────┘
```

- **Client** (`client/`): app grafica Python + PyQt6. Tiene un'unica cartella
  locale (di default `~/NASBox`) sincronizzata, root contro root, con
  un'unica cartella sul NAS: rileva le modifiche locali quasi in tempo reale
  e le manda al NAS (push), e periodicamente scarica quello che gli altri PC
  hanno mandato nel frattempo (pull).
- **Server** (`server/`): demone bash autonomo installato sul NAS. Non
  partecipa al trasferimento file (quello lo fanno i client via SSH/rsync
  direttamente sul NAS) — si occupa solo di far rispettare la retention dello
  storico, in modo indipendente da quali client sono accesi o spenti. È anche
  l'unica fonte di verità per la cartella NASBox lato NAS e per la retention:
  i client la leggono da lì invece di doverla ridigitare ognuno per conto
  proprio (vedi [§6](#6-connessione-al-nas-tab-impostazioni)).
- **Storico/cestino**: ogni volta che un file viene sovrascritto o cancellato,
  la versione precedente non sparisce: viene rinominata con un timestamp UTC
  ad alta precisione e
  spostata in `.sync-trash/` (vedi [§15](#15-storico--cestino)).

## 2. Requisiti

**Sul PC client:**
- Python 3.11 o successivo
- PyQt6 (`sudo pacman -S python-pyqt6` su Arch/CachyOS, `python3-pyqt6` su
  Debian/Ubuntu, oppure `pip install --user PyQt6`)
- `rsync`, `ssh` (praticamente sempre già presenti)
- `inotify-tools` (**consigliato, non obbligatorio**): fornisce `inotifywait`,
  che permette al client di accorgersi delle modifiche locali all'istante
  invece di controllare la cartella ogni 2 secondi. Senza, il client funziona
  comunque (fallback a controllo periodico) ma con un ritardo di rilevamento
  fino a qualche secondo. `sudo pacman -S inotify-tools` su Arch/CachyOS,
  `apt install inotify-tools` su Debian/Ubuntu. `install.sh` (vedi
  [§3](#3-installazione-del-client-pc)) lo controlla e avvisa se manca.

**Sul NAS (Synology, QNAP o Linux generico):**
- `bash`, `rsync`, `ssh`/`sshd`, coreutils (`find`, `stat`, `date`, `awk`,
  `flock`) —
  già presenti di serie su qualunque NAS che possa fare da target rsync.
- Nessuna dipendenza aggiuntiva: nessun Python, nessun pacchetto esterno.

## 3. Installazione del client (PC)

Il metodo consigliato e scaricare una release Linux. L'AppImage non richiede
installazione; su Debian/Ubuntu e disponibile anche il pacchetto `.deb`:

```bash
chmod +x NASBox-<versione>-x86_64.AppImage
./NASBox-<versione>-x86_64.AppImage
sudo apt install ./nasbox-client_<versione>_amd64.deb  # alternativa
```

I pacchetti includono Python e PyQt6, ma usano `rsync` e OpenSSH del sistema.
Le release pacchettizzate si aggiornano installando una release successiva.
Il bundle sorgente per l'updater e generato in modo riproducibile da
`packaging/build-client-update.sh`; una directory locale `client-update/` e
solo output estratto/di deploy ignorato da Git, mai una copia canonica.

Per installare direttamente dal sorgente:

```bash
cd client
./install.sh
```

Lo script è un **wizard end-to-end**: alla fine il client è pronto a
sincronizzare, non solo installato. Nell'ordine:

1. verifica le dipendenze, incluso `inotify-tools` (opzionale, vedi
   [§2](#2-requisiti)), e dice come installare quelle mancanti;
2. controlla/genera una chiave SSH (vedi [§5](#5-chiavi-ssh-obbligatorio)) e
   mostra la chiave pubblica da copiare sul NAS (e sul bastione, se lo usi);
3. **configurazione guidata** — chiede, con verifica live via SSH dove
   possibile:
   - dove mettere la cartella NASBox su questo PC (la crea se non esiste);
   - host/utente/porta SSH del NAS, testando subito l'accesso in chiave
     (`ssh -o BatchMode=yes`, così un tipo sbagliato si scopre qui, non più
     avanti guardando "NAS non raggiungibile" nell'app);
   - se il NAS è raggiungibile direttamente da fuori casa, oppure se serve un
     bastione (vedi [§7](#7-il-caso-del-bastione-nas-non-raggiungibile-da-fuori)),
     con lo stesso test di accesso;
   - la cartella NASBox lato NAS (default `/volume1/NASBox`) e, se già
     installato, il percorso di `sync-daemon-server.sh` (facoltativo: si può
     anche lasciare vuoto e usare "Rileva dal NAS" più avanti dalla GUI);

   Se lanci di nuovo `install.sh` su un client già configurato, te lo chiede
   prima di sovrascrivere: puoi rispondere "no" e lasciare la configurazione
   esistente intatta, eseguendo solo i passi 1-2 e quelli sotto.
4. installa un lanciatore in `~/.local/bin/sync-daemon-gui`;
5. crea una voce nel menu applicazioni ("NASBox");
6. installa e abilita un **servizio systemd utente**
    (`~/.config/systemd/user/sync-daemon-client.service`), così il client parte
    da solo ad ogni login senza doverlo aprire a mano. Se `systemctl` non è
    disponibile sul sistema, questo passaggio viene saltato con un avviso (il
    lanciatore e la voce di menu restano comunque installati). Comandi utili:

   ```bash
   systemctl --user start sync-daemon-client.service     # avvialo subito
   systemctl --user status sync-daemon-client.service    # stato
    journalctl --user -u sync-daemon-client.service -f     # log del servizio
    systemctl --user disable sync-daemon-client.service    # disattiva l'avvio automatico
    ```

    Il servizio segnala a NASBox di essere un avvio in background: non mostra
    finestre di conferma per gli aggiornamenti e, se parte prima della sessione
    grafica, usa automaticamente Qt in modalità `offscreen` invece di andare in
    crash per il plugin X11. L'avvio dal menu applicazioni resta invece il
    percorso GUI interattivo.

Se salti la configurazione testuale, il primo avvio apre un wizard grafico sul
PC: sceglie la cartella locale, verifica davvero la connessione SSH e prova a
rilevare script server e cartella remota. Sul NAS non serve alcuna interfaccia
grafica. WAN e bastione, insieme a banda, retention ed esclusioni, restano nel
tab **Impostazioni**. Annullando il wizard non viene salvata una configurazione
parziale e l'app resta utilizzabile per riprovare.

Ripeti questa installazione su **ogni PC** che vuoi far partecipare alla
sincronizzazione — puntandolo alla stessa cartella NASBox sul NAS.

## 4. Installazione del pacchetto server (NAS)

Il checkout Git completo del progetto deve stare su un **volume dati
persistente** del NAS. Per la configurazione ufficiale Synology usare:

- Checkout sorgente: `/volume1/Varie/sync-daemon/`
- Cartella sincronizzata (`SHARE_ROOT`): `/volume1/NASBox`

Il checkout del codice e la cartella sincronizzata sono distinti: il secondo
non deve contenere il repository Git.

> **Perché non altrove?** Su Synology DSM e QNAP QTS le cartelle `/etc` e
> `/root` possono essere azzerate o modificate dagli aggiornamenti firmware.
> Il pacchetto tiene tutto (config, log, PID) dentro sé stesso apposta, così
> sopravvive agli aggiornamenti — ma solo se lo metti su un volume dati vero.

Poi, via SSH sul NAS:

```bash
git clone https://github.com/buzzqw/NASBox.git /volume1/Varie/sync-daemon
cd /volume1/Varie/sync-daemon
sudo ./server/install.sh
```

Per un aggiornamento del sorgente già installato:

```bash
git -C /volume1/Varie/sync-daemon pull --ff-only
/volume1/Varie/sync-daemon/server/sync-daemon-server.sh --restart
```

Lo script è un **wizard**, non un semplice "copia e configura":

1. **rileva la piattaforma** (Synology DSM / QNAP QTS / Linux generico) e la
   usa per proporre un percorso di default sensato e per capire più avanti
   come registrare il demone all'avvio;
2. crea `server.conf` da `server.conf.example` se non esiste già;
3. chiede `SHARE_ROOT` (la cartella NASBox lato NAS — i client la leggono da
   qui in automatico, non serve ridigitarla, vedi
   [§6](#6-connessione-al-nas-tab-impostazioni)) e **la verifica davvero**,
   non si limita a scriverla nel config:
   - se non esiste, prova a crearla (`mkdir -p`);
   - se anche crearla fallisce — **comune su Synology DSM**: una cartella
     pensata per contenere dati condivisi va spesso creata come vera
     "Cartella condivisa" dall'interfaccia (permessi/ACL gestiti da DSM, non
     raggiungibili con un semplice `mkdir` via SSH) — lo script te lo segnala
     con le istruzioni precise (`DSM > Pannello di controllo > Cartella
     condivisa > Crea`, o l'equivalente QNAP) e aspetta che tu la crei da lì
     prima di riprovare;
   - una volta che la cartella esiste, verifica anche di **potercelo
     scrivere davvero** con un file di prova — una cartella condivisa creata
     da DSM può comunque avere ACL che escludono l'utente SSH, un problema
     che altrimenti si scoprirebbe solo al primo caricamento fallito;
4. chiede la retention dello storico (giorni, `0` = mai eliminare);
5. registra il demone come **servizio di sistema** con priorità:
   1. **systemd**, se presente (es. NAS Linux generico) — unità di sistema in
      `/etc/systemd/system/`, riavvio automatico, gestita con `systemctl`;
   2. **Synology DSM** — hook in `/usr/local/etc/rc.d/`, eseguito dal sistema
      all'avvio/spegnimento;
   3. **QNAP QTS** — nessun hook di sistema pulito senza un vero pacchetto
      QPKG: l'installer avvia comunque il demone e stampa le istruzioni per
      registrarlo manualmente in Task Scheduler ("Attività all'avvio").

Il demone **non usa cron**: è un processo persistente con un proprio timer
interno (`CHECK_INTERVAL_MINUTES` in `server.conf`, default 60 minuti).

## 5. Chiavi SSH (obbligatorio)

Il client gira in background e non può mai digitare una password interattiva.
**Serve l'autenticazione SSH a chiave**, sia verso il NAS sia — se lo usi —
verso il bastione.

`client/install.sh` genera la chiave se manca e mostra la chiave pubblica da
copiare, poi verifica da solo che l'accesso funzioni prima di proseguire. In
generale, per farlo a mano:

```bash
# Genera una chiave (se non ne hai già una)
ssh-keygen -t ed25519

# Autorizzala sul NAS
ssh-copy-id -p <porta_ssh_nas> <utente>@<host_nas>

# Se usi un bastione (vedi §7), autorizzala ANCHE lì
ssh-copy-id -p <porta_ssh_bastione> <utente>@<host_bastione>
```

Senza questo passaggio vedrai errori di autenticazione nel tab **Log** e
nessun file si sincronizzerà.

### Verifica dell'identità del NAS

Dal tab **Impostazioni** → **Verifica chiavi SSH…** puoi rendere esplicita
l'identità del NAS. Il client mostra l'impronta SHA-256 della chiave host per
l'host LAN, l'eventuale host WAN e il bastione SSH. Confrontala con quella
mostrata dal NAS o dalla sua console e accettala solo se coincide.

Il client salva le chiavi in un `known_hosts` dedicato con permessi riservati
all'utente e, dopo la verifica completa, passa a
`StrictHostKeyChecking=yes`. Un cambio successivo della chiave blocca la
connessione invece di accettarlo automaticamente. Se la verifica resta
incompleta, il collegamento continua a funzionare in modalità compatibile ma
la chiave non è ancora fissata.

## 6. Connessione al NAS (tab Impostazioni)

Tutto si configura dalla GUI, tab **Impostazioni** → riquadro "Connessione al
NAS" (la configurazione guidata di `install.sh`, [§3](#3-installazione-del-client-pc),
scrive qui gli stessi identici valori). Niente è fisso nel codice: ogni campo
è salvato in `client.json` e riletto dal motore di sincronizzazione ad ogni
ciclo (cambi bandwidth/host senza riavviare l'app).

| Campo | Significato |
|---|---|
| Host LAN | Indirizzo del NAS sulla rete locale (es. `192.0.2.10` o `nas.local`) |
| Host WAN diretto (opzionale) | Solo se il NAS **è** raggiungibile direttamente da internet — lascia vuoto altrimenti |
| Utente SSH sul NAS | Utente con cui il client si collega al NAS |
| Porta SSH sul NAS | Di solito 22 |
| Host / Porta / Utente bastione | Vedi [§7](#7-il-caso-del-bastione-nas-non-raggiungibile-da-fuori) |
| Script server sul NAS | Percorso di `sync-daemon-server.sh` sul NAS. **Puoi lasciarlo vuoto**: "Rileva dal NAS" lo trova da solo cercando il processo in esecuzione (`ps aux`), a patto che il demone sia avviato. Se il demone è fermo, va indicato a mano |
| Cartella NASBox sul NAS | Cartella sul NAS con cui la cartella NASBox locale viene sincronizzata, root contro root. **Non serve scriverla a mano**: premi "Rileva dal NAS" |
| Propaga le cancellazioni | Se spenta, i file cancellati localmente restano sul NAS (e viceversa) |

**Pulsante "Rileva dal NAS"** (accanto al campo Cartella NASBox sul NAS): si
collega al NAS con i parametri sopra. Se "Script server sul NAS" è vuoto,
prova prima a trovarlo da solo cercando il processo del demone in esecuzione
(`ps aux`); poi verifica che sia effettivamente attivo e legge da lì, via
SSH, il vero `SHARE_ROOT` e la retention configurata in `server.conf` —
niente più valori da tenere allineati a mano in due posti diversi. Il NAS
resta l'unica fonte di verità: se cambi `server.conf`, basta premere di
nuovo questo pulsante su ogni client per riallinearlo. Se il demone risulta
fermo, la ricerca automatica del percorso non trova nulla (non c'è nessun
processo da cercare) — in quel caso indicalo a mano una volta sola.

**Pulsante "Riavvia demone"** (accanto al campo Script server sul NAS): manda
`--restart` al demone sul NAS via SSH, con richiesta di conferma. Utile se il
demone sembra bloccato o non risponde. Riguarda solo la pulizia dello storico
sul NAS ([§17](#17-comandi-del-demone-server-nas)) — la sincronizzazione dei
file non dipende da lui e non viene interrotta dal riavvio.

Il client controlla anche da solo, ogni 15 minuti, se il demone sul NAS
risulta attivo (oltre al controllo versione, vedi
[§17](#17-comandi-del-demone-server-nas)): se lo trova fermo prova a
riavviarlo automaticamente e te lo segnala — notifica nella tray + riga nel
tab Log (`SERVER_DOWN` quando lo rileva fermo, `SERVER_RESTARTED` con l'esito
del tentativo). Non serve premere "Riavvia demone" a mano se non vuoi
forzarlo subito: il client se ne accorge comunque entro 15 minuti.

Ad ogni ciclo il client prova, in ordine: Host LAN → Host WAN diretto →
bastione. Usa la prima via che risulta raggiungibile in quel momento — utile
se sposti il portatile dentro/fuori casa senza toccare nulla (vedi anche
[§7](#7-il-caso-del-bastione-nas-non-raggiungibile-da-fuori): quando sei in
LAN il bastione non viene proprio contattato).

## 7. Il caso del bastione (NAS non raggiungibile da fuori)

Scenario tipico (ed è esattamente il caso per cui questa funzione esiste):
il NAS non ha un indirizzo pubblico proprio, ma un **altro PC** sulla stessa
rete locale sì (es. raggiungibile via un DDNS + port forwarding sul router).

Esempio di documentazione:

- NAS: `192.0.2.10`, non raggiungibile da fuori casa
- PC bastione: `192.0.2.20`, raggiungibile da internet come
  `bastion.example.net` porta `2222`, e che vede il NAS regolarmente sulla LAN

Configurazione nel client (tab Impostazioni):

| Campo | Valore in questo esempio |
|---|---|
| Host LAN | `192.0.2.10` |
| Host bastione (ponte) | `bastion.example.net` |
| Porta SSH sul bastione | `2222` |
| Utente SSH sul bastione | utente SSH del bastione (vuoto = riusa l'utente del NAS) |

**Come funziona:** quando il client **non è in LAN** (l'Host LAN non risponde
entro pochi secondi), prova il bastione. Se il bastione è raggiungibile, il
client apre la connessione SSH **attraverso** di esso
(`ssh -o ProxyJump=utente@bastion.example.net:2222 ... 192.0.2.10`) — la
destinazione finale resta sempre l'indirizzo LAN del NAS: il bastione fa solo
da tramite per l'ultimo salto, non serve che i file passino "attraverso" di
lui a livello applicativo.

Quando invece il client **è** in LAN, il tentativo diretto all'Host LAN
riesce subito e il bastione non viene nemmeno interrogato: zero passaggio da
internet per il traffico locale.

Requisiti perché funzioni:
- la tua chiave SSH deve essere autorizzata **sia sul NAS sia sul bastione**
  (vedi [§5](#5-chiavi-ssh-obbligatorio));
- dal bastione deve essere effettivamente possibile raggiungere il NAS in SSH
  (stessa LAN, o comunque instradamento valido) — è quello che, secondo la
  tua descrizione, è già vero nel tuo caso.

## 8. La cartella NASBox

NASBox sincronizza **un'unica cartella** per PC (di default `~/NASBox`) con
un'unica cartella sul NAS, root contro root: qualunque cosa ci metti dentro —
sottocartelle comprese — la ritrovi su ogni altro PC collegato e sul NAS.
Non esiste più un elenco di "condivisioni" da aggiungere, modificare o
rimuovere: la cartella si sceglie una volta (al primo avvio, o durante
`install.sh`, [§3](#3-installazione-del-client-pc)) e da lì in poi è
sempre e solo quella.

**Tab Stato:**

- La riga **Controllo modifiche** indica se il client usa `inotify` (rilevamento
  immediato) oppure la scansione periodica ogni 2 secondi. Se compare la
  scansione periodica, installa `inotify-tools` per ridurre I/O e CPU sugli
  alberi locali grandi; il client continua comunque a funzionare.
- Un trasferimento rimandato per **lock NAS occupato** è una contesa normale e
  viene ritentato automaticamente. Un errore del lock, della connessione SSH o
  del trasferimento rsync viene invece indicato separatamente e resta visibile
  nel riepilogo degli ultimi problemi.
- Il riquadro "Cartella NASBox" mostra il percorso attuale, con **Apri
  cartella** (la apre nel file manager) e **Cambia percorso…**.
- **Cambia percorso…** aggiorna solo *dove il client guarda* sul disco — **non
  sposta da sola i file già presenti nella vecchia posizione**. Se stai
  spostando la cartella su questo stesso PC, muovi prima tu i file (es. col
  file manager) e poi punta il client alla nuova posizione; se invece parti
  da una cartella vuota, il client la riempirà da capo scaricando tutto dal
  NAS al prossimo ciclo. Questa scelta è voluta: un trasferimento file
  automatico durante un cambio di percorso è esattamente il tipo di
  operazione "invisibile" che in passato ha causato una perdita di dati — qui
  preferiamo un passaggio manuale ed esplicito a un'automazione rischiosa.

**Sincronizzazione selettiva (tab Impostazioni → "Escludi dalla
sincronizzazione"):** invece di poter escludere solo intere cartelle
(come il "selective sync" di Dropbox), NASBox accetta pattern in stile
rsync — utile per cache, cartelle di build o file temporanei che non ha
senso portarsi dietro su un NAS condiviso, es.:

```
node_modules/
.cache/
*.tmp
.git/
.venv/
```

Un pattern aggiunto qui si applica **subito**, in entrambe le direzioni: un
file/cartella già sincronizzato non viene toccato retroattivamente (non
cancella nulla), semplicemente smette di essere considerato ai giri
successivi. Per esempio, `.git/` esclude i repository Git e `.venv/` gli
ambienti virtuali Python, anche se si trovano dentro una sottocartella di
NASBox. Se erano già stati sincronizzati, l'esclusione non li rimuove dal NAS
o dagli altri PC: per eliminarne una copia esistente serve farlo manualmente.

### Cartelle esterne (mirror)

Puoi tenere dentro NASBox la copia sincronizzata di una o più cartelle che si
trovano FUORI dalla cartella NASBox — per esempio la tua cartella di lavoro —
senza spostarle. Tab **Cartelle esterne** → **Aggiungi cartella…**: scegli la
cartella sorgente (deve essere un percorso assoluto sul disco) e il nome della
sottocartella dentro NASBox dove vuoi la copia (di default il nome della
cartella sorgente). Da lì la copia viene propagata al NAS e agli altri PC dal
normale ciclo di sincronizzazione.

- È un mirror a **senso unico**: la cartella sorgente resta autorevole.
  Qualunque modifica fatta altrove alla copia dentro NASBox viene sovrascritta
  (o eliminata, se la cartella sorgente non contiene più quel file) alla
  passata successiva.
- La copia si aggiorna quasi subito dopo ogni modifica della sorgente
  (rilevamento inotify, come per la cartella NASBox), con un re-sync periodico
  di sicurezza ogni 5 minuti che recupera anche le modifiche avvenute col PC
  spento o sospeso.
- La colonna **Attiva** mette in pausa il mirror senza rimuoverlo;
  **Sincronizza ora** forza una copia immediata. La tab mostra anche l'ultima
  sincronizzazione e gli eventuali errori (es. cartella sorgente non trovata).
- I pattern di esclusione (vedi sotto) valgono anche per le copie mirror: ciò
  che escludi dalla sincronizzazione non finisce nelle copie, e le copie non
  esportano le cartelle interne di NASBox (`.sync-trash`, `.sync-partial`,
  `.nasbox-root`).
- Le cancellazioni della copia seguono l'impostazione globale **Propaga le
  cancellazioni** della cartella NASBox.

### Marker del repository e volume locale

Il server crea nella radice di `SHARE_ROOT` il file nascosto `.nasbox-root`.
Contiene un identificatore univoco del repository e non è un file utente: il
client lo esclude sempre dal trasferimento. Il marker impedisce di confondere
una directory vuota con il vero volume NAS, per esempio quando un volume
Synology o QNAP è smontato ma il punto di mount continua a esistere.

Il client conserva inoltre, fuori dalla cartella NASBox, un marker in:

```text
~/.local/state/sync-daemon/repository-marker.json
```

Questo file registra il percorso scelto, l'identificatore del filesystem e
l'ID del repository NAS. Se il volume locale cambia, viene smontato o il NAS
presenta un altro repository, il client lo segnala e non esegue operazioni
distruttive.

L'installazione del server crea il marker tramite `--init-repository`. Non
eliminarlo e non copiarlo manualmente da un altro NASBox. Se il marker remoto
manca o è corrotto, verificare prima il volume e solo dopo inizializzarlo: la
creazione non deve essere usata per riparare alla cieca un percorso sospetto.

### Cancellazioni protette

Le cancellazioni restano disattivate di default. Se abiliti **Propaga le
cancellazioni**, prima di ogni trasferimento il client verifica marker locale
e remoto, identità del filesystem locale, percorso NASBox remoto e numero di
cancellazioni proposte.

Un batch superiore a 1000 cancellazioni viene bloccato come anomalo. Il limite
si trova in `client.json` come `max_delete_files` e può essere aumentato solo
consapevolmente per repository che devono gestire cancellazioni più grandi.

Il comando server `--run-once --dry-run` riguarda la pulizia delle vecchie
versioni in `.sync-trash`; non sostituisce il preflight della sincronizzazione
file. Prima di autorizzare una cancellazione massiva controlla quindi anche la
tab **Trasferimenti** del client.

Il trasferimento usa `--delete-after`: le cancellazioni vengono rimandate alla
fine, dopo il trasferimento dei nuovi dati. Le versioni sostituite o cancellate
vengono conservate nello storico prima di essere rimosse. Se il marker, il
volume o la scansione non sono verificabili, NASBox si ferma con un evento
`SAFETY_BLOCK` nel tab **Log**.

### Conflitti con cancellazioni

Lo stato locale conserva anche un tombstone per un file cancellato. Questo
permette di distinguere un file mai visto da un file cancellato dopo una
sincronizzazione precedente.

Se un PC modifica un file mentre un altro lo cancella, la versione modificata
locale non viene trattata come un file qualsiasi: il client registra
`CONFLICT_REMOTE_DELETE` e mantiene la versione locale invece di perderla
silenziosamente. La versione NAS viene preservata come copia di conflitto
quando entrambe le parti hanno modificato lo stesso file.

## 9. Limite di banda

Tab Impostazioni → riquadro "Limite di banda": due valori **separati**, ↑
Carica e ↓ Scarica, in KB/s (`0` = illimitato ciascuno). Premendo "Salva
impostazioni" i nuovi limiti vengono usati dal ciclo di sincronizzazione
successivo, senza riavviare l'app — puoi ad esempio lasciare libero lo
scaricamento e limitare solo il caricamento (o viceversa).

## 10. Frequenza di sincronizzazione

- **Caricamento (push)**: quasi immediato — un controllo rileva i cambiamenti
  nella cartella NASBox (tramite `inotify` se disponibile sul sistema,
  altrimenti un controllo periodico leggero ogni 2 secondi, vedi
  [§2](#2-requisiti)) e invia il file al NAS dopo un breve "assestamento"
  (debounce, default 2 secondi, configurabile in `client.json` come
  `debounce_seconds`).
- **Scaricamento (pull)**: periodico, intervallo configurabile dal tab
  Impostazioni ("Controlla il NAS per novità ogni…"), default 60 secondi. È
  così che un client scopre cosa hanno caricato gli altri.

Caricamento e scaricamento girano su percorsi indipendenti: uno scaricamento
lungo (es. il NAS ha molti file da controllare) non ritarda un caricamento
appena pronto, e viceversa. Restano invece sincronizzati tra loro: non
caricano e scaricano nello stesso istante, per evitare conflitti fra una
modifica appena fatta e uno scaricamento in corso.

**Pulsante "Sincronizza tutto ora"** (tab Stato, sopra il riquadro "Cartella
NASBox"): forza subito il controllo in entrambe le direzioni, senza aspettare
l'intervallo di scaricamento. Marca anche la cartella locale per un nuovo
controllo di caricamento, quindi una modifica mostrata in coda viene inviata
anche se il watcher l'aveva rilevata prima dell'avvio dell'app. L'app mostra
subito che la richiesta e stata presa in carico. Se la sincronizzazione è in
pausa, avvisa che va prima ripresa.

## 11. Pausa e pausa a tempo

Tab Stato (e anche dall'icona nella tray, vedi [§13](#13-icona-nella-tray)):

- **Pausa / Riprendi**: sospende push e pull (la scansione della coda file
  continua, così vedi comunque cosa è in attesa). Se in quel momento un file
  è già a metà trasferimento, viene interrotto subito — la pausa non aspetta
  che finisca da solo.
- **Pausa per…**: apre una finestra con durate preimpostate (15 min, 30 min,
  1h, 4h, 8h) o un valore personalizzato in minuti. Il conto alla rovescia
  fino alla ripresa è visibile nello stato; scaduto il tempo, riprende da
  sola senza bisogno di intervenire. Anche questa interrompe subito un
  eventuale trasferimento in corso.

## 12. Coda dei trasferimenti

Tab **Trasferimenti**: elenco di cosa è in attesa di essere caricato (↑),
scaricato (↓) o eliminato (🗑), con dimensione.

L'elenco è un'anteprima della **coda corrente** calcolata con un controllo
senza scritture; non è lo storico delle operazioni. Si aggiorna durante e dopo
la sincronizzazione.

- **Velocità live**: due indicatori ("↑ Carica" / "↓ Scarica") mostrano la
  velocità in tempo reale mentre un trasferimento è in corso (letta
  direttamente dal progresso di rsync), non un valore medio a posteriori.
  Tornano a "-" quando non c'è nulla in corso in quella direzione.
- I file vengono rimossi dalla coda **appena completati**, uno per uno,
  invece di aspettare il prossimo controllo periodico — durante un
  trasferimento grosso vedi il conteggio scendere via via, non restare
  fermo fino alla fine.
- **Cerca**: filtro live per nome file, sopra la tabella.

## 13. Icona nella tray

L'app resta attiva in background quando chiudi la finestra (icona nella
system tray) — chiudere la finestra non ferma la sincronizzazione. Click
sull'icona per riaprire la finestra principale; click destro per il menu:

- **Stato** (riga in grigio, non cliccabile): riassume la situazione attuale
  — es. "✅ Attivo — 192.0.2.10" oppure "✅ Attivo — 192.0.2.10 (via
  bastione)" oppure "⚠️ NAS non raggiungibile".
- **Apri cartella NASBox**: apre la cartella locale nel file manager.
- **File recenti ▸**: gli ultimi upload/download/cancellazioni; cliccandone
  uno si apre la cartella che lo contiene.
- **Pausa / Riprendi** e **Pausa per…**: stessi controlli del tab Stato,
  senza dover riaprire la finestra.
- **Apri finestra principale**.
- **Esci**: questa sì chiude davvero l'app (ferma anche il motore di
  sincronizzazione). Se in quel momento c'è un trasferimento grosso in corso,
  viene interrotto subito; durante l'arresto compare un messaggio di attesa e
  l'app si chiude appena i processi in background terminano.

## 14. Log

Tab **Log**: cronologia di ogni upload, download, cancellazione ed errore,
con filtro per tipo di azione e un campo **Cerca** (per percorso o dettaglio)
accanto al filtro — la ricerca è "debounced": non rilegge il file ad ogni
tasto premuto, solo quando smetti di digitare, per restare fluida anche con
uno storico lungo. Persistito su disco anche a app chiusa:

- `~/.local/state/sync-daemon/client.log` — testo semplice, leggibile con `tail -f`
- `~/.local/state/sync-daemon/events.jsonl` — un evento JSON per riga, usato
  dalla GUI stessa

Oltre a `UPLOAD` / `DOWNLOAD` / `DELETE_LOCAL` / `DELETE_REMOTE` / `ERROR`,
altre azioni che puoi trovare nel filtro:

| Azione | Significato |
|---|---|
| `CANCELLED` | Un trasferimento è stato interrotto da una pausa o dalla chiusura dell'app — non è un errore |
| `PULL_DEFERRED` | Uno scaricamento in corso è stato annullato perché nel frattempo è stata rilevata una modifica locale non ancora inviata; riprovato al giro successivo |
| `SERVER_DOWN` | Il controllo periodico ([§17](#17-comandi-del-demone-server-nas)) ha trovato il demone sul NAS fermo e sta provando a riavviarlo |
| `SERVER_RESTARTED` | Esito del tentativo di riavvio automatico del demone |
| `SERVER_OUTDATED` | Il pacchetto server sul NAS è più vecchio di quanto questo client si aspetti (vedi [§18](#18-risoluzione-problemi)) |
| `PRUNE_LOCAL_TRASH` / `PRUNE_REMOTE_TRIGGER` | Pulizia dello storico, locale o richiesta al NAS (vedi [§15](#15-storico--cestino)) |

## 15. Storico / Cestino

Ogni volta che un file viene sovrascritto o cancellato (in push o in pull),
la versione precedente **non sparisce**: viene spostata in una cartella
`.sync-trash/` (sia sul NAS sia nella copia locale di trash) con il nome
rinominato aggiungendo il timestamp esatto, ad esempio:

```
pippo.txt-2025-05-01--10-31-57-123456Z
pippo.txt-2025-05-01--10-31-59-654321Z
vacanze/altro.jpg-2025-05-02--09-00-00-019283Z
```

Niente cartelle-per-versione da esplorare: guardando dentro `.sync-trash/`
si capisce a colpo d'occhio quali file sono storici e di quando. Il suffisso
`Z` indica UTC: i nomi restano ordinabili e la retention non dipende dal fuso
orario del PC che ha effettuato la modifica.

**Non sono due copie dello stesso storico** — proteggono due direzioni
diverse:
- **Sul NAS**: quando *tu* carichi (push) una modifica che ne sovrascrive
  un'altra, la versione precedente finisce qui. Protegge gli *altri* PC
  dalle tue modifiche.
- **Cestino locale (su questo PC)**: quando *scarichi* (pull) qualcosa che
  sta per sovrascrivere un tuo file locale, la TUA versione finisce qui
  invece di essere persa. Protegge te da un conflitto (una tua modifica non
  ancora caricata, sovrascritta da un aggiornamento in arrivo da un altro
  PC).

Questa doppia rete di sicurezza è più prudente del cestino/versioning che
offre Dropbox di default: qui **ogni** sovrascrittura o cancellazione, in
entrambe le direzioni, lascia una traccia recuperabile — non solo per un
numero limitato di giorni su un piano gratuito.

**Tab "Storico / Cestino" nella GUI:**

- **Cestino locale su questo PC (giorni)**: per quanto tempo il client tiene
  le versioni storiche nella sua copia locale di trash
  (`~/.local/state/sync-daemon/trash/`). `0` = non eliminare mai
  automaticamente. Si imposta e si salva qui.
- **Sul NAS (giorni)**: **sola lettura** — non è un valore che si imposta in
  questo client. Riflette l'ultimo valore letto dal NAS con "Rileva dal NAS"
  (tab Impostazioni, [§6](#6-connessione-al-nas-tab-impostazioni)).
  L'applicazione effettiva della retention resta compito del demone server
  ([§17](#17-comandi-del-demone-server-nas)), che continua a funzionare anche
  a client spenti; qui la vedi soltanto.
- **Pulisci storico locale ora**: forza subito la pulizia locale.
- **Chiedi al NAS di pulire ora**: esegue da remoto un pass di pulizia sul
  NAS, usando lo "Script server sul NAS" impostato nel tab Impostazioni.
- **Cerca**: filtro live per nome file, sopra la tabella versioni.
- **Tabella versioni + "Ripristina selezionati…"**: elenco di ogni versione
  storica locale (file, data/ora, età in giorni). Con `Ctrl` o `Maiusc` puoi
  selezionare più file e cartelle: per ciascun file viene ripristinata la
  versione più recente selezionata. Nel cestino locale puoi anche eliminare
  definitivamente più voci con una sola conferma.

## 16. File e percorsi di configurazione

| Percorso | Contenuto |
|---|---|
| `~/.config/sync-daemon/client.json` | Configurazione client (cartella NASBox locale, connessione NAS, bastione, banda, retention, esclusioni, pausa) |
| `~/.local/state/sync-daemon/client.log` | Log testuale |
| `~/.local/state/sync-daemon/events.jsonl` | Log strutturato (eventi) |
| `~/.local/state/sync-daemon/trash/` | Cestino/storico locale |
| `<pacchetto_server>/server.conf` | Configurazione demone NAS (fonte di verità) |
| `<pacchetto_server>/state/server.log` | Log del demone NAS |
| `<pacchetto_server>/state/daemon.pid` | PID del demone NAS (se in esecuzione) |

`client.json` è JSON semplice: si può anche editare a mano (app chiusa) se
mai servisse, ma tutti i campi documentati qui sono già esposti in GUI.

## 17. Comandi del demone server (NAS)

Dalla cartella del pacchetto sul NAS:

```bash
./sync-daemon-server.sh --start              # avvia il demone in background
./sync-daemon-server.sh --stop               # lo ferma
./sync-daemon-server.sh --restart
./sync-daemon-server.sh --status             # stato + storico/retention + ultimo pass
./sync-daemon-server.sh --print-config       # dump KEY=VALUE (usato dal pulsante "Rileva dal NAS")
./sync-daemon-server.sh --init-repository    # crea il marker UUID se manca
./sync-daemon-server.sh --run-once           # un solo pass di pulizia, poi esce
./sync-daemon-server.sh --run-once --dry-run # anteprima: mostra cosa verrebbe rimosso, senza toccare nulla
./sync-daemon-server.sh --run-foreground     # esegue il loop nel terminale corrente (debug)
```

**Controllo automatico del demone:** ogni client, se ha "Script server sul
NAS" configurato, ricontrolla da solo **ogni 15 minuti** (oltre che ad ogni
"Rileva dal NAS" manuale) lo stato del demone via `--print-config`:

- **È attivo?** Se lo trova fermo, prova a riavviarlo automaticamente
  (`--start`, che non fa nulla se in realtà era già attivo) e te lo segnala —
  notifica nella tray + righe `SERVER_DOWN`/`SERVER_RESTARTED` nel tab Log
  (vedi [§14](#14-log)). Il pulsante manuale "Riavvia demone"
  ([§6](#6-connessione-al-nas-tab-impostazioni)) fa la stessa cosa ma subito,
  senza aspettare.
- **È aggiornato?** Se sul NAS gira una copia più vecchia di quella attesa
  da quel client, arriva un avviso — notifica + riga `SERVER_OUTDATED` nel
  Log — ma **al massimo una volta ogni 6 ore**, per non ripetere lo stesso
  avviso ad ogni controllo. Per aggiornare: sostituisci la cartella `server/`
  sul NAS con quella più recente e fai `./sync-daemon-server.sh --restart`
  (o il pulsante "Riavvia demone", o riavvia il servizio di sistema).

**Cartella di stato del demone auto-esclusa dalla sincronizzazione:** il
demone riporta anche, via `--print-config`, dove si trova la propria
cartella `state/` (PID, lock, log — dati che esistono solo perché il demone
gira lì, non hanno un equivalente locale da sincronizzare). Se il pacchetto
server si trova *dentro* la cartella NASBox lato NAS, il client la esclude
automaticamente dal push/pull — senza, un caricamento qualsiasi potrebbe
cancellare il PID file del demone mentre è ancora in esecuzione (visto
succedere: il controllo automatico sopra lo avrebbe poi trovato "fermo" per
errore e tentato di riavviarlo, accumulando istanze duplicate). Non serve
fare nulla per attivare questa protezione: si aggiorna da sola ad ogni
"Rileva dal NAS" e ad ogni controllo periodico.

`server.conf` (una per pacchetto installato) — **fonte di verità unica**, i
client la leggono con "Rileva dal NAS" invece di duplicarla:

```ini
SHARE_ROOT=/volume1/NASBox      # la cartella NASBox lato NAS
RETENTION_DAYS=90                # retention, in giorni (0 = non eliminare mai)
CHECK_INTERVAL_MINUTES=60        # ogni quanto il demone ricontrolla da solo
PRUNE_MAX_FILES_PER_PASS=500     # limite per passata, per non monopolizzare il lock
```

Il comando `--status` e `--print-config` mostrano anche se il lock globale di
trasferimento è occupato, da quanto tempo e, quando il sistema lo consente, il
PID del processo che lo possiede. Questo aiuta a distinguere una sincronizzazione
lunga da un problema di connessione.

Durante l'installazione viene creato anche `SHARE_ROOT/.nasbox-root`. Il
comando `--print-config` espone `REPOSITORY_ID` e `REPOSITORY_READY`; i client
recenti richiedono `REPOSITORY_READY=true` prima di propagare cancellazioni.

> Versioni del pacchetto server precedenti alla 3.0.0 supportavano più
> "condivisioni" nominate sotto `SHARE_ROOT`, ciascuna con la propria
> retention (`RETENTION_DAYS_<nome>`). Quel modello è stato ritirato insieme
> all'equivalente lato client ([§8](#8-la-cartella-nasbox)): oggi
> `SHARE_ROOT` è sempre e solo la cartella NASBox, con un'unica retention.

Se rimuovi il pacchetto: `./uninstall.sh` ferma il demone e rimuove
l'eventuale registrazione come servizio di sistema (systemd o rc.d); non
cancella `server.conf` né i log.

## 18. Risoluzione problemi

**"NAS non raggiungibile" nel tab Stato**
- Prova manualmente: `ssh -p <porta> <utente>@<host_lan>` dallo stesso PC.
- Se usi un bastione, prova anche: `ssh -p <porta_bastione> <utente>@<host_bastione>`
  e poi da lì `ssh <utente>@<host_lan_nas>`.
- Controlla che la chiave SSH sia autorizzata su entrambi ([§5](#5-chiavi-ssh-obbligatorio)).

**"Cartella NASBox non configurata" nel tab Stato**
- Premi "Cambia percorso…" nel riquadro "Cartella NASBox" e scegline una:
  succede se hai saltato la configurazione guidata di `install.sh` e annullato
  anche il wizard grafico del primo avvio.

**"Rileva dal NAS" fallisce**
- Verifica che "Script server sul NAS" punti al percorso esatto di
  `sync-daemon-server.sh` (non alla cartella, al file).
- Prova a lanciare a mano `ssh <utente>@<host> <percorso>/sync-daemon-server.sh --print-config`
  per vedere l'errore per esteso.

**Notifica "aggiornamento NAS richiesto" nella tray / riga `SERVER_OUTDATED` nel Log**
- Significa che la versione del pacchetto `server/` installata sul NAS è più
  vecchia di quella che questo client si aspetta (es. non ha ancora
  `--print-config` o `STATE_DIR`, aggiunti in versioni successive). Copia la
  cartella `server/` più recente sul NAS, sovrascrivendo i file `.sh` (lascia
  stare `server.conf`, quello è tuo), poi `./sync-daemon-server.sh --restart`
  (o il pulsante "Riavvia demone", [§6](#6-connessione-al-nas-tab-impostazioni)).
- Il controllo è automatico ogni 15 minuti per ogni client con "Script
  server sul NAS" impostato, ma l'avviso stesso non si ripete più di una
  volta ogni 6 ore — non serve controllare a mano, se non hai ricevuto
  nessun avviso è già tutto allineato (vedi
  [§17](#17-comandi-del-demone-server-nas)).

**Notifica "demone NAS" nella tray / righe `SERVER_DOWN` o `SERVER_RESTARTED` nel Log**
- `SERVER_DOWN`: il controllo automatico ha trovato il demone fermo e sta
  provando a riavviarlo da solo — non serve fare nulla, verifica solo la riga
  `SERVER_RESTARTED` subito dopo per l'esito.
- `SERVER_RESTARTED` con esito positivo: tutto risolto da solo.
- Se il riavvio automatico **fallisce** (messaggio nella notifica/nel Log),
  serve un intervento manuale sul NAS: prova `./sync-daemon-server.sh --status`
  via SSH per capire perché non riparte (spesso: `server.conf` mancante o
  corrotto, o il volume dati non montato).

**I file non si sincronizzano ma la connessione risulta attiva**
- Controlla il tab **Log**, filtro `ERROR`: quasi sempre è un problema di
  permessi sulla cartella remota o di spazio esaurito sul NAS.
- Verifica con "Rileva dal NAS" che la "Cartella NASBox sul NAS" corrisponda
  davvero a `SHARE_ROOT` sul NAS.
- Controlla anche il tab Impostazioni → "Escludi dalla sincronizzazione": un
  pattern troppo largo (es. `*` per errore) esclude tutto senza dare nessun
  errore, semplicemente non c'è niente da trasferire.

**Il demone server non riparte dopo un riavvio del NAS**
- `./sync-daemon-server.sh --status` per vedere se è attivo.
- Se hai systemd: `systemctl status sync-daemon-server`.
- Su Synology senza systemd: controlla che `/usr/local/etc/rc.d/sync-daemon-server.sh`
  esista e sia eseguibile.
- Su QNAP: verifica che l'attività sia registrata in Task Scheduler come
  "Attività all'avvio" con comando `<percorso>/sync-daemon-server.sh --start`.

**Voglio vedere se una retention sta davvero eliminando quello che penso**
- Usa sempre prima `--run-once --dry-run` sul NAS: elenca cosa verrebbe
  rimosso senza cancellare nulla.
