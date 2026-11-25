package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/config"
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
					status.SetText(lt("Non riesco a collegarmi. ", "I cannot connect. ") + humanSetupError(err.Error()))
					advanced.Open(0)
					return
				}
				status.SetText("")
				showStep(3)
			})
		}()
	}

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
			setStep(1, lt("Dove tieni i file", "Where your files live"),
				cnote(lt("Scegli la cartella del tuo PC che NASBox terrà allineata con il NAS. Se non esiste la creo io.",
					"Pick the folder on this PC that NASBox keeps in line with the NAS. I create it if it does not exist.")),
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
				field(lt("Indirizzo del NAS (nome o IP)", "NAS address (name or IP)"), hostEntry),
				field(lt("Utente", "User"), userEntry),
				advanced,
				container.NewHBox(
					plainButton(lt("Indietro", "Back"), theme.NavigateBackIcon(), lt("Torna alla scelta della cartella", "Back to the folder choice"),
						widget.LowImportance, func() { status.SetText(""); showStep(1) }, func(string) {}),
					connect,
				),
			)
		default:
			setStep(3, lt("Tutto pronto", "All set"),
				cnote(lt(fmt.Sprintf("Collegato a %s. I file di %s verranno sincronizzati con il NAS: i nuovi si caricano, quelli degli altri PC si scaricano.",
					cfg.String("nas_lan"), strings.TrimSpace(localEntry.Text)),
					fmt.Sprintf("Connected to %s. The files in %s will be synced with the NAS: new ones upload, other PCs' files download.",
						cfg.String("nas_lan"), strings.TrimSpace(localEntry.Text)))),
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
