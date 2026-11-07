//go:build cgo

// Command mfy-desktop is mfetchy's desktop app: a native window (Fyne) that shows the very same
// screens as the terminal UI. The screens live in internal/screen as state machines that
// describe what to show; the terminal and this window each just draw that description, so
// they look and behave alike. Nothing here is specific to one screen.
//
// Sign-ins, library imports and tools run the same core flows as the terminal; their
// questions are dialogs. It needs a C compiler to build (Fyne draws with OpenGL); the mfetchy
// command itself does not.
package main

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/theme"

	"github.com/bybrooklyn/mfetchy/internal/screen"
)

func main() {
	a := app.NewWithID("io.github.bybrooklyn.mfetchy")
	a.Settings().SetTheme(ytmTheme{Theme: theme.DefaultTheme()})
	win := a.NewWindow("mFetchy Desktop")
	win.Resize(fyne.NewSize(980, 720))
	ctx, stop := context.WithCancel(context.Background())
	win.SetOnClosed(stop)
	ap := newApp(ctx, win, coreServices())
	host := screen.NewHost(screen.DesktopTabs(ap.dl, ap.library, ap.tools)...)
	v := newView(win, host)
	ap.attach(v)
	v.attachDownloads(ap.dl)
	win.ShowAndRun()
}
