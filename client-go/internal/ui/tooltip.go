package ui

import (
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Fyne 2.7 has no tooltip widget and a PopUp overlay would swallow the next
// click. The tooltip is therefore a plain card in a transparent layer stacked
// above the window content: it takes no events, so clicks reach the widget
// underneath, and it is drawn near the pointer after a short delay.

// tipPointer is the last pointer position (canvas coordinates) reported by a
// widget that has a tooltip. Fyne delivers hover events on the UI thread.
var tipPointer fyne.Position

const (
	tipDelay    = 450 * time.Millisecond
	tipLifetime = 6 * time.Second
	tipWrapAt   = 52
)

// wrapTip breaks text on spaces at about `width` characters per line.
func wrapTip(text string, width int) []string {
	lines := []string{}
	for _, paragraph := range strings.Split(strings.TrimSpace(text), "\n") {
		current := ""
		for _, word := range strings.Fields(paragraph) {
			if current != "" && len([]rune(current))+1+len([]rune(word)) > width {
				lines = append(lines, current)
				current = word
				continue
			}
			if current != "" {
				current += " "
			}
			current += word
		}
		lines = append(lines, current)
	}
	return lines
}

// setHintText shows (or, for "", hides) the tooltip for the hovered widget.
func (c *clientApp) setHintText(text string) {
	fyne.Do(func() { c.scheduleTip(text) })
}

func (c *clientApp) scheduleTip(text string) {
	c.tipGen++
	generation := c.tipGen
	if c.tipTimer != nil {
		c.tipTimer.Stop()
	}
	c.hideTip()
	text = strings.TrimSpace(text)
	if text == "" || c.tipLayer == nil {
		return
	}
	position := tipPointer
	c.tipTimer = time.AfterFunc(tipDelay, func() {
		fyne.Do(func() {
			if generation != c.tipGen {
				return
			}
			c.showTip(text, position)
			c.tipTimer = time.AfterFunc(tipLifetime, func() {
				fyne.Do(func() {
					if generation == c.tipGen {
						c.hideTip()
					}
				})
			})
		})
	})
}

func (c *clientApp) hideTip() {
	if c.tipLayer == nil || len(c.tipLayer.Objects) == 0 {
		return
	}
	c.tipLayer.Objects = nil
	c.tipLayer.Refresh()
}

func (c *clientApp) showTip(text string, at fyne.Position) {
	bg := canvas.NewRectangle(c.tint(theme.ColorNameForeground))
	bg.CornerRadius = 6
	lines := container.New(layout.NewCustomPaddedVBoxLayout(0))
	for _, line := range wrapTip(text, tipWrapAt) {
		label := canvas.NewText(line, c.tint(theme.ColorNameBackground))
		label.TextSize = 12
		lines.Add(label)
	}
	card := container.NewStack(bg, container.NewPadded(lines))
	size := card.MinSize()
	card.Resize(size)
	position := fyne.NewPos(at.X+12, at.Y+22)
	if c.window != nil && c.window.Canvas() != nil {
		canvasSize := c.window.Canvas().Size()
		if position.X+size.Width > canvasSize.Width-6 {
			position.X = canvasSize.Width - size.Width - 6
		}
		if position.Y+size.Height > canvasSize.Height-6 {
			position.Y = at.Y - size.Height - 8
		}
	}
	if position.X < 4 {
		position.X = 4
	}
	if position.Y < 4 {
		position.Y = 4
	}
	card.Move(position)
	c.tipLayer.Objects = []fyne.CanvasObject{card}
	c.tipLayer.Refresh()
}

// tipButton is a button with a tooltip. widget.Button itself has no hover
// hook, so it is extended like hintButton; it can be used wherever a
// *widget.Button was (SetText, Enable, Disable...).
type tipButton struct {
	widget.Button
	hint    string
	setHint func(string)
}

func (b *tipButton) MouseIn(event *desktop.MouseEvent) {
	b.Button.MouseIn(event)
	tipPointer = event.AbsolutePosition
	if b.setHint != nil {
		b.setHint(b.hint)
	}
}

func (b *tipButton) MouseMoved(event *desktop.MouseEvent) {
	b.Button.MouseMoved(event)
	tipPointer = event.AbsolutePosition
}

func (b *tipButton) MouseOut() {
	b.Button.MouseOut()
	if b.setHint != nil {
		b.setHint("")
	}
}

func (b *tipButton) Tapped(event *fyne.PointEvent) {
	if b.setHint != nil {
		b.setHint("") // the tooltip has done its job once the button is used
	}
	b.Button.Tapped(event)
}
