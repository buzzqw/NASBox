package ui

import (
	"path"
	"sort"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/selective"
)

// selectiveTree shows the NAS folder tree (not the local one: an excluded
// folder is not on this PC, and it must stay visible to be included again).
// Children are loaded lazily, one level at a time; sizes come from du on the
// NAS, on demand.
type selectiveTree struct {
	app      *clientApp
	tree     *widget.Tree
	status   *widget.Label
	mu       sync.Mutex
	children map[string][]string // "" = root
	loading  map[string]bool
	sizes    map[string]int64
}

func (c *clientApp) buildSelectiveTree() fyne.CanvasObject {
	s := &selectiveTree{app: c, children: map[string][]string{}, loading: map[string]bool{}, sizes: map[string]int64{}}
	s.status = widget.NewLabel(tr("ui.selective.loading", nil))
	s.status.Importance = widget.LowImportance
	s.status.Wrapping = fyne.TextWrapWord
	s.tree = widget.NewTree(
		func(uid widget.TreeNodeID) []widget.TreeNodeID { return s.childIDs(uid) },
		func(uid widget.TreeNodeID) bool { return true },
		func(branch bool) fyne.CanvasObject {
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			size := widget.NewLabel("")
			size.Importance = widget.LowImportance
			return container.NewBorder(nil, nil, widget.NewIcon(theme.CheckButtonCheckedIcon()), size, name)
		},
		func(uid widget.TreeNodeID, branch bool, object fyne.CanvasObject) { s.render(uid, object) },
	)
	s.tree.OnSelected = func(uid widget.TreeNodeID) {
		s.toggle(uid)
		s.tree.UnselectAll()
	}
	s.tree.OnBranchOpened = func(uid widget.TreeNodeID) { go s.loadSizes(uid) }
	sizes := widget.NewButtonWithIcon(tr("ui.selective.sizes", nil), theme.StorageIcon(), func() { go s.loadSizes("") })
	sizes.Importance = widget.LowImportance
	reload := widget.NewButtonWithIcon(tr("browse.refresh_btn", nil), theme.ViewRefreshIcon(), func() {
		s.mu.Lock()
		s.children = map[string][]string{}
		s.loading = map[string]bool{}
		s.mu.Unlock()
		s.tree.Refresh()
	})
	reload.Importance = widget.LowImportance
	holder := container.NewGridWrap(fyne.NewSize(1, 260), s.tree)
	return container.NewBorder(container.NewBorder(nil, nil, nil, container.NewHBox(sizes, reload), s.status), nil, nil, nil, holder)
}

func (s *selectiveTree) childIDs(uid string) []string {
	s.mu.Lock()
	children, ok := s.children[uid]
	loading := s.loading[uid]
	if !ok && !loading {
		s.loading[uid] = true
	}
	s.mu.Unlock()
	if !ok && !loading {
		go s.load(uid)
	}
	return children
}

func (s *selectiveTree) load(uid string) {
	// This runs in its own goroutine and ends with a UI update; if the window
	// is already gone (tests, shutdown) that update must not crash the process.
	defer func() { _ = recover() }()
	entries, err := s.app.client.BrowseList(uid)
	dirs := []string{}
	if err == nil {
		for _, entry := range entries {
			if entry.Kind != "DIR" || strings.HasPrefix(entry.Name, ".") {
				continue
			}
			name := strings.TrimSuffix(entry.Name, "/")
			if uid == "" {
				dirs = append(dirs, name)
			} else {
				dirs = append(dirs, uid+"/"+name)
			}
		}
		sort.Strings(dirs)
	}
	s.mu.Lock()
	s.children[uid] = dirs
	s.loading[uid] = false
	s.mu.Unlock()
	fyne.Do(func() {
		if err != nil {
			s.status.SetText(tr("ui.selective.error", map[string]any{"detail": firstLine(err.Error())}))
		} else if uid == "" {
			s.status.SetText(tr("ui.selective.hint", map[string]any{"count": len(selective.ExcludedPaths(s.app.cfg))}))
		}
		s.tree.Refresh()
	})
}

func (s *selectiveTree) loadSizes(uid string) {
	fyne.Do(func() { s.status.SetText(tr("ui.selective.sizing", nil)) })
	sizes, err := s.app.client.DirSizes(uid)
	s.mu.Lock()
	for name, size := range sizes {
		key := name
		if uid != "" {
			key = uid + "/" + name
		}
		s.sizes[key] = size
	}
	s.mu.Unlock()
	fyne.Do(func() {
		if err != nil {
			s.status.SetText(tr("ui.selective.error", map[string]any{"detail": firstLine(err.Error())}))
		} else {
			s.status.SetText(tr("ui.selective.hint", map[string]any{"count": len(selective.ExcludedPaths(s.app.cfg))}))
		}
		s.tree.Refresh()
	})
}

func (s *selectiveTree) render(uid string, object fyne.CanvasObject) {
	border := object.(*fyne.Container)
	var name, size *widget.Label
	var icon *widget.Icon
	for _, child := range border.Objects {
		switch value := child.(type) {
		case *widget.Icon:
			icon = value
		case *widget.Label:
			if name == nil && value.Importance != widget.LowImportance {
				name = value
			} else {
				size = value
			}
		}
	}
	if name == nil || size == nil || icon == nil {
		return
	}
	name.SetText(path.Base(uid))
	cfg := s.app.cfg
	switch {
	case selective.Excluded(cfg, uid):
		icon.SetResource(theme.CheckButtonIcon())
	case s.hasExcludedDescendant(uid):
		icon.SetResource(theme.CheckButtonFillIcon())
	default:
		icon.SetResource(theme.CheckButtonCheckedIcon())
	}
	s.mu.Lock()
	bytes, known := s.sizes[uid]
	s.mu.Unlock()
	if known {
		size.SetText(humanBytes(bytes))
	} else {
		size.SetText("")
	}
}

func (s *selectiveTree) hasExcludedDescendant(uid string) bool {
	for _, excluded := range selective.ExcludedPaths(s.app.cfg) {
		if strings.HasPrefix(excluded, uid+"/") {
			return true
		}
	}
	return false
}

// toggle includes/excludes a folder. Excluding a parent replaces any
// exclusions below it; including a child of an excluded parent is refused
// with an explanation (the parent decides).
func (s *selectiveTree) toggle(uid string) {
	cfg := s.app.cfg
	current := selective.ExcludedPaths(cfg)
	next := []string{}
	if selective.Excluded(cfg, uid) {
		if !containsString(current, uid) {
			s.status.SetText(tr("ui.selective.parent_excluded", nil))
			return
		}
		for _, item := range current {
			if item != uid {
				next = append(next, item)
			}
		}
		s.app.appendAction(tr("ui.selective.included", map[string]any{"path": uid}))
	} else {
		for _, item := range current {
			if !strings.HasPrefix(item, uid+"/") {
				next = append(next, item)
			}
		}
		next = append(next, uid)
		s.app.appendAction(tr("ui.selective.excluded", map[string]any{"path": uid}))
	}
	selective.SetExcluded(cfg, next)
	s.status.SetText(tr("ui.selective.hint", map[string]any{"count": len(next)}))
	s.tree.Refresh()
	s.app.runner.SyncNow()
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// firstLine keeps an error readable in one line (ssh can print its whole
// usage text on a bad configuration).
func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 140 {
		line = line[:140] + "…"
	}
	return line
}
