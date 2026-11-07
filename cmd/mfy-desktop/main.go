//go:build cgo

// Command ytm-desktop is ytm's desktop app: a native window (Fyne) that shows the very same
// screens as the terminal UI. The screens live in internal/screen as state machines that
// describe what to show; the terminal and this window each just draw that description, so
// they look and behave alike. Nothing here is specific to one screen.
//
// It needs a C compiler to build (Fyne draws with OpenGL); the ytm command itself does not.
package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/theme"

	"github.com/bybrooklyn/mfetchy/internal/screen"
)

func main() {
	a := app.NewWithID("io.github.bybrooklyn.mfetchy")
	a.Settings().SetTheme(ytmTheme{Theme: theme.DefaultTheme()})
	win := a.NewWindow("ytm")
	win.Resize(fyne.NewSize(980, 720))
	newView(win, screen.NewHost(screen.DefaultTabs()...))
	win.ShowAndRun()
}
