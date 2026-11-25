package ui

import (
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/i18n"
)

// tr is the single entry point for translated UI strings.
func tr(key string, vars map[string]any) string { return i18n.T(key, vars) }

var colorTransparent = color.Transparent

// surface draws a rounded card behind content. It is a widget (not a bare
// Stack) so the border is drawn only once the card has a real size: a
// stroked rounded rectangle smaller than its radius breaks the software
// painter used for captures and tests.
func (c *clientApp) surface(content fyne.CanvasObject, accent fyne.ThemeColorName) fyne.CanvasObject {
	bg := canvas.NewRectangle(c.tint(colorNameSurface))
	bg.CornerRadius = 12
	bg.StrokeColor = c.tint(theme.ColorNameSeparator)
	stroke := float32(1)
	if accent != "" {
		bg.StrokeColor = c.tint(accent)
		stroke = 2
	}
	panel := &surfacePanel{bg: bg, stroke: stroke, content: container.NewPadded(content)}
	panel.ExtendBaseWidget(panel)
	return panel
}

type surfacePanel struct {
	widget.BaseWidget
	bg      *canvas.Rectangle
	stroke  float32
	content fyne.CanvasObject
}

func (p *surfacePanel) CreateRenderer() fyne.WidgetRenderer {
	r := &surfaceRenderer{p: p}
	r.Layout(p.Size())
	return r
}

type surfaceRenderer struct{ p *surfacePanel }

func (r *surfaceRenderer) Layout(size fyne.Size) {
	limit := 2*r.p.bg.CornerRadius + 2*r.p.stroke + 2
	if size.Width < limit || size.Height < limit {
		r.p.bg.StrokeWidth = 0
	} else {
		r.p.bg.StrokeWidth = r.p.stroke
	}
	r.p.bg.Resize(size)
	r.p.content.Resize(size)
}
func (r *surfaceRenderer) MinSize() fyne.Size { return r.p.content.MinSize() }
func (r *surfaceRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.p.bg, r.p.content}
}
func (r *surfaceRenderer) Refresh() {
	r.Layout(r.p.Size())
	r.p.bg.Refresh()
	r.p.content.Refresh()
}
func (r *surfaceRenderer) Destroy() {}

// tint resolves a semantic colour from the active palette.
func (c *clientApp) tint(name fyne.ThemeColorName) color.Color {
	return c.uiTheme.color(name)
}

// bannerTint returns a light semantic wash instead of a saturated fill, so the
// status colour is recognisable without drowning the text in colour.
func (c *clientApp) bannerTint(name fyne.ThemeColorName) color.Color {
	dark := c.uiTheme != nil && c.uiTheme.variant == theme.VariantDark
	if dark {
		switch name {
		case theme.ColorNameError:
			return nr(0x4a, 0x1d, 0x1d)
		case theme.ColorNameWarning:
			return nr(0x4a, 0x38, 0x12)
		case theme.ColorNameSuccess:
			return nr(0x12, 0x3a, 0x28)
		case theme.ColorNamePrimary:
			return nr(0x1f, 0x29, 0x37)
		default:
			return c.tint(name)
		}
	}
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

// sectionTitle builds a consistent heading with a hairline separator. The
// heading is optional: inside a tab the tab label already names the page, so
// repeating it only costs vertical space. The subtitle is a muted one-liner.
func (c *clientApp) sectionTitle(title, subtitle string) fyne.CanvasObject {
	objects := []fyne.CanvasObject{}
	if title != "" {
		objects = append(objects, widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	}
	if subtitle != "" {
		sub := widget.NewLabel(subtitle)
		sub.Importance = widget.LowImportance
		sub.Wrapping = fyne.TextWrapWord
		objects = append(objects, sub)
	}
	if title != "" {
		line := canvas.NewRectangle(c.tint(theme.ColorNameSeparator))
		line.SetMinSize(fyne.NewSize(1, 1))
		objects = append(objects, line)
	}
	return container.NewVBox(objects...)
}

// statTile is a compact tile: small muted caption on top, the value below.
// Its height follows its content (never use it inside a uniform grid next to
// a taller tile: GridLayout makes every cell as tall as the tallest one).
func (c *clientApp) statTile(value, caption string) (*widget.Label, fyne.CanvasObject) {
	number := widget.NewLabelWithStyle(value, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	number.Wrapping = fyne.TextWrapWord
	label := widget.NewLabel(caption)
	label.Importance = widget.LowImportance
	label.SizeName = theme.SizeNameCaptionText
	label.Truncation = fyne.TextTruncateEllipsis
	return number, c.surface(container.NewVBox(label, number), "")
}

// tileRows lays tiles out in rows of `columns`, each row as tall as its own
// tallest tile (container.NewGridWithColumns would use one height for all).
func tileRows(columns int, tiles ...fyne.CanvasObject) fyne.CanvasObject {
	rows := container.NewVBox()
	for start := 0; start < len(tiles); start += columns {
		end := start + columns
		row := append([]fyne.CanvasObject{}, tiles[start:minInt(end, len(tiles))]...)
		for len(row) < columns {
			row = append(row, layout.NewSpacer())
		}
		rows.Add(container.NewGridWithColumns(columns, row...))
	}
	return rows
}

// formatCount groups digits ("111.402" in Italian, "111,402" in English).
func formatCount(value int) string {
	separator := "."
	if strings.EqualFold(i18n.Language, "en") {
		separator = ","
	}
	digits := strconv.Itoa(value)
	negative := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")
	var out []byte
	for index := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			out = append(out, separator...)
		}
		out = append(out, digits[index])
	}
	if negative {
		return "-" + string(out)
	}
	return string(out)
}

// hintArea is kept only for compatibility with older call sites: wrapping a
// widget in another widget without its own renderer broke drawing (labels
// clipped, buttons without background). withHint now returns the widget as is.
type hintArea struct {
	fyne.Widget
	hint    string
	setHint func(string)
}

func withHint(object fyne.Widget, _ string, _ func(string)) fyne.CanvasObject {
	return object
}

// hintButton is a real widget.Button (ExtendBaseWidget) that also reports a
// short explanation on hover. Fyne 2.7 has no tooltip widget.
type hintButton struct {
	widget.Button
	hint    string
	setHint func(string)
}

func newHintButton(
	label string, icon fyne.Resource, hint string, importance widget.Importance, onTap func(), setHint func(string),
) *hintButton {
	if hint == "" {
		hint = label
	}
	b := &hintButton{hint: hint, setHint: setHint}
	b.Text = label
	b.Icon = icon
	b.OnTapped = onTap
	b.Importance = importance
	b.ExtendBaseWidget(b)
	return b
}

// plainButton returns a standalone *widget.Button for fields that are both
// referenced (SetText/Disable) and placed in containers. The embedded Button
// of a hintButton must never be added to a container on its own: its
// renderer belongs to the outer widget and it would draw without background.
func plainButton(label string, icon fyne.Resource, _ string, importance widget.Importance, onTap func(), _ func(string)) *widget.Button {
	button := widget.NewButtonWithIcon(label, icon, onTap)
	if icon == nil {
		button = widget.NewButton(label, onTap)
	}
	button.Importance = importance
	return button
}

// Btn exposes the underlying button for SetText/Enable/Disable.
func (b *hintButton) Btn() *widget.Button { return &b.Button }

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

// listRow is a compact two-line row used by files, conflicts, history and
// devices. Keeping the metadata in a trailing label avoids the old padded text
// strings that became unreadable at narrow widths.
type listRow struct {
	icon *widget.Icon
	name *widget.Label
	meta *widget.Label
}

type listRowObject struct {
	widget.BaseWidget
	content fyne.CanvasObject
	row     *listRow
}

type listRowRenderer struct {
	*listRowObject
}

func (o *listRowObject) CreateRenderer() fyne.WidgetRenderer {
	return &listRowRenderer{listRowObject: o}
}

func (r *listRowRenderer) Layout(size fyne.Size) {
	r.content.Resize(size)
	r.content.Move(fyne.NewPos(0, 0))
}

func (r *listRowRenderer) MinSize() fyne.Size           { return r.content.MinSize() }
func (r *listRowRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.content} }
func (r *listRowRenderer) Refresh()                     { r.content.Refresh() }
func (r *listRowRenderer) Destroy()                     {}

