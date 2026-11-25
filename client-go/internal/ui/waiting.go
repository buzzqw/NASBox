package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/deletesafety"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/integration"
)

// This file is the "never an error, always a next step" side of the UI:
//   - files NASBox could not handle right now ("waiting files"), each with a
//     plain-language reason, retried by themselves;
//   - deletions a safety guard holds back, with the two real choices:
//     delete them from the NAS too, or bring them back.

// humanIssueReason explains in one sentence why a file is waiting.
func humanIssueReason(issue engine.Issue) string {
	low := strings.ToLower(issue.Reason)
	has := func(tokens ...string) bool {
		for _, token := range tokens {
			if strings.Contains(low, token) {
				return true
			}
		}
		return false
	}
	switch {
	case has("permission denied", "operation not permitted", "access is denied"):
		return lt("Non ho il permesso di leggere o scrivere questo file.", "I do not have permission to read or write this file.")
	case has("instabile", "cambiato durante la lettura", "text file busy", "in use", "being used"):
		return lt("Il file è in uso o sta cambiando: riprovo appena si ferma.", "The file is in use or changing: I retry as soon as it settles.")
	case has("no space left", "disk quota"):
		return lt("Spazio su disco esaurito.", "The disk is full.")
	case has("file name too long", "name too long"):
		return lt("Il nome del file è troppo lungo.", "The file name is too long.")
	case has("esiste gia", "destinazione gia", "already exists"):
		return lt("Sul NAS esiste già un file con questo nome: lo riconcilio da solo.", "The NAS already has a file with this name: I reconcile it myself.")
	case has("sorgente e cambiata", "source changed"):
		return lt("Il file è cambiato sul NAS mentre lo elaboravo: ricontrollo.", "The file changed on the NAS while I was handling it: checking again.")
	case has("no such file", "vanished", "non esiste"):
		return lt("Il file è stato spostato o eliminato mentre lo sincronizzavo.", "The file was moved or deleted while syncing.")
	case issue.Phase == engine.PhaseDelete:
		return lt("Eliminato qui ma sul NAS è cambiato nel frattempo: non lo elimino senza una verifica.",
			"Deleted here but it changed on the NAS meanwhile: I will not delete it without a check.")
	case issue.Phase == engine.PhasePlan:
		return lt("Due versioni diverse: serve guardare questo file.", "Two different versions: this file needs a look.")
	}
	return lt("Non sono riuscito a sincronizzarlo adesso: riprovo da solo.", "I could not sync it just now: I will retry by myself.")
}

// issueRetryText is "riprovo tra 2 min" or "riprovo a breve".
func issueRetryText(issue engine.Issue, now time.Time) string {
	wait := issue.NextTry.Sub(now)
	if wait < 45*time.Second {
		return lt("riprovo a breve", "retrying shortly")
	}
	if wait < 90*time.Minute {
		return lt(fmt.Sprintf("riprovo tra %d min", int(wait.Minutes()+0.5)), fmt.Sprintf("retrying in %d min", int(wait.Minutes()+0.5)))
	}
	return lt("riprovo più tardi", "retrying later")
}

// buildWaitingPane is Activity > Waiting: one row per waiting file.
func (c *clientApp) buildWaitingPane() fyne.CanvasObject {
	c.waitingList = newRowList(
		func() int { return len(c.waitingRows) }, 220, 200,
		func(id int, row *listRow) {
			if id >= len(c.waitingRows) {
				return
			}
			item := c.waitingRows[id]
			row.icon.SetResource(theme.NewWarningThemedResource(theme.WarningIcon()))
			row.name.SetText(item.Path + "  —  " + humanIssueReason(item))
			row.meta.SetText(issueRetryText(item, time.Now()))
		},
	)
	retry := plainButton(lt("Riprova ora", "Retry now"), theme.ViewRefreshIcon(),
		lt("Riprova subito tutti i file in attesa", "Retry every waiting file right away"),
		widget.HighImportance, func() {
			c.runner.RetryWaiting()
			c.appendAction(lt("Nuovo tentativo avviato per i file in attesa", "Retrying the waiting files"))
		}, c.setHint)
	c.waitingEmpty = widget.NewLabel(lt("Nessun file in attesa: tutto quello che si può sincronizzare è sincronizzato.",
		"No waiting files: everything that can be synced is synced."))
	c.waitingEmpty.Importance = widget.LowImportance
	c.waitingEmpty.Wrapping = fyne.TextWrapWord
	header := container.NewVBox(
		c.sectionTitle("", lt("File che non è stato possibile sincronizzare subito. Non fermano gli altri: li riprovo da solo con un'attesa crescente.",
			"Files that could not be synced right away. They never stop the others: I retry them myself with a growing delay.")),
		container.NewHBox(retry),
		c.waitingEmpty,
	)
	return container.NewBorder(header, nil, nil, nil, c.waitingList)
}

