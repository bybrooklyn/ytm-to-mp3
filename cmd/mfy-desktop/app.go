//go:build cgo

package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// flowFn is a core flow: it prints to u, asks through p, and runs with o.
type flowFn = func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error

// services is the work the window can start. Each is a core flow that asks its questions
// through a core.Prompter (here, dialogs) and prints to a ui.UI (here, a log or nothing).
// Tests replace them with fakes that talk to the prompter the same way.
type services struct {
	// account: the sign-ins and sign-outs of Settings and Connections, by the action name the
	// screens use ("signin", "applein", ...).
	account map[string]func(ctx context.Context, u *ui.UI, p core.Prompter) error
	// library: the imports of the Library page, by screen.Lib* id.
	library map[string]flowFn
	// tools: the Tools page, by screen.Tool* id.
	tools map[string]flowFn
}

func coreServices() services {
	return services{
		account: map[string]func(context.Context, *ui.UI, core.Prompter) error{
			"signin":   core.VPNSignIn,
			"signout":  func(ctx context.Context, u *ui.UI, _ core.Prompter) error { core.VPNSignOut(ctx); return nil },
			"premium":  core.PremiumSetup,
			"applein":  core.AppleLogin,
			"appleout": func(_ context.Context, u *ui.UI, _ core.Prompter) error { return core.AppleLogout(u) },
			"tidalin":  core.TidalLogin,
			"tidalout": func(_ context.Context, u *ui.UI, _ core.Prompter) error { return core.TidalLogout(u) },
			"youtube":  func(ctx context.Context, _ *ui.UI, p core.Prompter) error { return core.ChooseYouTubeBrowser(ctx, p) },
			"bandcamp": func(ctx context.Context, _ *ui.UI, p core.Prompter) error { return core.ChooseBandcampBrowser(ctx, p) },
		},
		library: map[string]flowFn{
			screen.LibAll:        core.AllAccountsFlow,
			screen.LibYouTube:    core.LiveLibraryFlow,
			screen.LibBandcamp:   core.BandcampFlow,
			screen.LibTakeout:    core.TakeoutFlow,
			screen.LibAppleMatch: core.AppleMatchFlow,
		},
		tools: map[string]flowFn{
			screen.ToolDoctor: func(ctx context.Context, u *ui.UI, o core.Options, _ core.Prompter) error {
				return core.RunDoctor(ctx, u, o)
			},
			screen.ToolVPNTest: func(ctx context.Context, u *ui.UI, _ core.Options, _ core.Prompter) error {
				return core.VPNTest(ctx, u)
			},
			screen.ToolUpdate: func(ctx context.Context, u *ui.UI, _ core.Options, _ core.Prompter) error {
				return core.UpdateTools(ctx, u)
			},
			screen.ToolTag:  core.TagFlow,
			screen.ToolTidy: core.TidyFlow,
			screen.ToolLyrics: func(ctx context.Context, u *ui.UI, o core.Options, _ core.Prompter) error {
				return core.LyricsLibraryFlow(ctx, u, o)
			},
			screen.ToolSidecars: core.SidecarLibraryFlow,
		},
	}
}

// app is everything behind the window: the pages' models, and the work the buttons start.
type mfy struct {
	win fyne.Window
	svc services
	ctx context.Context

	dl             *screen.Downloads
	library, tools *screen.Menu
	v              *view

	busy atomic.Bool // an account flow has its dialogs open
}

func newApp(ctx context.Context, win fyne.Window, svc services) *mfy {
	a := &mfy{win: win, svc: svc, ctx: ctx}
	a.dl = screen.NewDownloads(a.runDownload, func() screen.DownloadSettings {
		o := core.OptionsFromConfig(config.Load())
		return screen.DownloadSettings{Folder: core.Tilde(o.Out), Format: o.Format, Extras: o.Extras()}
	})
	a.library = screen.NewMenu("library", "", screen.LibraryItems(), func(id string) { go a.startLibrary(id) })
	a.tools = screen.NewMenu("tools", "", screen.ToolItems(), a.startTool)
	return a
}

// prompter is how every flow asks its questions: a dialog over the window.
func (a *mfy) prompter() core.Prompter { return dialogPrompter{a.win} }

// options are the settings a flow runs with, read fresh. The window can always ask.
func (a *mfy) options() core.Options {
	o := core.OptionsFromConfig(config.Load())
	o.Ask = true
	return o
}

