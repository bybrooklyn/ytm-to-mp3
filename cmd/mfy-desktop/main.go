//go:build cgo

// Command mfy-desktop is mfetchy's desktop app: a native window (Fyne) that shows the very same
// screens as the terminal UI. The screens live in internal/screen as state machines that
// describe what to show; the terminal and this window each just draw that description, so
// they look and behave alike. Nothing here is specific to one screen.
//
// It needs a C compiler to build (Fyne draws with OpenGL); the mfetchy command itself does not.
package main

import (
	"context"
	"io"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/theme"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

func main() {
	a := app.NewWithID("io.github.bybrooklyn.mfetchy")
	a.Settings().SetTheme(ytmTheme{Theme: theme.DefaultTheme()})
	win := a.NewWindow("mFetchy Desktop")
	win.Resize(fyne.NewSize(980, 720))
	dl := newDownloads(win)
	v := newView(win, screen.NewHost(screen.DesktopTabs(dl)...))
	v.attachDownloads(dl)
	win.ShowAndRun()
}

// newDownloads is the Downloads page wired to the real pipeline: the very core.Download the
// terminal runs, with dialogs for its questions and the page as its event sink.
func newDownloads(win fyne.Window) *screen.Downloads {
	run := func(ctx context.Context, inputs []string, sink jobs.Sink) (screen.RunResult, error) {
		o := core.OptionsFromConfig(config.Load())
		res, err := core.Download(ctx, inputs, o, core.Deps{
			UI: ui.NewWriter(io.Discard), Prompter: dialogPrompter{win}, Sink: sink,
		})
		return screen.RunResult{Results: res.Results, Held: res.Held}, err
	}
	settings := func() screen.DownloadSettings {
		o := core.OptionsFromConfig(config.Load())
		return screen.DownloadSettings{Folder: core.Tilde(o.Out), Format: o.Format, Extras: o.Extras()}
	}
	return screen.NewDownloads(run, settings)
}
