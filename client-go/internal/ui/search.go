package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/state"
)

// Search is the everyday tool of a cloud folder: type a few letters, find the
// file wherever it is, open it. It searches the list of tracked files kept in
// memory, so results appear while typing, without asking the NAS anything.

type searchHit struct {
	Path  string
	Size  int64
	MTime int64 // ns
	score int
}

var accentFold = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a", "è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ò", "o", "ó", "o", "ô", "o", "ö", "o", "õ", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ç", "c", "ñ", "n",
)

// normalizeSearch lowercases and folds accents, so "perche" finds "Perché".
func normalizeSearch(text string) string {
	return accentFold.Replace(strings.ToLower(text))
}

// searchEntries returns up to limit tracked files whose path contains every
// word of query. Matches in the file name rank above matches in a folder name;
// ties go to the most recently modified file.
func searchEntries(entries map[string]state.EntryState, query string, limit int) []searchHit {
	words := strings.Fields(normalizeSearch(query))
	if len(words) == 0 {
		return nil
	}
	hits := make([]searchHit, 0, 64)
	for rel, entry := range entries {
		if entry.Digest == "" {
			continue // deleted: only a tombstone is left
		}
		full := normalizeSearch(rel)
		name := path.Base(full)
		score := 0
		matched := true
		for _, word := range words {
			switch {
			case strings.HasPrefix(name, word):
				score += 6
			case strings.Contains(name, word):
				score += 4
			case strings.Contains(full, word):
				score++
			default:
				matched = false
			}
			if !matched {
				break
			}
		}
		if !matched {
			continue
		}
		if name == strings.Join(words, " ") {
			score += 10
		}
		hits = append(hits, searchHit{Path: rel, Size: entry.Size, MTime: entry.MTimeNS, score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].MTime != hits[j].MTime {
			return hits[i].MTime > hits[j].MTime
		}
		return hits[i].Path < hits[j].Path
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// searchIndex caches the tracked-file list; it is reloaded when older than
// searchIndexTTL so a search sees files synced a minute ago.
type searchIndex struct {
	mu      sync.Mutex
	entries map[string]state.EntryState
	at      time.Time
	loading bool
}

const searchIndexTTL = 45 * time.Second

func (c *clientApp) currentSearchIndex() (map[string]state.EntryState, int) {
	c.searchIdx.mu.Lock()
	defer c.searchIdx.mu.Unlock()
	stale := c.searchIdx.entries == nil || time.Since(c.searchIdx.at) > searchIndexTTL
	if stale && !c.searchIdx.loading && c.store != nil {
		c.searchIdx.loading = true
		go func() {
			entries, err := c.store.EntrySnapshot(c.cfg.LocalRepositoryID())
			c.searchIdx.mu.Lock()
			c.searchIdx.loading = false
			if err == nil {
				c.searchIdx.entries = entries
				c.searchIdx.at = time.Now()
			}
			c.searchIdx.mu.Unlock()
			fyne.Do(c.runSearch)
		}()
	}
	return c.searchIdx.entries, len(c.searchIdx.entries)
}

// localFilePath resolves a relative synced path inside the local folder,
// refusing anything that would leave it.
func (c *clientApp) localFilePath(relative string) (string, bool) {
	root := c.cfg.LocalRoot()
	clean := path.Clean("/" + filepath.ToSlash(relative))
	if root == "" || clean == "/" {
		return "", false
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), true
}

// openFile opens a synced file with the desktop's default application.
func (c *clientApp) openFile(relative string) {
	full, ok := c.localFilePath(relative)
	if !ok {
		return
	}
	if _, err := os.Stat(full); err != nil {
		dialog.ShowInformation(lt("File non presente", "File not here"),
			lt("Questo file non è su questo PC (sincronizzazione selettiva o ancora da scaricare). Lo trovi in Sfoglia NAS.",
				"This file is not on this PC (selective sync or not downloaded yet). Find it in Browse NAS."), c.window)
		return
	}
	_ = exec.Command("xdg-open", full).Start()
}

func (c *clientApp) buildSearchPane() fyne.CanvasObject {
	c.searchEntry = widget.NewEntry()
	c.searchEntry.SetPlaceHolder(lt("Cerca un file per nome o cartella…", "Search a file by name or folder…"))
	c.searchEntry.ActionItem = widget.NewIcon(theme.SearchIcon())
	c.searchEntry.OnChanged = func(string) {
		if c.searchTimer != nil {
			c.searchTimer.Stop()
		}
		c.searchTimer = time.AfterFunc(160*time.Millisecond, func() { fyne.Do(c.runSearch) })
	}
	c.searchInfo = widget.NewLabel("")
	c.searchInfo.Importance = widget.LowImportance
	c.searchInfo.Wrapping = fyne.TextWrapWord
	c.searchList = newRowList(
		func() int { return len(c.searchHits) }, 220, 170,
		func(id int, row *listRow) {
			if id >= len(c.searchHits) {
				return
			}
			hit := c.searchHits[id]
			name, folder := splitPath(hit.Path)
			row.icon.SetResource(theme.FileIcon())
			row.name.SetText(name + "  —  " + folder)
			meta := humanBytes(hit.Size)
			if hit.MTime > 0 {
				meta += " · " + time.Unix(0, hit.MTime).Format("02/01/2006")
			}
			row.meta.SetText(meta)
		},
	)
	c.searchList.OnSelected = func(id widget.ListItemID) {
		if int(id) < len(c.searchHits) {
			c.searchSelected = c.searchHits[id].Path
		}
		c.updateSearchButtons()
	}
	selected := func(do func(string)) func() {
		return func() {
			if c.searchSelected != "" {
				do(c.searchSelected)
			}
		}
	}
	c.searchOpenBtn = plainButton(lt("Apri", "Open"), theme.FileIcon(), lt("Apri il file con il programma predefinito", "Open the file with the default application"),
		widget.HighImportance, selected(c.openFile), c.setHint)
	c.searchFolderBtn = plainButton(lt("Mostra cartella", "Show folder"), theme.FolderOpenIcon(), lt("Apri la cartella che contiene il file", "Open the folder containing the file"),
		widget.MediumImportance, selected(c.openContainingFolder), c.setHint)
	c.searchVersionsBtn = plainButton(lt("Versioni", "Versions"), theme.HistoryIcon(), lt("Vedi e ripristina le versioni precedenti del file", "See and restore earlier versions of the file"),
		widget.LowImportance, selected(c.showFileVersions), c.setHint)
	c.searchCopyBtn = plainButton(lt("Copia percorso", "Copy path"), theme.ContentCopyIcon(), lt("Copia il percorso del file negli appunti", "Copy the file path to the clipboard"),
		widget.LowImportance, selected(func(p string) {
			if full, ok := c.localFilePath(p); ok {
				c.window.Clipboard().SetContent(full)
				c.appendAction(lt("Percorso copiato", "Path copied"))
			}
		}), c.setHint)
	c.updateSearchButtons()
	header := container.NewVBox(
		c.sectionTitle("", lt("Cerca tra tutti i file sincronizzati. Premi Ctrl+F da qualsiasi schermata.", "Search all synced files. Press Ctrl+F from any screen.")),
		c.searchEntry,
		c.searchInfo,
		buttonRows(2, c.searchOpenBtn, c.searchFolderBtn, c.searchVersionsBtn, c.searchCopyBtn),
	)
	c.runSearch()
	return container.NewBorder(header, nil, nil, nil, c.searchList)
}

func (c *clientApp) updateSearchButtons() {
	for _, button := range []*tipButton{c.searchOpenBtn, c.searchFolderBtn, c.searchVersionsBtn, c.searchCopyBtn} {
		if button == nil {
			continue
		}
		if c.searchSelected == "" {
			button.Disable()
		} else {
			button.Enable()
		}
	}
}

// runSearch recomputes the result list from the current text.
func (c *clientApp) runSearch() {
	if c.searchEntry == nil {
		return
	}
	entries, total := c.currentSearchIndex()
	query := strings.TrimSpace(c.searchEntry.Text)
	c.searchSelected = ""
	c.updateSearchButtons()
	switch {
	case entries == nil:
		c.searchHits = nil
		c.searchInfo.SetText(lt("Preparo l'elenco dei file…", "Preparing the file list…"))
	case query == "":
		c.searchHits = nil
		c.searchInfo.SetText(lt(fmt.Sprintf("Scrivi per cercare tra %s file.", formatCount(total)),
			fmt.Sprintf("Type to search among %s files.", formatCount(total))))
	default:
		c.searchHits = searchEntries(entries, query, 300)
		switch {
		case len(c.searchHits) == 0:
			c.searchInfo.SetText(lt("Nessun file trovato. Prova con meno parole o controlla l'ortografia.", "No file found. Try fewer words or check the spelling."))
		case len(c.searchHits) == 300:
			c.searchInfo.SetText(lt("Mostro i primi 300 risultati: aggiungi una parola per restringere.", "Showing the first 300 results: add a word to narrow down."))
		default:
			c.searchInfo.SetText(lt(fmt.Sprintf("%s risultati", formatCount(len(c.searchHits))), fmt.Sprintf("%s results", formatCount(len(c.searchHits)))))
		}
	}
	if c.searchList != nil {
		c.searchList.UnselectAll()
		c.searchList.Refresh()
	}
}

// focusSearch jumps to Files › Search with the cursor in the box (Ctrl+F).
func (c *clientApp) focusSearch() {
	c.navigate(sectionFiles, 3)
	if c.window != nil && c.window.Canvas() != nil && c.searchEntry != nil {
		c.window.Canvas().Focus(c.searchEntry)
	}
}

// openLocalFolder opens the NASBox folder in the file manager.
func (c *clientApp) openLocalFolder() {
	if root := c.cfg.LocalRoot(); root != "" {
		_ = exec.Command("xdg-open", root).Start()
	}
}
