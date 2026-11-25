package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// sparkline is a small line chart of recent samples (newest on the right),
// drawn with canvas lines: no chart library, crisp at any scale.
type sparkline struct {
	widget.BaseWidget
	values  []float64
	max     float64 // fixed upper bound; 0 = auto
	stroke  color.Color
	grid    color.Color
	lines   []*canvas.Line
	base    *canvas.Line
	limit   int
	minSize fyne.Size
}

func newSparkline(limit int, fixedMax float64, stroke, grid color.Color) *sparkline {
	s := &sparkline{limit: limit, max: fixedMax, stroke: stroke, grid: grid, minSize: fyne.NewSize(120, 56)}
	s.base = canvas.NewLine(grid)
	s.base.StrokeWidth = 1
	s.ExtendBaseWidget(s)
	return s
}

// Add appends a sample, dropping the oldest beyond the limit.
func (s *sparkline) Add(value float64) {
	s.values = append(s.values, value)
	if len(s.values) > s.limit {
		s.values = s.values[len(s.values)-s.limit:]
	}
	s.Refresh()
}

func (s *sparkline) CreateRenderer() fyne.WidgetRenderer {
	r := &sparklineRenderer{s: s}
	r.Layout(s.Size())
	return r
}

type sparklineRenderer struct{ s *sparkline }

func (r *sparklineRenderer) MinSize() fyne.Size { return r.s.minSize }

func (r *sparklineRenderer) Objects() []fyne.CanvasObject {
	objects := []fyne.CanvasObject{r.s.base}
	for _, line := range r.s.lines {
		objects = append(objects, line)
	}
	return objects
}

func (r *sparklineRenderer) Layout(size fyne.Size) {
	s := r.s
	s.base.Position1 = fyne.NewPos(0, size.Height-1)
	s.base.Position2 = fyne.NewPos(size.Width, size.Height-1)
	count := len(s.values)
	segments := count - 1
	if segments < 0 {
		segments = 0
	}
	for len(s.lines) < segments {
		line := canvas.NewLine(s.stroke)
		line.StrokeWidth = 2
		s.lines = append(s.lines, line)
	}
	s.lines = s.lines[:segments]
	if segments == 0 || size.Width <= 0 || size.Height <= 0 {
		return
	}
	top := s.max
	if top <= 0 {
		for _, value := range s.values {
			if value > top {
				top = value
			}
		}
		if top <= 0 {
			top = 1
		}
		top *= 1.15
	}
	// A young series is stretched over the whole card (and compresses as it
	// grows to the limit); right-aligned fixed steps left it as a tiny
	// squiggle in the corner for the first hour.
	visible := count
	if visible > s.limit {
		visible = s.limit
	}
	step := size.Width / float32(maxInt(visible-1, 1))
	offset := size.Width - step*float32(segments)
	point := func(index int) fyne.Position {
		value := s.values[index]
		if value < 0 {
			value = 0
		}
		if value > top {
			value = top
		}
		y := size.Height - 2 - float32(value/top)*(size.Height-4)
		return fyne.NewPos(offset+step*float32(index), y)
	}
	for index, line := range s.lines {
		line.Position1 = point(index)
		line.Position2 = point(index + 1)
		line.StrokeColor = s.stroke
	}
}

func (r *sparklineRenderer) Refresh() {
	r.Layout(r.s.Size())
	canvas.Refresh(r.s)
}

func (r *sparklineRenderer) Destroy() {}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// metricChart is a titled card with a sparkline, its current value and an
// optional detail line under the chart.
type metricChart struct {
	line  *sparkline
	value *widget.Label
	unit  string
}

func (c *clientApp) newMetricChart(title, unit string, fixedMax float64) (*metricChart, fyne.CanvasObject, *widget.Label) {
	chart := &metricChart{
		line:  newSparkline(120, fixedMax, c.tint(theme.ColorNamePrimary), c.tint(theme.ColorNameSeparator)),
		value: widget.NewLabelWithStyle("—", fyne.TextAlignTrailing, fyne.TextStyle{Bold: true}),
		unit:  unit,
	}
	caption := widget.NewLabel(title)
	caption.Importance = widget.LowImportance
	caption.SizeName = theme.SizeNameCaptionText
	detail := widget.NewLabel("")
	detail.Importance = widget.LowImportance
	detail.SizeName = theme.SizeNameCaptionText
	detail.Truncation = fyne.TextTruncateEllipsis
	return chart, c.surface(container.NewVBox(
		container.NewBorder(nil, nil, caption, chart.value),
		chart.line,
		detail,
	), ""), detail
}

func (m *metricChart) add(value float64) {
	if m == nil {
		return
	}
	m.line.Add(value)
	m.value.SetText(fmt.Sprintf("%.1f%s", value, m.unit))
}
