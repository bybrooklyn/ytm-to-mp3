//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/screen"
)

// The test driver has no UI thread: it runs whatever it is given on the caller's goroutine, and
// Fyne's internals are not safe for two at once. So the window's own work from other
// goroutines (a run's redraws, a dialog being shown) is queued, and the test goroutine does
// it when it next looks (pump), which is what the real UI thread does.
type uiJob struct {
	f    func()
	done chan struct{}
}

var uiq = make(chan uiJob, 1024)

func init() {
	uiDo = func(f func()) {
		j := uiJob{f, make(chan struct{})}
		uiq <- j
		<-j.done
	}
	uiPost = func(f func()) { uiq <- uiJob{f, make(chan struct{})} }
}

// pump runs the queued UI work. Tests call it on their own goroutine only.
func pump() {
	for {
		select {
		case j := <-uiq:
			j.f()
			close(j.done)
		default:
			return
		}
	}
}

// fakeRun stands in for core.Download: it records what it was given, then waits until the
// test finishes it (or its context is cancelled).
type fakeRun struct {
	mu     sync.Mutex
	calls  [][]string
	finish chan screen.RunResult
}

func newFakeRun() *fakeRun { return &fakeRun{finish: make(chan screen.RunResult, 1)} }

func (f *fakeRun) run(ctx context.Context, inputs []string, sink jobs.Sink) (screen.RunResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, inputs)
	f.mu.Unlock()
	select {
	case r := <-f.finish:
		return r, nil
	case <-ctx.Done():
		return screen.RunResult{}, ctx.Err()
	}
}

func (f *fakeRun) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func downloadsView(t *testing.T, variant fyne.ThemeVariant, size fyne.Size) (*view, *screen.Downloads, *fakeRun) {
	t.Helper()
	fr := newFakeRun()
	dl := screen.NewDownloads(fr.run, func() screen.DownloadSettings {
		return screen.DownloadSettings{Folder: "~/Music", Format: "opus", Extras: "also: complete albums, fix tags, fetch lyrics  (change in settings)"}
	})
	host := screen.NewHost(
		&screen.Tab{Name: "downloads", Keep: true, Make: func() screen.Model { return dl }},
		&screen.Tab{Name: "connections", Make: func() screen.Model { return screen.NewConnections(nil, 0) }},
		&screen.Tab{Name: "settings", Keep: true, Make: func() screen.Model {
			return screen.NewSettings(config.Config{}, false, "", 0, func(config.Config) error { return nil }, func(string) error { return nil })
		}},
	)
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(ytmTheme{Theme: theme.DefaultTheme(), Force: variant, Forced: true})
	win := test.NewWindow(nil)
	t.Cleanup(win.Close)
	win.Resize(size)
	v := newView(win, host)
	v.attachDownloads(dl)
	return v, dl, fr
}

// walk visits every object under o, going into widgets through their renderers.
func walk(o fyne.CanvasObject, f func(fyne.CanvasObject)) {
	f(o)
	switch c := o.(type) {
	case *fyne.Container:
		for _, x := range c.Objects {
			walk(x, f)
		}
	case fyne.Widget:
		for _, x := range test.WidgetRenderer(c).Objects() {
			walk(x, f)
		}
	}
}

func texts(root fyne.CanvasObject) string {
	var b strings.Builder
	walk(root, func(o fyne.CanvasObject) {
		switch t := o.(type) {
		case *canvas.Text:
			b.WriteString(t.Text + "\n")
		case *widget.Label:
			b.WriteString(t.Text + "\n")
		}
	})
	return b.String()
}

// tapText taps the clickable that has this exact text inside it.
func tapText(t *testing.T, v *view, text string) {
	t.Helper()
	var target fyne.Tappable
	pump()
	root := v.win.Canvas().Content()
	walk(root, func(o fyne.CanvasObject) {
		if c, ok := o.(*click); ok && target == nil {
			if strings.Contains(texts(c.content), text) && strings.TrimSpace(texts(c.content)) == text {
				target = c
			}
		}
	})
	if target == nil {
		t.Fatalf("no button %q in\n%s", text, page(v))
	}
	test.Tap(target)
}

