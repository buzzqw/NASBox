package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
	"nasbox-go/internal/discover"
	"nasbox-go/internal/remote"
)

// showSetupWizard builds the first-run configuration page. It mirrors the
// Python wizard's essentials: local folder, SSH endpoint, NAS detection and a
// real connection test before the sync engine starts.
func showSetupWizard(fyneApp fyne.App, window fyne.Window, cfg *config.Config, onDone func()) {
	fyneApp.Settings().SetTheme(newNAStheme(variantFrom(cfg.String("go_theme"))))

	localEntry := widget.NewEntry()
	localEntry.SetText(cfg.LocalRoot())
	if strings.TrimSpace(localEntry.Text) == "" {
		// A sensible default: most people want a NASBox folder in their home.
		if home, err := os.UserHomeDir(); err == nil {
			localEntry.SetText(filepath.Join(home, "NASBox"))
		}
	}
	hostEntry := widget.NewEntry()
	hostEntry.SetText(cfg.String("nas_lan"))
	userEntry := widget.NewEntry()
	userEntry.SetText(cfg.String("nas_user"))
	portEntry := widget.NewEntry()
	portEntry.SetText(fmt.Sprint(cfg.Int("ssh_port")))
	prefixEntry := widget.NewEntry()
	prefixEntry.SetText(cfg.String("remote_prefix"))
	scriptEntry := widget.NewEntry()
	scriptEntry.SetText(cfg.String("remote_server_script"))
	status := widget.NewLabel(tr("setup.detect_optional", nil))
	status.Wrapping = fyne.TextWrapWord

	apply := func() error {
		root := strings.TrimSpace(localEntry.Text)
		host := strings.TrimSpace(hostEntry.Text)
		user := strings.TrimSpace(userEntry.Text)
		if root == "" {
			return fmt.Errorf("%s", tr("setup.local_failed_title", nil))
		}
		if host == "" || user == "" {
			return fmt.Errorf("%s", tr("setup.connection_missing", nil))
		}
		port, err := strconv.Atoi(strings.TrimSpace(portEntry.Text))
		if err != nil || port <= 0 || port > 65535 {
			port = 22
		}
		_ = cfg.Set("local_root", root, false)
		_ = cfg.Set("nas_lan", host, false)
		_ = cfg.Set("nas_user", user, false)
		_ = cfg.Set("ssh_port", port, false)
		_ = cfg.Set("remote_prefix", strings.TrimSpace(prefixEntry.Text), false)
		_ = cfg.Set("remote_server_script", strings.TrimSpace(scriptEntry.Text), false)
		return nil
	}

	persist := func() error {
		if err := os.MkdirAll(strings.TrimSpace(localEntry.Text), 0o700); err != nil {
			return err
		}
		for _, key := range []string{
			"local_root", "nas_lan", "nas_user", "ssh_port", "remote_prefix", "remote_server_script",
		} {
			if err := cfg.Set(key, cfg.Get(key), true); err != nil {
				return err
			}
		}
		return nil
	}

	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			localEntry.SetText(uri.Path())
		}, window)
	})

	// ---- three simple steps instead of one long form -------------------------
	// 1. where the files live on this PC   2. connect to the NAS   3. start.
	// Port, NAS folder and server script are detected; they stay available
	// under "Advanced" for the rare NAS that needs them.
	stepTitle := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	stepTitle.SizeName = theme.SizeNameSubHeadingText
	stepBox := container.NewVBox()
	var showStep func(int)
	field := func(label string, object fyne.CanvasObject) fyne.CanvasObject {
		caption := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		return container.NewVBox(caption, object)
	}
	setStep := func(index int, title string, items ...fyne.CanvasObject) {
		stepTitle.SetText(lt(fmt.Sprintf("Passo %d di 3 — %s", index, title), fmt.Sprintf("Step %d of 3 — %s", index, title)))
		stepBox.Objects = items
		stepBox.Refresh()
	}

	// Machines found on the network (mDNS + SSH scan), offered as one-click
	// choices so nobody has to know the NAS address.
	foundBox := container.NewVBox()
	foundLabel := widget.NewLabel("")
	foundLabel.Importance = widget.LowImportance
	foundLabel.Wrapping = fyne.TextWrapWord
	searching := false
	search := func() {
		if searching {
			return
		}
		searching = true
		foundLabel.SetText(lt("Cerco il NAS in rete…", "Looking for the NAS on the network…"))
		go func() {
			defer func() { _ = recover() }() // the window may be gone (tests, shutdown)
			candidates := discover.Find(context.Background(), 5*time.Second)
			fyne.Do(func() {
				searching = false
				foundBox.Objects = nil
				if len(candidates) == 0 {
					foundLabel.SetText(lt("Non ho trovato nessun NAS in rete: scrivi l'indirizzo qui sotto.", "I found no NAS on the network: type the address below."))
					foundBox.Refresh()
					return
				}
				foundLabel.SetText(lt("Trovati in rete — tocca il tuo NAS:", "Found on the network — tap your NAS:"))
				limit := len(candidates)
				if limit > 5 {
					limit = 5
				}
				for _, candidate := range candidates[:limit] {
					candidate := candidate
					foundBox.Add(plainButton(candidate.Label(), theme.ComputerIcon(),
						lt("Usa questo indirizzo", "Use this address"), widget.MediumImportance, func() {
							hostEntry.SetText(candidate.Host())
						}, func(string) {}))
				}
				// A single machine that advertised itself and nothing typed yet:
				// fill it in, the person only has to confirm.
				if strings.TrimSpace(hostEntry.Text) == "" && candidates[0].MDNS {
					hostEntry.SetText(candidates[0].Host())
				}
				foundBox.Refresh()
			})
		}()
	}

	// Key setup panel: shown only when the NAS answers but does not know this
	// PC's SSH key. The password is used once to install the key; it is not
	// saved anywhere.
	passwordEntry := widget.NewPasswordEntry()
	keyStatus := widget.NewLabel("")
	keyStatus.Wrapping = fyne.TextWrapWord
	keyPanel := container.NewVBox()
	var connectNow func()
	authorize := plainButton(lt("Autorizza questo PC", "Authorise this PC"), theme.ConfirmIcon(),
		lt("Copia la chiave di questo PC sul NAS usando la password, una volta sola", "Copy this PC's key to the NAS using the password, just once"),
		widget.HighImportance, nil, func(string) {})
	authorize.OnTapped = func() {
		if passwordEntry.Text == "" {
			keyStatus.SetText(lt("Scrivi la password del NAS.", "Type the NAS password."))
			return
		}
		authorize.Disable()
		keyStatus.SetText(lt("Autorizzo questo PC sul NAS…", "Authorising this PC on the NAS…"))
		host, user, password := strings.TrimSpace(hostEntry.Text), strings.TrimSpace(userEntry.Text), passwordEntry.Text
		port, _ := strconv.Atoi(strings.TrimSpace(portEntry.Text))
		if port <= 0 {
			port = 22
		}
		go func() {
			var err error
			keyPath, _, keyErr := remote.LocalPublicKey()
			if keyErr != nil {
				err = keyErr
			} else if data, readErr := os.ReadFile(keyPath); readErr != nil {
				err = readErr
			} else {
				err = remote.InstallKey(context.Background(), host, port, user, password, string(data))
			}
			fyne.Do(func() {
				authorize.Enable()
				if err != nil {
					if errors.Is(err, remote.ErrWrongPassword) {
						keyStatus.SetText(lt("Password non corretta: riprova.", "Wrong password: try again."))
					} else {
						keyStatus.SetText(humanSetupError(err.Error()))
					}
					return
				}
				passwordEntry.SetText("")
				keyPanel.Objects = nil
				keyPanel.Refresh()
				connectNow()
			})
		}()
	}

	advanced := widget.NewAccordion(widget.NewAccordionItem(lt("Impostazioni avanzate (di solito non servono)", "Advanced settings (usually not needed)"),
		container.NewVBox(
			field(lt("Porta SSH", "SSH port"), portEntry),
			field(lt("Cartella sul NAS", "Folder on the NAS"), prefixEntry),
			field(lt("Script del server sul NAS", "Server script on the NAS"), scriptEntry),
		)))

	connect := plainButton(lt("Collegati al NAS", "Connect to the NAS"), theme.ComputerIcon(),
		lt("Prova la connessione e cerca da solo le impostazioni del NAS", "Test the connection and find the NAS settings by itself"),
		widget.HighImportance, nil, func(string) {})
	connect.OnTapped = func() {
		if err := apply(); err != nil {
			status.SetText(err.Error())
			return
		}
		connect.Disable()
		status.SetText(lt("Mi collego al NAS…", "Connecting to the NAS…"))
		go func() {
			client := remote.New(cfg)
			values, err := client.PrintConfig()
			if err != nil {
				// The server script path may simply be different: look for it.
				if scripts, derr := client.DiscoverServerScripts(); derr == nil && len(scripts) > 0 {
					_ = cfg.Set("remote_server_script", scripts[0], false)
					scriptEntry.SetText(scripts[0])
					values, err = remote.New(cfg).PrintConfig()
				}
			}
			if err == nil && values["SHARE_ROOT"] != "" {
				_ = cfg.Set("remote_prefix", values["SHARE_ROOT"], false)
				prefixEntry.SetText(values["SHARE_ROOT"])
			}
			fyne.Do(func() {
				connect.Enable()
				if err != nil {
					if remote.NeedsKeySetup(err.Error()) {
						status.SetText("")
						keyStatus.SetText(lt("Il NAS risponde, ma non conosce ancora questo PC. Scrivi la password del NAS una volta sola: NASBox la usa per autorizzare questo PC e poi la dimentica.",
							"The NAS answers but does not know this PC yet. Type the NAS password once: NASBox uses it to authorise this PC and then forgets it."))
						keyPanel.Objects = []fyne.CanvasObject{
							keyStatus, field(lt("Password del NAS", "NAS password"), passwordEntry), container.NewHBox(authorize),
						}
						keyPanel.Refresh()
						return
					}
					status.SetText(lt("Non riesco a collegarmi. ", "I cannot connect. ") + humanSetupError(err.Error()))
					advanced.Open(0)
					return
				}
				status.SetText("")
				showStep(3)
			})
		}()
	}

	connectNow = connect.OnTapped

	start := plainButton(lt("Inizia a sincronizzare", "Start syncing"), theme.ConfirmIcon(),
		lt("Salva le impostazioni e avvia la sincronizzazione", "Save the settings and start syncing"),
		widget.HighImportance, nil, func(string) {})
	start.OnTapped = func() {
		start.Disable()
		status.SetText(lt("Preparo il NAS…", "Preparing the NAS…"))
		go func() {
			bootstrapErr := remote.New(cfg).Bootstrap()
			fyne.Do(func() {
				start.Enable()
				finish := func() {
					if err := persist(); err != nil {
						status.SetText(tr("setup.local_failed_title", nil) + ": " + err.Error())
						return
					}
					onDone()
				}
				if bootstrapErr != nil {
					dialog.ShowConfirm(lt("Il NAS non ha risposto del tutto", "The NAS did not fully answer"),
						lt("Posso comunque continuare: NASBox riproverà da solo. Continuare?", "I can still continue: NASBox will retry by itself. Continue?"),
						func(ok bool) {
							if ok {
								finish()
							}
						}, window)
					return
				}
				finish()
			})
		}()
	}

	showStep = func(step int) {
		switch step {
		case 1:
			titleIT := "Dove tieni i file"
			titleEN := "Where your files live"
			noteIT := "Scegli la cartella del tuo PC che NASBox terrà allineata con il NAS. Se non esiste la creo io."
			noteEN := "Pick the folder on this PC that NASBox keeps in line with the NAS. I create it if it does not exist."
			if cfg.Ghost() {
				titleIT, titleEN = "Cartella di download e caricamento", "Download and upload folder"
				noteIT = "Scegli la cartella dove NASBox mette i file che scarichi e da cui carica quelli che scegli. Nessun altro file del NAS verrà copiato qui."
				noteEN = "Pick the folder where NASBox puts downloaded files and reads uploads from. No other NAS file is copied here."
			}
			setStep(1, lt(titleIT, titleEN), cnote(lt(noteIT, noteEN)),
				container.NewBorder(nil, nil, nil, browse, localEntry),
				container.NewHBox(plainButton(lt("Avanti", "Next"), theme.NavigateNextIcon(), lt("Passa al collegamento con il NAS", "Go on to connect to the NAS"),
					widget.HighImportance, func() {
						if strings.TrimSpace(localEntry.Text) == "" {
							status.SetText(lt("Scegli una cartella.", "Pick a folder."))
							return
						}
						status.SetText("")
						showStep(2)
					}, func(string) {})),
			)
		case 2:
			setStep(2, lt("Collegati al tuo NAS", "Connect to your NAS"),
				cnote(lt("Indirizzo e utente del NAS, gli stessi che usi per entrarci via SSH. Il resto lo trovo da solo.",
					"Address and user of the NAS, the same you use to log in over SSH. I find the rest by myself.")),
				foundLabel, foundBox,
				field(lt("Indirizzo del NAS (nome o IP)", "NAS address (name or IP)"), hostEntry),
				field(lt("Utente", "User"), userEntry),
				keyPanel,
				advanced,
				container.NewHBox(
					plainButton(lt("Indietro", "Back"), theme.NavigateBackIcon(), lt("Torna alla scelta della cartella", "Back to the folder choice"),
						widget.LowImportance, func() { status.SetText(""); showStep(1) }, func(string) {}),
					connect,
					plainButton(lt("Cerca di nuovo", "Search again"), theme.SearchIcon(), lt("Cerca di nuovo il NAS in rete", "Look for the NAS on the network again"),
						widget.LowImportance, search, func(string) {}),
				),
			)
			search()
		default:
			readyIT := fmt.Sprintf("Collegato a %s. I file di %s verranno sincronizzati con il NAS: i nuovi si caricano, quelli degli altri PC si scaricano.",
				cfg.String("nas_lan"), strings.TrimSpace(localEntry.Text))
			readyEN := fmt.Sprintf("Connected to %s. The files in %s will be synced with the NAS: new ones upload, other PCs' files download.",
				cfg.String("nas_lan"), strings.TrimSpace(localEntry.Text))
			if cfg.Ghost() {
				readyIT = fmt.Sprintf("Collegato a %s in modalità consultazione: %s è solo la cartella di download/caricamento. Nessuna sincronizzazione automatica.",
					cfg.String("nas_lan"), strings.TrimSpace(localEntry.Text))
				readyEN = fmt.Sprintf("Connected to %s in consultation mode: %s is only the download/upload folder. No automatic sync.",
					cfg.String("nas_lan"), strings.TrimSpace(localEntry.Text))
			}
			setStep(3, lt("Tutto pronto", "All set"), cnote(lt(readyIT, readyEN)),
				cnote(lt("Puoi cambiare tutto più tardi da Impostazioni.", "You can change everything later in Settings.")),
				container.NewHBox(
					plainButton(lt("Indietro", "Back"), theme.NavigateBackIcon(), lt("Torna ai dati del NAS", "Back to the NAS details"),
						widget.LowImportance, func() { status.SetText(""); showStep(2) }, func(string) {}),
					start,
				),
			)
		}
	}
	status.SetText("")
	showStep(1)
	title := widget.NewLabelWithStyle(lt("Benvenuto in NASBox", "Welcome to NASBox"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameHeadingText
	window.SetContent(container.NewPadded(container.NewVScroll(container.NewVBox(
		title,
		cnote(lt("Il tuo cloud, sul tuo NAS. Tre passi e hai finito.", "Your cloud, on your NAS. Three steps and you are done.")),
		widget.NewSeparator(),
		stepTitle,
		stepBox,
		status,
	))))
}

// humanSetupError turns a connection failure into what to check next.
func humanSetupError(detail string) string {
	low := strings.ToLower(detail)
	switch {
	case strings.Contains(low, "permission denied"):
		return lt("Il NAS ha rifiutato l'accesso: controlla l'utente e che la chiave SSH di questo PC sia autorizzata sul NAS.",
			"The NAS refused the login: check the user and that this PC's SSH key is authorised on the NAS.")
	case strings.Contains(low, "could not resolve"), strings.Contains(low, "no route"), strings.Contains(low, "timed out"),
		strings.Contains(low, "connection refused"), strings.Contains(low, "exit status 255"):
		return lt("Il NAS non risponde: controlla l'indirizzo, che sia acceso e che l'SSH sia attivo (DSM › Terminale e SNMP).",
			"The NAS does not answer: check the address, that it is on and that SSH is enabled (DSM › Terminal & SNMP).")
	case strings.Contains(low, "host key"):
		return lt("La chiave del NAS non coincide con quella già salvata: verifica che sia il NAS giusto.",
			"The NAS key does not match the saved one: check it is the right NAS.")
	}
	return lt("Dettaglio: ", "Detail: ") + detail
}

// cnote is the setup wizard's secondary text helper (the clientApp one is not
// available yet at this stage).
func cnote(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{})
	label.Wrapping = fyne.TextWrapWord
	return label
}
