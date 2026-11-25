package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"nasbox-go/internal/diagnostics"
	"nasbox-go/internal/i18n"
)

// logRow is one line of the Log tab as the user reads it: an icon for the
// level or the action, a complete sentence and a short time. The raw events
// ("DOWNLOAD  CLAUDE.md") stay in the files; this is only the presentation.
type logRow struct {
	Icon fyne.Resource
	Text string
	When string
}

// noisyActions are diagnostics, not things that happened to the user's
// files: they are hidden by the default "Main" filter.
var noisyActions = map[string]bool{
	"PERF": true, "DOCTOR": true, "VALID": true, "AFTER_CLOSE": true,
}

// groupableActions are folded when they repeat in the same folder: a bulk
// download of 19.445 files is one line, not 19.445.
var groupableActions = map[string]bool{
	"DOWNLOAD": true, "UPLOAD": true, "DELETE_LOCAL": true, "DELETE_REMOTE": true,
}

func lt(it, en string) string {
	if strings.EqualFold(i18n.Language, "en") {
		return en
	}
	return it
}

func dropNoise(entries []diagnostics.Entry) []diagnostics.Entry {
	kept := make([]diagnostics.Entry, 0, len(entries))
	for _, entry := range entries {
		if !noisyActions[strings.ToUpper(entry.Action)] {
			kept = append(kept, entry)
		}
	}
	return kept
}