// page is every text on the window now. The view's lock keeps it from reading while the run's
// goroutine redraws (a test driver runs UI callbacks on whichever goroutine made them).
func page(v *view) string {
	pump()
	return texts(v.win.Canvas().Content())
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		pump()
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDownloadsPageStartProgressStopAndSummary(t *testing.T) {
	v, dl, fr := downloadsView(t, theme.VariantDark, wide)

	// Empty: the page says what to do and Start is not offered as ready.
	p := page(v)
	for _, want := range []string{"Downloads", "Add music", "Saving to ~/Music as opus.", "Change in Settings", "Browse..."} {
		if !strings.Contains(p, want) {
			t.Errorf("empty page lacks %q:\n%s", want, p)
		}
	}
	if err := dl.Start(); err == nil {
		t.Error("starting with nothing typed should be refused")
	}

	// Paste, then Start.
	test.Type(v.entry, "https://youtu.be/aaaaaaaaaaa")
	if v.dl.Snapshot().Input != "https://youtu.be/aaaaaaaaaaa" {
		t.Fatalf("the text area does not reach the model: %q", v.dl.Snapshot().Input)
	}
	tapText(t, v, "Start")
	waitUntil(t, "run started", func() bool { return fr.count() == 1 && dl.Running() })

	// Events arrive: the queue lists them, grouped by source, with progress.
	for i, label := range []string{"Daft Punk - One More Time", "Daft Punk - Digital Love", "Boards of Canada - Roygbiv"} {
		dl.Emit(jobs.Event{Kind: jobs.Queued, ID: string(rune('a' + i)), Label: label, Source: "youtube"})
	}
	dl.Emit(jobs.Event{Kind: jobs.Queued, ID: "x", Label: "Air - La femme d'argent", Source: "apple"})
	dl.Emit(jobs.Event{Kind: jobs.Started, ID: "a"})
	dl.Emit(jobs.Event{Kind: jobs.Progress, ID: "a", Bytes: 2 << 20, Total: 8 << 20})
	dl.Emit(jobs.Event{Kind: jobs.Done, ID: "b", Path: "/m/b.opus", Quality: "opus"})
	dl.Emit(jobs.Event{Kind: jobs.Failed, ID: "c", Reason: "Video unavailable"})
	waitUntil(t, "queue drawn", func() bool { return strings.Contains(page(v), "25%") })
	p = page(v)
	for _, want := range []string{"Queue", "Downloading", "YouTube", "Apple Music", "Daft Punk - One More Time", "2.0 MB of 8.0 MB", "25%", "Done", "Failed", "Video unavailable", "Waiting", "Stop", "2 of 4"} {
		if !strings.Contains(p, want) {
			t.Errorf("running page lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Retry failed") {
		t.Error("retry is for the end of the run")
	}

	// Stop: the run's context ends; the page says so and offers Resume.
	tapText(t, v, "Stop")
	waitUntil(t, "stopped", func() bool { return dl.Snapshot().State == "stopped" })
	waitUntil(t, "stopped page", func() bool { return strings.Contains(page(v), "Resume") })
	if p = page(v); !strings.Contains(p, "Stopped early") || !strings.Contains(p, "What finished is kept") {
		t.Errorf("stopped page:\n%s", p)
	}

	// Resume runs the same inputs; this time it finishes with a failure that may pass.
	tapText(t, v, "Resume")
	waitUntil(t, "second run", func() bool { return fr.count() == 2 && dl.Running() })
	if fr.calls[1][0] != "https://youtu.be/aaaaaaaaaaa" {
		t.Errorf("resume inputs: %v", fr.calls[1])
	}
	fr.finish <- screen.RunResult{Held: 3, Results: []jobs.Result{
		{ID: "a", Label: "Daft Punk - One More Time", Kind: jobs.Done},
		{ID: "b", Label: "Daft Punk - Digital Love", Kind: jobs.Failed, Reason: "connection reset", Retryable: true},
		{ID: "c", Label: "Boards of Canada - Roygbiv", Kind: jobs.Failed, Reason: "Video unavailable"},
	}}
	waitUntil(t, "summary", func() bool { return strings.Contains(page(v), "Retry failed") })
	p = page(v)
	for _, want := range []string{"Summary", "Downloaded", "Already in your library", "Failed", "connection reset  (1)", "Video unavailable  (1)", "1 song may work on another try."} {
		if !strings.Contains(p, want) {
			t.Errorf("summary lacks %q:\n%s", want, p)
		}
	}

	// Retry failed runs them again.
	tapText(t, v, "Retry failed")
	waitUntil(t, "third run", func() bool { return fr.count() == 3 })
	fr.finish <- screen.RunResult{}
	waitUntil(t, "idle", func() bool { return !dl.Running() })
}

func TestSettingsLinkOpensSettings(t *testing.T) {
	v, _, _ := downloadsView(t, theme.VariantLight, wide)
	tapText(t, v, "Change in Settings")
	if got := v.host.View().Screen.Title; got != "settings" {
		t.Fatalf("title %q", got)
	}
}

// A question asked mid-run is a native dialog; the answer reaches the run, and stopping the
// run closes the dialog.
func TestPrompterShowsDialogs(t *testing.T) {
	v, _, _ := downloadsView(t, theme.VariantDark, wide)
	p := dialogPrompter{win: v.win}
	answer := make(chan bool, 1)
	go func() {
		ok, _ := p.Confirm(context.Background(), core.Question{
			Title:   "Download them too?",
			Detail:  "120 more songs from 14 albums, about 1.2 GB.",
			Default: false,
		})
		answer <- ok
	}()
	waitUntil(t, "dialog", func() bool { return topOverlay(v) != nil })
	top := topOverlay(v)
	if s := texts(top); !strings.Contains(s, "Download them too?") || !strings.Contains(s, "1.2 GB") {
		t.Errorf("dialog text:\n%s", s)
	}
	var yes *widget.Button
	walk(top, func(o fyne.CanvasObject) {
		if b, ok := o.(*widget.Button); ok && b.Text == "Yes" {
			yes = b
		}
	})
	if yes == nil {
		t.Fatal("no Yes button")
	}
	test.Tap(yes)
	var ok bool
	waitUntil(t, "the answer", func() bool {
		select {
		case ok = <-answer:
			return true
		default:
			return false
		}
	})
	if !ok {
		t.Error("Yes should answer true")
	}

	// cancelled run: the dialog goes away and the default comes back with the error
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Confirm(ctx, core.Question{Title: "Try again?", Yes: "Try again", No: "Not now"})
		done <- err
	}()
	waitUntil(t, "second dialog", func() bool { return topOverlay(v) != nil })
	cancel()
	var cerr error
	waitUntil(t, "the question to end", func() bool {
		select {
		case cerr = <-done:
			return true
		default:
			return false
		}
	})
	if cerr == nil {
		t.Error("a cancelled run should get the context's error")
	}
	waitUntil(t, "dialog closed", func() bool { return topOverlay(v) == nil })
}

// Screenshots of the page in its states. MFY_SHOT_DIR=/dir writes them for a look.
func TestDownloadsScreenshots(t *testing.T) {
	dir := appname.Getenv("SHOT_DIR")
	if dir == "" {
		t.Skip("set MFY_SHOT_DIR=/dir")
	}
	events := func(dl *screen.Downloads) {
		songs := []struct{ src, label string }{
			{"youtube", "Daft Punk - One More Time"}, {"youtube", "Daft Punk - Digital Love"}, {"youtube", "Boards of Canada - Roygbiv"},
			{"youtube", "Massive Attack - Teardrop"}, {"youtube", "Portishead - Glory Box"}, {"youtube", "Burial - Archangel"},
			{"apple", "Air - La femme d'argent"}, {"apple", "Air - Kelly Watch the Stars"}, {"apple", "Moby - Porcelain"},
		}
		for i, s := range songs {
			dl.Emit(jobs.Event{Kind: jobs.Queued, ID: string(rune('a' + i)), Label: s.label, Source: s.src})
		}
	}
	running := func(dl *screen.Downloads, fr *fakeRun) {
		dl.SetInput("https://music.youtube.com/playlist?list=PLabc\nhttps://music.apple.com/us/album/moon-safari/1\n~/Downloads/library.csv")
		_ = dl.Start()
		waitUntil(t, "started", dl.Running)
		events(dl)
		dl.Emit(jobs.Event{Kind: jobs.Done, ID: "a", Path: "/m/a.opus", Quality: "opus 160k"})
		dl.Emit(jobs.Event{Kind: jobs.Done, ID: "b", Path: "/m/b.opus", Quality: "opus 160k"})
		dl.Emit(jobs.Event{Kind: jobs.Started, ID: "c"})
		dl.Emit(jobs.Event{Kind: jobs.Progress, ID: "c", Bytes: 3 << 20, Total: 5 << 20})
		dl.Emit(jobs.Event{Kind: jobs.Started, ID: "d"})
		dl.Emit(jobs.Event{Kind: jobs.Stage, ID: "d", Stage: "tagging"})
		dl.Emit(jobs.Event{Kind: jobs.Failed, ID: "e", Reason: "Video unavailable"})
		dl.Emit(jobs.Event{Kind: jobs.Started, ID: "g"})
		dl.Emit(jobs.Event{Kind: jobs.Progress, ID: "g", Bytes: 9 << 20, Total: 10 << 20})
		dl.Emit(jobs.Event{Kind: jobs.Skipped, ID: "h", Reason: "already on disk"})
		dl.Emit(jobs.Event{Kind: jobs.Progress, ID: "c", Bytes: 3<<20 + 1, Total: 5 << 20})
	}
	finished := func(dl *screen.Downloads, fr *fakeRun) {
		running(dl, fr)
		fr.finish <- screen.RunResult{Held: 41, Results: []jobs.Result{
			{ID: "a", Kind: jobs.Done}, {ID: "b", Kind: jobs.Done}, {ID: "c", Kind: jobs.Done}, {ID: "d", Kind: jobs.Done},
			{ID: "e", Label: "Portishead - Glory Box", Kind: jobs.Failed, Reason: "Video unavailable"},
			{ID: "f", Label: "Burial - Archangel", Kind: jobs.Failed, Reason: "connection reset", Retryable: true},
			{ID: "g", Kind: jobs.Upgraded}, {ID: "h", Kind: jobs.Skipped}, {ID: "i", Label: "Moby - Porcelain", Kind: jobs.Failed, Reason: "connection reset", Retryable: true},
		}}
		waitUntil(t, "finished", func() bool { return dl.Snapshot().State == "done" })
	}
	for _, c := range []struct {
		name    string
		variant fyne.ThemeVariant
		size    fyne.Size
		do      func(*screen.Downloads, *fakeRun)
	}{
		{"downloads-empty-dark-wide", theme.VariantDark, wide, func(*screen.Downloads, *fakeRun) {}},
		{"downloads-running-dark-wide", theme.VariantDark, wide, running},
		{"downloads-running-light-wide", theme.VariantLight, wide, running},
		{"downloads-running-light-narrow", theme.VariantLight, fyne.NewSize(700, 760), running},
		{"downloads-finished-dark-wide", theme.VariantDark, fyne.NewSize(980, 900), finished},
		{"downloads-finished-light-wide", theme.VariantLight, fyne.NewSize(980, 900), finished},
		{"downloads-finished-dark-narrow", theme.VariantDark, fyne.NewSize(700, 900), finished},
		{"downloads-stopped-light-wide", theme.VariantLight, wide, func(dl *screen.Downloads, fr *fakeRun) {
			running(dl, fr)
			dl.Stop()
			waitUntil(t, "stopped", func() bool { return dl.Snapshot().State == "stopped" })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, dl, fr := downloadsView(t, c.variant, c.size)
			c.do(dl, fr)
			time.Sleep(50 * time.Millisecond)
			v.draw(v.host.View())
			writePNG(t, filepath.Join(dir, c.name+".png"), v)
		})
	}
}

// topOverlay is the dialog on show, read in step with the code that shows it.
func topOverlay(v *view) (o fyne.CanvasObject) {
	pump()
	if t := v.win.Canvas().Overlays().Top(); t != nil {
		o = t
	}
	return o
}