func newRowList(length func() int, nameWidth, metaWidth float32, update func(int, *listRow)) *widget.List {
	return widget.NewList(
		length,
		func() fyne.CanvasObject {
			icon := widget.NewIcon(theme.FileIcon())
			name := widget.NewLabel("")
			name.Wrapping = fyne.TextTruncate
			meta := widget.NewLabel("")
			meta.Alignment = fyne.TextAlignTrailing
			meta.Wrapping = fyne.TextTruncate
			row := &listRow{icon: icon, name: name, meta: meta}
			object := &listRowObject{
				content: container.NewBorder(nil, nil, icon, minWidth(meta, metaWidth), minWidth(name, nameWidth)),
				row:     row,
			}
			object.ExtendBaseWidget(object)
			return object
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			row := findListRow(object)
			if row != nil {
				update(int(id), row)
				row.icon.Refresh()
			}
		},
	)
}

func findListRow(object fyne.CanvasObject) *listRow {
	switch value := object.(type) {
	case *listRowObject:
		return value.row
	case *fyne.Container:
		for _, child := range value.Objects {
			if row := findListRow(child); row != nil {
				return row
			}
		}
	case *hintArea:
		return findListRow(value.Widget)
	}
	return nil
}

// widthWidget constrains a child's minimum width while keeping it in Fyne's
// widget renderer tree. A plain CanvasObject wrapper is laid out but never
// painted, which previously hid setting labels and header/footer text.
type widthWidget struct {
	widget.BaseWidget
	content fyne.CanvasObject
	width   float32
	maximum bool
}

type widthRenderer struct {
	*widthWidget
}

func (w *widthWidget) CreateRenderer() fyne.WidgetRenderer { return &widthRenderer{widthWidget: w} }

func (w *widthWidget) MinSize() fyne.Size {
	size := w.content.MinSize()
	if w.maximum && size.Width > w.width {
		size.Width = w.width
	}
	if !w.maximum && size.Width < w.width {
		size.Width = w.width
	}
	return size
}

func (r *widthRenderer) Layout(size fyne.Size) {
	r.content.Resize(size)
	r.content.Move(fyne.NewPos(0, 0))
}

func (r *widthRenderer) MinSize() fyne.Size           { return r.widthWidget.MinSize() }
func (r *widthRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.content} }
func (r *widthRenderer) Refresh()                     { r.content.Refresh() }
func (r *widthRenderer) Destroy()                     {}

func minWidth(object fyne.CanvasObject, width float32) fyne.CanvasObject {
	constrained := &widthWidget{content: object, width: width}
	constrained.ExtendBaseWidget(constrained)
	return constrained
}

func maxWidth(object fyne.CanvasObject, width float32) fyne.CanvasObject {
	constrained := &widthWidget{content: object, width: width, maximum: true}
	constrained.ExtendBaseWidget(constrained)
	return constrained
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

// inset gives scrolling pages a right margin that clears the scroll bar and a
// small left margin, so cards do not touch the window edge.
func inset(object fyne.CanvasObject) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(2, 12, 2, 14), object)
}
