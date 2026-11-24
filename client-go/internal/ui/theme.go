package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// nasTheme is a small, dependency-free Fyne theme. It keeps Fyne's fonts,
// icons and metrics but replaces the palette with a calmer, more legible one
// and, above all, with stable semantic colours (primary/success/warning/error)
// used by the status banner and the cards.
type nasTheme struct {
	variant fyne.ThemeVariant
	base    fyne.Theme
	colors  map[fyne.ThemeColorName]color.Color
}

func newNAStheme(variant fyne.ThemeVariant) *nasTheme {
	t := &nasTheme{
		variant: variant,
		base:    theme.LightTheme(),
		colors:  make(map[fyne.ThemeColorName]color.Color),
	}
	if variant == theme.VariantDark {
		t.loadDark()
	} else {
		t.loadLight()
	}
	return t
}

func (t *nasTheme) set(colors map[fyne.ThemeColorName]color.Color) {
	for name, value := range colors {
		t.colors[name] = value
	}
}

func (t *nasTheme) loadLight() {
	t.set(map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:          nr(0xf6, 0xf8, 0xfb),
		theme.ColorNameButton:              nr(0xff, 0xff, 0xff),
		theme.ColorNameDisabled:            nr(0x94, 0xa3, 0xb8),
		theme.ColorNameDisabledButton:      nr(0xee, 0xf2, 0xf6),
		theme.ColorNameError:               nr(0xdc, 0x26, 0x26),
		theme.ColorNameForeground:          nr(0x0b, 0x12, 0x20),
		theme.ColorNameForegroundOnError:   nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnPrimary: nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnSuccess: nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnWarning: nr(0xff, 0xff, 0xff),
		theme.ColorNameHeaderBackground:    nr(0xff, 0xff, 0xff),
		theme.ColorNameHover:               color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x0f},
		theme.ColorNameInputBackground:     nr(0xff, 0xff, 0xff),
		theme.ColorNameInputBorder:         nr(0xcb, 0xd5, 0xe1),
		theme.ColorNameMenuBackground:      nr(0xff, 0xff, 0xff),
		theme.ColorNameOverlayBackground:   nr(0xff, 0xff, 0xff),
		theme.ColorNamePlaceHolder:         nr(0x47, 0x55, 0x69),
		theme.ColorNamePressed:             color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x1f},
		theme.ColorNameScrollBar:           color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x80},
		theme.ColorNameScrollBarBackground: nr(0xf1, 0xf5, 0xf9),
		theme.ColorNameSeparator:           nr(0x94, 0xa3, 0xb8),
		theme.ColorNameShadow:              color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x2e},
		theme.ColorNameSuccess:             nr(0x05, 0x96, 0x69),
		theme.ColorNameWarning:             nr(0xd9, 0x77, 0x06),
		// Primary is a calm slate, not a saturated blue: blue stays reserved for
		// hyperlinks and semantic information, so the UI does not look "all blue".
		theme.ColorNamePrimary:   nr(0x33, 0x41, 0x55),
		theme.ColorNameHyperlink: nr(0x25, 0x63, 0xeb),
		theme.ColorNameFocus:     color.NRGBA{R: 0x33, G: 0x41, B: 0x55, A: 0x45},
		theme.ColorNameSelection: nr(0xdb, 0xea, 0xfe),
	})
}

func (t *nasTheme) loadDark() {
	t.set(map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:          nr(0x0f, 0x17, 0x2a),
		theme.ColorNameButton:              nr(0x1e, 0x29, 0x3b),
		theme.ColorNameDisabled:            nr(0x47, 0x55, 0x69),
		theme.ColorNameDisabledButton:      nr(0x1e, 0x29, 0x3b),
		theme.ColorNameError:               nr(0xf8, 0x71, 0x71),
		theme.ColorNameForeground:          nr(0xe2, 0xe8, 0xf0),
		theme.ColorNameForegroundOnError:   nr(0x0f, 0x17, 0x2a),
		theme.ColorNameForegroundOnPrimary: nr(0xe2, 0xe8, 0xf0),
		theme.ColorNameForegroundOnSuccess: nr(0x0f, 0x17, 0x2a),
		theme.ColorNameForegroundOnWarning: nr(0x0f, 0x17, 0x2a),
		theme.ColorNameHeaderBackground:    nr(0x1e, 0x29, 0x3b),
		theme.ColorNameHover:               color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x14},
		theme.ColorNameInputBackground:     nr(0x17, 0x20, 0x33),
		theme.ColorNameInputBorder:         nr(0x47, 0x55, 0x69),
		theme.ColorNameMenuBackground:      nr(0x1e, 0x29, 0x3b),
		theme.ColorNameOverlayBackground:   nr(0x0f, 0x17, 0x2a),
		theme.ColorNamePlaceHolder:         nr(0x94, 0xa3, 0xb8),
		theme.ColorNamePressed:             color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x26},
		theme.ColorNameScrollBar:           color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x80},
		theme.ColorNameScrollBarBackground: nr(0x17, 0x20, 0x33),
		theme.ColorNameSeparator:           nr(0x33, 0x41, 0x55),
		theme.ColorNameShadow:              color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x80},
		theme.ColorNameSuccess:             nr(0x34, 0xd3, 0x99),
		theme.ColorNameWarning:             nr(0xfb, 0xbf, 0x24),
		theme.ColorNamePrimary:             nr(0x47, 0x55, 0x69),
		theme.ColorNameHyperlink:           nr(0x60, 0xa5, 0xfa),
		theme.ColorNameFocus:               color.NRGBA{R: 0x71, G: 0x85, B: 0x9c, A: 0x5c},
		theme.ColorNameSelection:           nr(0x33, 0x41, 0x55),
	})
}

func nr(r, g, b uint8) color.NRGBA { return color.NRGBA{R: r, G: g, B: b, A: 0xff} }

func (t *nasTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	if value, ok := t.colors[name]; ok {
		return value
	}
	return t.base.Color(name, t.variant)
}

func (t *nasTheme) Font(style fyne.TextStyle) fyne.Resource    { return t.base.Font(style) }
func (t *nasTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.base.Icon(name) }

// Size keeps Fyne's metrics: custom sizes tend to break widget internals on
// smaller windows (900x640 is a supported size).
func (t *nasTheme) Size(name fyne.ThemeSizeName) float32 { return t.base.Size(name) }

func (t *nasTheme) color(name fyne.ThemeColorName) color.Color {
	return t.Color(name, t.variant)
}

// variantFrom returns the requested theme variant, defaulting to light for
// unknown values (config key "go_theme": light | dark).
func variantFrom(value string) fyne.ThemeVariant {
	if value == "dark" {
		return theme.VariantDark
	}
	return theme.VariantLight
}