// logWhen is "11:31:16" today, "ieri 11:31" yesterday, "25/11 11:31" before.
func logWhen(at, now time.Time) string {
	at = at.Local()
	now = now.Local()
	switch {
	case sameDay(at, now):
		return at.Format("15:04:05")
	case sameDay(at, now.AddDate(0, 0, -1)):
		return lt("ieri ", "yesterday ") + at.Format("15:04")
	}
	return at.Format("02/01 15:04")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// buildLogRows turns events (newest first) into readable rows, folding runs
// of the same transfer action in the same folder.
func buildLogRows(entries []diagnostics.Entry, now time.Time) []logRow {
	rows := make([]logRow, 0, len(entries))
	for index := 0; index < len(entries); {
		entry := entries[index]
		action := strings.ToUpper(entry.Action)
		if groupableActions[action] && entry.Path != "" && entry.Path != "-" {
			_, folder := splitPath(entry.Path)
			end := index + 1
			for end < len(entries) && strings.ToUpper(entries[end].Action) == action {
				if _, other := splitPath(entries[end].Path); other != folder {
					break
				}
				end++
			}
			if count := end - index; count > 1 {
				oldest := entries[end-1].Timestamp
				when := logWhen(entry.Timestamp, now)
				if span := entry.Timestamp.Sub(oldest); span >= time.Minute {
					when = oldest.Local().Format("15:04") + "–" + entry.Timestamp.Local().Format("15:04")
				}
				rows = append(rows, logRow{
					Icon: logIcon(entry, ""),
					Text: groupSentence(action, count, folder),
					When: when,
				})
				index = end
				continue
			}
		}
		rows = append(rows, logRow{
			Icon: logIcon(entry, ""),
			Text: humanLogText(entry),
			When: logWhen(entry.Timestamp, now),
		})
		index++
	}
	return rows
}

func groupSentence(action string, count int, folder string) string {
	n := formatCount(count)
	switch action {
	case "DOWNLOAD":
		return lt(n+" file scaricati in "+folder, n+" files downloaded in "+folder)
	case "UPLOAD":
		return lt(n+" file caricati da "+folder, n+" files uploaded from "+folder)
	case "DELETE_REMOTE":
		return lt(n+" file eliminati dal NAS in "+folder, n+" files deleted from the NAS in "+folder)
	default:
		return lt(n+" file eliminati da questo PC in "+folder, n+" files deleted from this PC in "+folder)
	}
}

// logIcon colours problems (red/amber) and shows the direction for transfers.
func logIcon(entry diagnostics.Entry, _ string) fyne.Resource {
	switch strings.ToUpper(entry.Level) {
	case "ERROR":
		return theme.NewErrorThemedResource(theme.ErrorIcon())
	case "WARN":
		return theme.NewWarningThemedResource(theme.WarningIcon())
	}
	switch strings.ToUpper(entry.Action) {
	case "DOWNLOAD":
		return theme.DownloadIcon()
	case "UPLOAD":
		return theme.UploadIcon()
	case "DELETE_LOCAL", "DELETE_REMOTE":
		return theme.DeleteIcon()
	case "SAFETY_BLOCK":
		return theme.NewWarningThemedResource(theme.WarningIcon())
	case "MIRROR":
		return theme.FolderIcon()
	case "SYNC":
		return theme.ViewRefreshIcon()
	}
	return theme.InfoIcon()
}

// humanLogText is the sentence for one event.
func humanLogText(entry diagnostics.Entry) string {
	path := entry.Path
	if path == "-" {
		path = ""
	}
	detail := strings.Join(strings.Fields(entry.Detail), " ")
	switch strings.ToUpper(entry.Action) {
	case "DOWNLOAD":
		return lt("Scaricato dal NAS: ", "Downloaded from the NAS: ") + path
	case "UPLOAD":
		return lt("Caricato sul NAS: ", "Uploaded to the NAS: ") + path
	case "DELETE_REMOTE":
		return lt("Eliminato dal NAS (copia nello storico): ", "Deleted from the NAS (kept in history): ") + path
	case "DELETE_LOCAL":
		return lt("Eliminato da questo PC (copia nel cestino): ", "Deleted from this PC (kept in trash): ") + path
	case "CONFLICT", "CONFLICT_AUTO":
		return lt("Conflitto, tenute entrambe le versioni: ", "Conflict, both versions kept: ") + path
	case "MIRROR":
		if path != "" && detail != "" {
			return lt("Cartella speciale aggiornata: ", "Mirrored folder updated: ") + path + " — " + detail
		}
		return lt("Cartella speciale aggiornata", "Mirrored folder updated")
	case "SAFETY_BLOCK":
		return lt("Protezione attivata: ", "Safety guard: ") + detail
	case "SYNC":
		return detail
	case "DOCTOR":
		return lt("Controllo del sistema eseguito", "System check run")
	case "PERF":
		return lt("Prestazioni: ", "Performance: ") + detail
	case "ERROR":
		return humanError(detail)
	case "UI", "INFO":
		if detail != "" {
			return detail
		}
	}
	text := strings.ReplaceAll(strings.Title(strings.ToLower(strings.ReplaceAll(entry.Action, "_", " "))), "  ", " ") //nolint:staticcheck
	if path != "" {
		text += ": " + path
	}
	if detail != "" {
		text += " — " + detail
	}
	return text
}

// humanError rewrites the terse technical errors into something a person can
// act on. The original text is always kept at the end of the line.
func humanError(detail string) string {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "exit status 255"), strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no route to host"), strings.Contains(lower, "could not resolve"),
		strings.Contains(lower, "timed out"):
		return lt("Il NAS non risponde: controlla rete e cavo. Riprovo da solo. (", "The NAS is not answering: check the network. Retrying. (") + detail + ")"
	case strings.Contains(lower, "ssh: exit status 1"):
		return lt("Il NAS ha rifiutato un'operazione senza spiegare perché. Dettaglio in client.log. (",
			"The NAS refused an operation without saying why. See client.log. (") + detail + ")"
	case strings.Contains(lower, "tick failed"):
		return lt("Errore interno di una versione precedente del client. (", "Internal error from an earlier client version. (") + detail + ")"
	case strings.Contains(lower, "rifiutato lo spostamento"):
		return detail
	case strings.Contains(lower, "host key"), strings.Contains(lower, "known_hosts"):
		return lt("La chiave del NAS è cambiata o non è fidata: verifica prima di continuare. (", "The NAS host key changed or is untrusted: verify before continuing. (") + detail + ")"
	}
	if detail == "" {
		return lt("Errore", "Error")
	}
	return lt("Errore: ", "Error: ") + detail
}

// logCountText is "123 di 3.000 voci".
func logCountText(shown, total int) string {
	return fmt.Sprintf("%s / %s", formatCount(shown), formatCount(total))
}
