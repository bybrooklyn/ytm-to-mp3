//go:build cgo

package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Colour roles beyond Fyne's own. The values are in docs/DESIGN.md; keep the two in step.
const (
	colSidebar  fyne.ThemeColorName = "ytm.sidebar"
	colSurface  fyne.ThemeColorName = "ytm.surface"
	colHover    fyne.ThemeColorName = "ytm.hover"
	colBorder   fyne.ThemeColorName = "ytm.border"
	colMuted    fyne.ThemeColorName = "ytm.muted"
	colOnAccent fyne.ThemeColorName = "ytm.onAccent"
	colSuccess  fyne.ThemeColorName = "ytm.success"
	colCaution  fyne.ThemeColorName = "ytm.caution"
	colProblem  fyne.ThemeColorName = "ytm.problem"
	colTint     fyne.ThemeColorName = "ytm.tint" // the accent, faint: the cursor row
)

// The spacing scale, in dp. Nothing in the window uses a gap that isn't one of these.
const (
	sp4  float32 = 4
	sp8  float32 = 8
	sp12 float32 = 12
	sp16 float32 = 16
	sp24 float32 = 24
	sp32 float32 = 32
)

const (
	radius       float32 = 6
	sidebarWidth float32 = 200
	contentMax   float32 = 720
)

func rgb(hex uint32) color.NRGBA {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 0xff}
}

type palette map[fyne.ThemeColorName]color.Color

var darkPalette = palette{
	theme.ColorNameBackground: rgb(0x121519),
	colSidebar:                rgb(0x0e1013),
	colSurface:                rgb(0x191d22),
	colHover:                  rgb(0x20252b),
	colBorder:                 rgb(0x2a3038),
	theme.ColorNameForeground: rgb(0xe8eaed),
	colMuted:                  rgb(0x8d96a1),
	theme.ColorNamePrimary:    rgb(0x5cc8d8),
	colOnAccent:               rgb(0x06242a),
	colSuccess:                rgb(0x62cf83),
	colCaution:                rgb(0xe2b04a),
	colProblem:                rgb(0xf2796d),
	colTint:                   color.NRGBA{0x5c, 0xc8, 0xd8, 0x1a},
}

var lightPalette = palette{
	theme.ColorNameBackground: rgb(0xf4f5f6),
	colSidebar:                rgb(0xe9ebed),
	colSurface:                rgb(0xffffff),
	colHover:                  rgb(0xf0f2f3),
	colBorder:                 rgb(0xd5d9dd),
	theme.ColorNameForeground: rgb(0x1a1d21),
	colMuted:                  rgb(0x5b646e),
	theme.ColorNamePrimary:    rgb(0x0b7285),
	colOnAccent:               rgb(0xffffff),
	colSuccess:                rgb(0x17794a),
	colCaution:                rgb(0x8f5d00),
	colProblem:                rgb(0xb3261e),
	colTint:                   color.NRGBA{0x0b, 0x72, 0x85, 0x14},
}

// ytmTheme is the window's look: flat colours, one accent, light and dark following the
// system. Force pins a variant (for screenshots); otherwise the system's is used.
type ytmTheme struct {
	fyne.Theme
	Force  fyne.ThemeVariant
	Forced bool
}

func (t ytmTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if t.Forced {
		v = t.Force
	}
	p := darkPalette
	if v == theme.VariantLight {
		p = lightPalette
	}
	if c, ok := p[n]; ok {
		return c
	}
	switch n {
	case theme.ColorNameButton, theme.ColorNameInputBackground, theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground, theme.ColorNameHeaderBackground:
		return p[colSurface]
	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return p[colBorder]
	case theme.ColorNameHover:
		return p[colHover]
	case theme.ColorNameFocus, theme.ColorNameSelection:
		return p[colTint]
	case theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
		return p[colMuted]
	case theme.ColorNameForegroundOnPrimary:
		return p[colOnAccent]
	case theme.ColorNameSuccess:
		return p[colSuccess]
	case theme.ColorNameWarning:
		return p[colCaution]
	case theme.ColorNameError:
		return p[colProblem]
	case theme.ColorNameShadow:
		return color.Transparent
	}
	return t.Theme.Color(n, v)
}

func (t ytmTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameText:
		return 14
	case theme.SizeNameCaptionText:
		return 12
	case theme.SizeNameHeadingText:
		return 24
	case theme.SizeNamePadding:
		return sp4
	case theme.SizeNameInnerPadding:
		return sp8
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return radius
	}
	return t.Theme.Size(n)
}

// pal looks a colour up in the theme now in force.
func pal(n fyne.ThemeColorName) color.Color {
	a := fyne.CurrentApp()
	return a.Settings().Theme().Color(n, a.Settings().ThemeVariant())
}