// buildHeldCards creates the two Home cards (waiting files, held deletions).
// They live in a box whose content is swapped, so hidden cards take no space.
func (c *clientApp) buildHeldCards() fyne.CanvasObject {
	c.heldBox = container.NewVBox()

	c.waitingTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.waitingTitle.Wrapping = fyne.TextWrapWord
	waitingNote := c.note(lt("Gli altri file continuano a sincronizzarsi. Questi li riprovo da solo.",
		"The other files keep syncing. I retry these by myself."))
	waitingNote.Importance = widget.LowImportance
	c.waitingCard = c.surface(container.NewVBox(
		c.waitingTitle, waitingNote,
		container.NewHBox(
			plainButton(lt("Vedi elenco", "See list"), theme.ListIcon(), lt("Apri l'elenco dei file in attesa", "Open the list of waiting files"),
				widget.MediumImportance, func() { c.navigate(sectionActivity, 2) }, c.setHint),
			plainButton(lt("Riprova ora", "Retry now"), theme.ViewRefreshIcon(), lt("Riprova subito", "Retry right away"),
				widget.LowImportance, func() { c.runner.RetryWaiting() }, c.setHint),
		),
	), theme.ColorNameWarning)

	c.deletesTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.deletesTitle.Wrapping = fyne.TextWrapWord
	c.deletesDetail = widget.NewLabel("")
	c.deletesDetail.Wrapping = fyne.TextWrapWord
	deletesNote := c.note(lt("Se li hai eliminati apposta, eliminali anche dal NAS: restano nello storico e puoi recuperarli. Se non volevi, riportali qui.",
		"If you deleted them on purpose, delete them from the NAS too: they stay in the history and can be recovered. If not, bring them back here."))
	deletesNote.Importance = widget.LowImportance
	c.deletesCard = c.surface(container.NewVBox(
		c.deletesTitle, c.deletesDetail, deletesNote,
		container.NewHBox(
			plainButton(lt("Elimina anche dal NAS", "Delete from the NAS too"), theme.DeleteIcon(),
				lt("Conferma: questi file vengono eliminati anche dal NAS (copia nello storico)", "Confirm: these files are deleted from the NAS too (a copy stays in the history)"),
				widget.DangerImportance, c.onDeleteHeld, c.setHint),
			plainButton(lt("Ripristina qui", "Bring back here"), theme.ContentUndoIcon(),
				lt("Riscarica questi file dal NAS in questo PC", "Download these files from the NAS to this PC again"),
				widget.HighImportance, c.onRestoreHeld, c.setHint),
		),
	), theme.ColorNameError)
	c.refreshHeld()
	return c.heldBox
}

// refreshHeld updates both cards and the waiting list from the engine.
func (c *clientApp) refreshHeld() {
	if c.heldBox == nil && c.waitingList == nil {
		return
	}
	issues := engine.Issues.Snapshot()
	held := engine.PendingDeletes.Paths()
	if c.heldBox != nil {
		objects := []fyne.CanvasObject{}
		if len(held) > 0 {
			c.deletesTitle.SetText(lt(
				fmt.Sprintf("%s file eliminati da questo PC sono ancora sul NAS", formatCount(len(held))),
				fmt.Sprintf("%s files deleted from this PC are still on the NAS", formatCount(len(held)))))
			lines := []string{}
			for _, folder := range engine.PendingDeletes.Folders(4) {
				lines = append(lines, fmt.Sprintf("• %s — %s", folder.Folder, lt(formatCount(folder.Count)+" file", formatCount(folder.Count)+" files")))
			}
			c.deletesDetail.SetText(strings.Join(lines, "\n"))
			objects = append(objects, c.deletesCard)
		}
		if len(issues) > 0 {
			c.waitingTitle.SetText(lt(
				fmt.Sprintf("%s file in attesa di un nuovo tentativo", formatCount(len(issues))),
				fmt.Sprintf("%s files waiting to be retried", formatCount(len(issues)))))
			objects = append(objects, c.waitingCard)
		}
		if len(c.heldBox.Objects) != len(objects) || !sameObjects(c.heldBox.Objects, objects) {
			c.heldBox.Objects = objects
		}
		c.heldBox.Refresh()
		if c.homeBox != nil {
			c.homeBox.Refresh()
		}
	}
	c.markWaitingFiles(issues)
	c.waitingRows = issues
	if c.waitingList != nil {
		c.waitingList.Refresh()
	}
	if c.waitingEmpty != nil {
		if len(issues) == 0 {
			c.waitingEmpty.Show()
		} else {
			c.waitingEmpty.Hide()
		}
	}
	if c.navs != nil {
		if nav := c.navs[sectionActivity]; nav != nil && len(issues) > 0 {
			nav.setBadge(itoa(len(issues)))
		}
	}
}

