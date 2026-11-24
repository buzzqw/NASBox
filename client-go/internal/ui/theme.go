package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// nasTheme is the NASBox design system on top of Fyne: Fyne's fonts and icons,
// a calm palette with one blue accent (as in the best-known sync clients),
// stable semantic colours and roomier metrics. The two extra colour names
// below are used for secondary text and for the sidebar surface.
type nasTheme struct {
	variant fyne.ThemeVariant
	base    fyne.Theme
	colors  map[fyne.ThemeColorName]color.Color
}

const (
	colorNameMuted   fyne.ThemeColorName = "nasMuted"
	colorNameSidebar fyne.ThemeColorName = "nasSidebar"
	colorNameSurface fyne.ThemeColorName = "nasSurface"
	colorNameAccentS fyne.ThemeColorName = "nasAccentSoft"
)

func newNAStheme(variant fyne.ThemeVariant) *nasTheme {
	t := &nasTheme{
		variant: variant,
		base:    theme.DefaultTheme(),
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
		theme.ColorNameBackground:          nr(0xf5, 0xf7, 0xfa),
		theme.ColorNameButton:              nr(0xea, 0xee, 0xf4),
		theme.ColorNameDisabled:            nr(0x9a, 0xa6, 0xb8),
		theme.ColorNameDisabledButton:      nr(0xf0, 0xf2, 0xf6),
		theme.ColorNameError:               nr(0xdc, 0x26, 0x26),
		theme.ColorNameForeground:          nr(0x0f, 0x17, 0x2a),
		theme.ColorNameForegroundOnError:   nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnPrimary: nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnSuccess: nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnWarning: nr(0xff, 0xff, 0xff),
		theme.ColorNameHeaderBackground:    nr(0xff, 0xff, 0xff),
		theme.ColorNameHover:               color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x0c},
		theme.ColorNameInputBackground:     nr(0xff, 0xff, 0xff),
		theme.ColorNameInputBorder:         nr(0xd5, 0xdc, 0xe6),
		theme.ColorNameMenuBackground:      nr(0xff, 0xff, 0xff),
		theme.ColorNameOverlayBackground:   nr(0xff, 0xff, 0xff),
		theme.ColorNamePlaceHolder:         nr(0x64, 0x74, 0x8b),
		theme.ColorNamePressed:             color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x18},
		theme.ColorNameScrollBar:           color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x55},
		theme.ColorNameScrollBarBackground: color.NRGBA{A: 0},
		theme.ColorNameSeparator:           nr(0xe4, 0xe8, 0xef),
		theme.ColorNameShadow:              color.NRGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0x22},
		theme.ColorNameSuccess:             nr(0x16, 0xa3, 0x4a),
		theme.ColorNameWarning:             nr(0xd9, 0x77, 0x06),
		theme.ColorNamePrimary:             nr(0x25, 0x63, 0xeb),
		theme.ColorNameHyperlink:           nr(0x25, 0x63, 0xeb),
		theme.ColorNameFocus:               color.NRGBA{R: 0x25, G: 0x63, B: 0xeb, A: 0x40},
		theme.ColorNameSelection:           nr(0xdb, 0xe7, 0xfe),
		colorNameMuted:                     nr(0x64, 0x74, 0x8b),
		colorNameSidebar:                   nr(0xff, 0xff, 0xff),
		colorNameSurface:                   nr(0xff, 0xff, 0xff),
		colorNameAccentS:                   nr(0xef, 0xf4, 0xff),
	})
}

func (t *nasTheme) loadDark() {
	t.set(map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:          nr(0x0b, 0x12, 0x20),
		theme.ColorNameButton:              nr(0x1c, 0x27, 0x3d),
		theme.ColorNameDisabled:            nr(0x50, 0x5e, 0x78),
		theme.ColorNameDisabledButton:      nr(0x16, 0x1f, 0x31),
		theme.ColorNameError:               nr(0xf8, 0x71, 0x71),
		theme.ColorNameForeground:          nr(0xe6, 0xeb, 0xf3),
		theme.ColorNameForegroundOnError:   nr(0x0b, 0x12, 0x20),
		theme.ColorNameForegroundOnPrimary: nr(0xff, 0xff, 0xff),
		theme.ColorNameForegroundOnSuccess: nr(0x0b, 0x12, 0x20),
		theme.ColorNameForegroundOnWarning: nr(0x0b, 0x12, 0x20),
		theme.ColorNameHeaderBackground:    nr(0x12, 0x1b, 0x2e),
		theme.ColorNameHover:               color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x10},
		theme.ColorNameInputBackground:     nr(0x0f, 0x17, 0x29),
		theme.ColorNameInputBorder:         nr(0x2c, 0x3a, 0x57),
		theme.ColorNameMenuBackground:      nr(0x16, 0x20, 0x35),
		theme.ColorNameOverlayBackground:   nr(0x12, 0x1b, 0x2e),
		theme.ColorNamePlaceHolder:         nr(0x8b, 0x9a, 0xb4),
		theme.ColorNamePressed:             color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x20},
		theme.ColorNameScrollBar:           color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x55},
		theme.ColorNameScrollBarBackground: color.NRGBA{A: 0},
		theme.ColorNameSeparator:           nr(0x22, 0x2f, 0x4a),
		theme.ColorNameShadow:              color.NRGBA{A: 0x80},
		theme.ColorNameSuccess:             nr(0x34, 0xd3, 0x99),
		theme.ColorNameWarning:             nr(0xfb, 0xbf, 0x24),
		theme.ColorNamePrimary:             nr(0x3b, 0x82, 0xf6),
		theme.ColorNameHyperlink:           nr(0x60, 0xa5, 0xfa),
		theme.ColorNameFocus:               color.NRGBA{R: 0x60, G: 0xa5, B: 0xfa, A: 0x55},
		theme.ColorNameSelection:           nr(0x1e, 0x3a, 0x5f),
		colorNameMuted:                     nr(0x8b, 0x9a, 0xb4),
		colorNameSidebar:                   nr(0x0f, 0x17, 0x29),
		colorNameSurface:                   nr(0x12, 0x1b, 0x2e),
		colorNameAccentS:                   nr(0x15, 0x24, 0x3d),
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

// Size gives the interface room to breathe: more inner padding (buttons and
// inputs no longer look cramped), rounded inputs and slightly larger headings.
func (t *nasTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 5
	case theme.SizeNameInnerPadding:
		return 7
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 8
	case theme.SizeNameText:
		return 14
	case theme.SizeNameHeadingText:
		return 24
	case theme.SizeNameSubHeadingText:
		return 18
	case theme.SizeNameScrollBarSmall:
		return 4
	}
	return t.base.Size(name)
}

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
