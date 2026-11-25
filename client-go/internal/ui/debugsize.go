//go:build debugsize

package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"nasbox-go/internal/engine"
	"nasbox-go/internal/transfer"
)

// injectBusyState puts the app in the state a busy real client is in (live
// transfer with a long path, held deletions, waiting files, a long status
// message), because a freshly started, offline client is never that wide.
func (c *clientApp) injectBusyState() {
	long := "hw2304847/sistema/etc/systemd/system/dbus-org.freedesktop.nm-dispatcher.service-con-un-nome-molto-lungo-davvero.conf"
	held := []string{}
	for index := 0; index < 2034; index++ {
		held = append(held, fmt.Sprintf("SADE/SADE/images/img%04d.png", index))
	}
	engine.PendingDeletes.Set(held)
	engine.Issues.BeginPass()
	engine.Issues.Report(long, engine.PhaseRead, "file instabile durante la lettura, sync interrotto: "+long)
	c.applyStatusUI(statusBlocked, "Ho trattenuto 2.034 cancellazioni: serve una tua scelta. Tutto il resto continua a sincronizzarsi. "+long)
	c.renderProgress(transfer.Progress{File: long, Percent: 53, Bytes: 38000000000, TotalBytes: 71000000000,
		Rate: "16.5 MB/s", ETA: "16:14", Done: 10301, Total: 19445})
	c.refreshHeld()
}

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
			if os.Getenv("NASBOX_UI_DEBUG_INJECT") != "" {
				c.injectBusyState()
			}
			for name, id := range map[string]sectionID{"status": sectionStatus, "activity": sectionActivity} {
				fmt.Fprintf(os.Stderr, "== %s\n%s\n", name, strings.Join(widestChain(c.views[id], 0), "\n"))
			}
		})
	}()
}
