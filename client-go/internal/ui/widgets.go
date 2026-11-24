package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/i18n"
)

// tr is the single entry point for translated UI strings.
func tr(key string, vars map[string]any) string { return i18n.T(key, vars) }

var colorTransparent = color.Transparent

// surface draws a rounded panel behind content. Fyne 2.7 has no card with
// configurable corners, so this is built from a canvas.Rectangle and a Stack.
func (c *clientApp) surface(content fyne.CanvasObject, accent fyne.ThemeColorName) fyne.CanvasObject {
	bg := canvas.NewRectangle(c.tint(theme.ColorNameHeaderBackground))
	bg.CornerRadius = 12
	bg.StrokeColor = c.tint(theme.ColorNameSeparator)
	bg.StrokeWidth = 1
	if accent != "" {
		bg.StrokeColor = c.tint(accent)
		bg.StrokeWidth = 2
	}
	return container.NewStack(bg, container.NewPadded(content))
}

// tint resolves a semantic colour from the active palette.
func (c *clientApp) tint(name fyne.ThemeColorName) color.Color {
	return c.uiTheme.color(name)
}

// bannerTint returns a light semantic wash instead of a saturated fill, so the
// status colour is recognisable without drowning the text in colour.
func (c *clientApp) bannerTint(name fyne.ThemeColorName) color.Color {
	switch name {
	case theme.ColorNameError:
		return nr(0xfe, 0xe2, 0xe2)
	case theme.ColorNameWarning:
		return nr(0xfe, 0xf3, 0xc7)
	case theme.ColorNameSuccess:
		return nr(0xdc, 0xfc, 0xe7)
	case theme.ColorNamePrimary:
		return nr(0xef, 0xf6, 0xff)
	default:
		return c.tint(name)
	}
}

// sectionTitle builds a consistent heading with a hairline separator.
func (c *clientApp) sectionTitle(title, subtitle string) fyne.CanvasObject {
	heading := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	objects := []fyne.CanvasObject{heading}
	if subtitle != "" {
		sub := widget.NewLabelWithStyle(subtitle, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
		sub.Wrapping = fyne.TextWrapWord
		objects = append(objects, sub)
	}
	line := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
	line.SetMinSize(fyne.NewSize(1, 1))
	objects = append(objects, line)
	return container.NewVBox(objects...)
}

// statTile is a compact KPI tile: big value, small caption.
func (c *clientApp) statTile(value, caption string) (*widget.Label, fyne.CanvasObject) {
	number := widget.NewLabelWithStyle(value, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label := widget.NewLabelWithStyle(caption, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	return number, c.surface(container.NewVBox(number, label), "")
}

// hintButton is a Button that reports a short explanation in the window's
// hint strip on hover. Fyne 2.7 has no tooltip widget, so this is the closest
// equivalent and keeps the interface discoverable without popups.
// hintArea adds hover descriptions to any widget without hiding its Tapped
// callback (Fyne 2.7 has no native tooltip).
type hintArea struct {
	fyne.Widget
	hint    string
	setHint func(string)
}

func (h *hintArea) MouseIn(*desktop.MouseEvent) {
	if h.setHint != nil {
		h.setHint(h.hint)
	}
}

func (h *hintArea) MouseMoved(*desktop.MouseEvent) {}

func (h *hintArea) MouseOut() {
	if h.setHint != nil {
		h.setHint("")
	}
}

func (h *hintArea) Tapped(event *fyne.PointEvent) {
	if target, ok := h.Widget.(fyne.Tappable); ok {
		target.Tapped(event)
	}
}

func withHint(object fyne.Widget, hint string, setHint func(string)) fyne.CanvasObject {
	return &hintArea{Widget: object, hint: hint, setHint: setHint}
}

type hintButton struct {
	*widget.Button
	hint    string
	setHint func(string)
}

func newHintButton(
	label string, icon fyne.Resource, hint string, importance widget.Importance, onTap func(), setHint func(string),
) *hintButton {
	if hint == "" {
		hint = label
	}
	var button *widget.Button
	if icon == nil {
		button = widget.NewButton(label, onTap)
	} else {
		button = widget.NewButtonWithIcon(label, icon, onTap)
	}
	button.Importance = importance
	button.Refresh()
	return &hintButton{Button: button, hint: hint, setHint: setHint}
}

func (b *hintButton) MouseIn(event *desktop.MouseEvent) {
	b.Button.MouseIn(event)
	if b.setHint != nil {
		b.setHint(b.hint)
	}
}

func (b *hintButton) MouseMoved(event *desktop.MouseEvent) { b.Button.MouseMoved(event) }

func (b *hintButton) MouseOut() {
	b.Button.MouseOut()
	if b.setHint != nil {
		b.setHint("")
	}
}

// newTextList builds a single-line list with word-elided labels.
func newTextList(length func() int, update func(int, *widget.Label)) *widget.List {
	return widget.NewList(
		func() int { return length() },
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.Wrapping = fyne.TextTruncate
			return label
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			update(int(id), object.(*widget.Label))
		},
	)
}

// note is a wrapping secondary label. Long notes must never blow up the
// minimum window width at 900px.
func (c *clientApp) note(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{})
	label.Wrapping = fyne.TextWrapWord
	return label
}

// placeholder is a short, muted line used in empty states.
func placeholder(text string) fyne.CanvasObject {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignCenter, fyne.TextStyle{})
	label.Wrapping = fyne.TextWrapWord
	return container.NewPadded(label)
}