// runDownload is the Downloads page's own run: the very core.Download the terminal runs.
func (a *mfy) runDownload(ctx context.Context, inputs []string, sink jobs.Sink) (screen.RunResult, error) {
	res, err := core.Download(ctx, inputs, a.options(), core.Deps{
		UI: ui.NewWriter(discard{}), Prompter: a.prompter(), Sink: sink,
	})
	return screen.RunResult{Results: res.Results, Held: res.Held}, err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// backedOut turns a flow's "the person said no" into the page's quiet ending.
func backedOut(err error) error {
	if errors.Is(err, core.ErrBack) {
		return screen.ErrBackedOut
	}
	return err
}

// attach connects the window to the pages that change by themselves.
func (a *mfy) attach(v *view) {
	a.v = v
	v.host.OnAction = a.onAction
	redraw := throttled(120*time.Millisecond, func() {
		uiPost(func() {
			if t := v.host.View().Screen.Title; t == "tools" {
				v.draw(v.host.View())
			}
		})
	})
	a.tools.OnChange = redraw
	v.tools = a.tools
}

// throttled calls f at most every d, with one trailing call so the last change is shown.
func throttled(d time.Duration, f func()) func() {
	var mu sync.Mutex
	var last time.Time
	var pending bool
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if wait := d - time.Since(last); wait > 0 {
			if !pending {
				pending = true
				time.AfterFunc(wait, func() {
					mu.Lock()
					pending, last = false, time.Now()
					mu.Unlock()
					f()
				})
			}
			return
		}
		last = time.Now()
		go f()
	}
}

// ---- the Library page ------------------------------------------------------------------

var libraryTitles = map[string]string{
	screen.LibAll:        "Reading your accounts",
	screen.LibYouTube:    "Reading your YouTube Music library",
	screen.LibBandcamp:   "Reading your Bandcamp purchases",
	screen.LibTakeout:    "Reading your export",
	screen.LibAppleMatch: "Finding your library on Apple Music",
}

// startLibrary runs an import as the Downloads page's run: it asks its questions in dialogs,
// and what it downloads is the live queue. The page is shown at once.
func (a *mfy) startLibrary(id string) {
	f := a.svc.library[id]
	if f == nil {
		return
	}
	err := a.dl.StartFlow(libraryTitles[id], func(ctx context.Context, sink jobs.Sink) (screen.RunResult, error) {
		o := a.options()
		o.Sink = sink
		return screen.RunResult{}, backedOut(f(ctx, ui.NewWriter(discard{}), o, a.prompter()))
	})
	uiPost(func() {
		if err != nil {
			dialog.ShowInformation("", "A download is already running. Stop it, or wait until it is finished, before starting an import.", a.win)
			return
		}
		a.v.openTab("downloads")
	})
}

// ---- the Tools page --------------------------------------------------------------------

func (a *mfy) startTool(id string) {
	f := a.svc.tools[id]
	if f == nil {
		return
	}
	title := id
	for _, it := range screen.ToolItems() {
		if it.ID == id {
			title = it.Label
		}
	}
	_ = a.tools.StartTask(title, func(ctx context.Context, log io.Writer) error {
		return backedOut(f(ctx, ui.NewWriter(log), a.options(), a.prompter()))
	})
}

// ---- sign-ins -----------------------------------------------------------------------------

// onAction runs the work a Settings or Connections row asked for.
func (a *mfy) onAction(action string) {
	if action == "test" { // Settings' Test VPN: the Tools page runs it and shows the output
		go func() {
			uiPost(func() { a.v.openTab("tools") })
			a.startTool(screen.ToolVPNTest)
		}()
		return
	}
	f := a.svc.account[action]
	if f == nil || !a.busy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.busy.Store(false)
		var out strings.Builder
		err := f(a.ctx, ui.NewWriter(&out), a.prompter())
		msg := resultText(out.String(), err)
		uiPost(func() {
			a.v.draw(a.v.host.Refresh())
			if msg != "" {
				dialog.ShowInformation("", msg, a.win)
			}
		})
	}()
}

// resultText is what a sign-in says when it is over: the lines it marked as good or bad, or
// the reason it failed. Backing out of it says nothing.
func resultText(out string, err error) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		for _, mark := range []string{"✓ ", "✗ ", "! "} {
			if rest, ok := strings.CutPrefix(l, mark); ok {
				lines = append(lines, rest)
			}
		}
	}
	if err != nil && !errors.Is(err, core.ErrBack) && !errors.Is(err, context.Canceled) {
		// the failure was also printed as a ✗ line; the error says it once
		return "Could not finish.\n\n" + capital(err.Error())
	}
	return capital(strings.Join(lines, "\n"))
}