func sameObjects(a, b []fyne.CanvasObject) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func (c *clientApp) onDeleteHeld() {
	count := len(engine.PendingDeletes.Paths())
	dialog.ShowConfirm(
		lt("Eliminare anche dal NAS?", "Delete from the NAS too?"),
		lt(fmt.Sprintf("%s file verranno eliminati dal NAS. Una copia resta nello storico e si può recuperare.", formatCount(count)),
			fmt.Sprintf("%s files will be deleted from the NAS. A copy stays in the history and can be recovered.", formatCount(count))),
		func(ok bool) {
			if !ok {
				return
			}
			if _, err := deletesafety.Shared().Confirm(c.cfg); err != nil {
				c.appendLog(tr("ui.confirm.mass_delete_failed", map[string]any{"detail": err.Error()}))
				return
			}
			c.appendAction(lt("Eliminazione dal NAS confermata", "Deletion from the NAS confirmed"))
			c.runner.SyncNowForce()
		}, c.window)
}

func (c *clientApp) onRestoreHeld() {
	count := len(engine.PendingDeletes.Paths())
	dialog.ShowConfirm(
		lt("Ripristinare i file in questo PC?", "Bring the files back to this PC?"),
		lt(fmt.Sprintf("%s file verranno scaricati di nuovo dal NAS in questo PC.", formatCount(count)),
			fmt.Sprintf("%s files will be downloaded again from the NAS to this PC.", formatCount(count))),
		func(ok bool) {
			if !ok {
				return
			}
			restored, err := c.runner.RestoreHeldDeletions()
			if err != nil {
				c.appendLog(err.Error())
				return
			}
			c.appendAction(lt(fmt.Sprintf("Ripristino di %s file avviato", formatCount(restored)),
				fmt.Sprintf("Restoring %s files", formatCount(restored))))
			c.refreshHeld()
		}, c.window)
}

// markWaitingFiles puts the "problem" emblem on the files that are waiting, in
// the file manager, and takes it off when they go through: the per-file
// status Dropbox shows, limited to the few files that need attention (marking
// every synced file would cost a process per file).
func (c *clientApp) markWaitingFiles(issues []engine.Issue) {
	if !c.emblemsEnabled() || c.cfg.LocalRoot() == "" {
		return
	}
	now := map[string]bool{}
	for _, issue := range issues {
		if len(now) >= 100 {
			break
		}
		if full, ok := c.localFilePath(issue.Path); ok {
			if _, err := os.Stat(full); err == nil {
				now[full] = true
			}
		}
	}
	c.emblemMu.Lock()
	previous := c.markedFiles
	c.markedFiles = now
	c.emblemMu.Unlock()
	go func() {
		for path := range now {
			if !previous[path] {
				integration.SetEmblem(path, integration.EmblemProblem)
			}
		}
		for path := range previous {
			if !now[path] {
				integration.SetEmblem(path, "")
			}
		}
	}()
}

// watchScanProgress shows what the first (or a full) scan is doing in the
// status banner, once a second while it runs and takes longer than a moment.
func (c *clientApp) watchScanProgress(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	shown := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := engine.Scan.Snapshot()
			if !snap.Active || snap.Visited < 2000 {
				if shown {
					shown = false
					fyne.Do(func() {
						if c.level == statusProgress && c.bannerReason != nil {
							c.bannerReason.SetText(tr("status.sync_state_active", map[string]any{"direction": tr("ui.direction.sync", nil)}))
						}
					})
				}
				continue
			}
			shown = true
			text := lt(
				fmt.Sprintf("Analizzo i file di questo PC: %s controllati", formatCount(int(snap.Visited))),
				fmt.Sprintf("Scanning this PC's files: %s checked", formatCount(int(snap.Visited))))
			if snap.Hashed > 0 {
				text += lt(fmt.Sprintf(" · %s letti (%s)", formatCount(int(snap.Hashed)), humanBytes(snap.Bytes)),
					fmt.Sprintf(" · %s read (%s)", formatCount(int(snap.Hashed)), humanBytes(snap.Bytes)))
			}
			fyne.Do(func() {
				if c.level == statusProgress && c.bannerReason != nil {
					c.bannerReason.SetText(text)
				}
			})
		}
	}
}
