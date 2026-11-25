//go:build debugsize

package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// widestChain follows, from obj, the child that has the largest minimum
// width: the last entries are what forces a page (and the window) to be wide.
func widestChain(obj fyne.CanvasObject, depth int) []string {
	line := fmt.Sprintf("%s%T min=%.0fx%.0f size=%.0fx%.0f", strings.Repeat(" ", depth), obj, obj.MinSize().Width, obj.MinSize().Height, obj.Size().Width, obj.Size().Height)
	if label, ok := obj.(interface{ Text() string }); ok {
		line += " text=" + label.Text()
	}
	var children []fyne.CanvasObject
	switch value := obj.(type) {
	case *fyne.Container:
		children = value.Objects
	case fyne.Widget:
		func() {
			defer func() { _ = recover() }()
			children = test.WidgetRenderer(value).Objects()
		}()
	}
	var widest fyne.CanvasObject
	for _, child := range children {
		if child == nil || !child.Visible() {
			continue
		}
		if widest == nil || child.MinSize().Width > widest.MinSize().Width {
			widest = child
		}
	}
	out := []string{line}
	if widest != nil && depth < 40 {
		out = append(out, widestChain(widest, depth+1)...)
	}
	return out
}

// debugMinSize writes the widest chain of every section to stderr when
// NASBOX_UI_DEBUG_MINSIZE is set (support tool for "the window is too wide").
func (c *clientApp) debugMinSize() {
	if os.Getenv("NASBOX_UI_DEBUG_MINSIZE") == "" {
		return
	}
	go func() {
		time.Sleep(4 * time.Second)
		fyne.Do(func() {
			for name, id := range map[string]sectionID{"status": sectionStatus, "activity": sectionActivity} {
				fmt.Fprintf(os.Stderr, "== %s\n%s\n", name, strings.Join(widestChain(c.views[id], 0), "\n"))
			}
		})
	}()
}
