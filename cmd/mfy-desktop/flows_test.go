//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/wgconf"
)

// sandbox points every settings and cache folder at a temporary one, so no test touches the
// real configuration or reads a real browser.
func sandbox(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	t.Setenv("HOME", d)
	t.Setenv("AppData", d)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(d, "cache"))
	return d
}

// appView is the whole window: the real tabs and models, the real dialogs, and the services
// the test hands in (fakes that talk to the prompter the way the core flows do, or the real
// core flows where they need nothing but local files).
func appView(t *testing.T, variant fyne.ThemeVariant, size fyne.Size, svc services) (*mfy, *view) {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(ytmTheme{Theme: theme.DefaultTheme(), Force: variant, Forced: true})
	win := test.NewWindow(nil)
	t.Cleanup(win.Close)
	win.Resize(size)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ap := newApp(ctx, win, svc)
	host := screen.NewHost(screen.DesktopTabs(ap.dl, ap.library, ap.tools)...)
	v := newView(win, host)
	ap.attach(v)
	v.attachDownloads(ap.dl)
	return ap, v
}

func openTab(t *testing.T, v *view, name string) {
	t.Helper()
	v.openTab(name)
	if got := v.host.View().Screen.Title; got != name {
		t.Fatalf("tab %q opened %q", name, got)
	}
}

// ---- finding things in a dialog -----------------------------------------------------------

func dialogOpen(v *view) bool { return topOverlay(v) != nil }

func waitDialog(t *testing.T, v *view, contains string) fyne.CanvasObject {
	t.Helper()
	var top fyne.CanvasObject
	for i := 0; i < 300; i++ {
		pump()
		if top = topOverlay(v); top != nil && strings.Contains(flat(texts(top)), contains) {
			return top
		}
		time.Sleep(10 * time.Millisecond)
	}
	shown := "(nothing)"
	if top := topOverlay(v); top != nil {
		shown = texts(top)
	}
	t.Fatalf("timed out waiting for a dialog with %q; showing:\n%s\nthe page:\n%s", contains, shown, page(v))
	return nil
}

// flat joins wrapped lines back into one, so text can be looked for as it reads.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func waitNoDialog(t *testing.T, v *view) {
	t.Helper()
	waitUntil(t, "the dialog to close", func() bool { return topOverlay(v) == nil })
}

// tapDialog taps the button or clickable with exactly this text in the dialog on show.
func tapDialog(t *testing.T, v *view, text string) {
	t.Helper()
	pump()
	top := topOverlay(v)
	if top == nil {
		t.Fatalf("no dialog to tap %q in", text)
	}
	var target fyne.Tappable
	walk(top, func(o fyne.CanvasObject) {
		if target != nil {
			return
		}
		switch b := o.(type) {
		case *widget.Button:
			if b.Text == text {
				target = b
			}
		case *click:
			if strings.TrimSpace(texts(b.content)) == text {
				target = b
			}
		}
	})
	if target == nil {
		t.Fatalf("no %q in the dialog:\n%s", text, texts(top))
	}
	test.Tap(target)
	pump()
}

func dialogEntries(v *view) (es []*widget.Entry) {
	pump()
	if top := topOverlay(v); top != nil {
		walk(top, func(o fyne.CanvasObject) {
			if e, ok := o.(*widget.Entry); ok {
				es = append(es, e)
			}
		})
	}
	return es
}

// pressRow presses the button of the row with this label on the page.
func pressRow(t *testing.T, v *view, label string) {
	t.Helper()
	for i, r := range v.host.View().Screen.Rows {
		if r.Label == label {
			v.send(screen.Event{Set: &screen.Set{Row: i, Press: true}})
			return
		}
	}
	t.Fatalf("no row %q", label)
}

// ---- sign-ins -----------------------------------------------------------------------------

// A fake VPN sign-in with the same questions as the real one: a form with a masked
// password and a username that must not be empty.
func fakeSignIn(ctx context.Context, u *ui.UI, p core.Prompter) error {
	ans, err := p.Form(ctx, core.FormSpec{Fields: []core.Field{
		{Key: "user", Label: "Proton email or username", Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("required")
			}
			return nil
		}},
		{Key: "pass", Kind: core.FieldSecret, Label: "Password", Detail: "only sent to Proton, never saved"},
	}})
	if err != nil {
		return err
	}
	u.Good("signed in as %s", ans["user"])
	return nil
}

func TestSignInDialogMasksThePasswordShowsErrorsInlineAndReportsTheResult(t *testing.T) {
	sandbox(t)
	svc := services{account: map[string]func(context.Context, *ui.UI, core.Prompter) error{"signin": fakeSignIn}}
	_, v := appView(t, theme.VariantDark, wide, svc)
	openTab(t, v, "connections")
	pressRow(t, v, "VPN")

	top := waitDialog(t, v, "Proton email or username")
	es := dialogEntries(v)
	if len(es) != 2 || es[0].Password || !es[1].Password {
		t.Fatalf("the username is plain and only the password is masked: %d entries", len(es))
	}
	if !strings.Contains(texts(top), "only sent to Proton, never saved") {
		t.Errorf("dialog:\n%s", texts(top))
	}

	// OK with the username empty: the dialog comes back with the reason, what was typed kept
	test.Type(es[1], "hunter2")
	tapDialog(t, v, "OK")
	waitUntil(t, "the error", func() bool { top = topOverlay(v); return top != nil && strings.Contains(texts(top), "Required") })
	es = dialogEntries(v)
	if es[1].Text != "hunter2" {
		t.Errorf("the password typed so far should be kept, got %q", es[1].Text)
	}
	test.Type(es[0], "me@proton.me")
	tapDialog(t, v, "OK")

	// the result, in a dialog; closing it leaves the page refreshed
	waitDialog(t, v, "Signed in as me@proton.me")
	tapDialog(t, v, "OK")
	waitNoDialog(t, v)
}

func TestSignInCancelIsQuietAndFailureIsExplained(t *testing.T) {
	sandbox(t)
	svc := services{account: map[string]func(context.Context, *ui.UI, core.Prompter) error{
		"signin": fakeSignIn,
		"applein": func(ctx context.Context, u *ui.UI, p core.Prompter) error {
			return errors.New("that token doesn't work: 401")
		},
	}}
	ap, v := appView(t, theme.VariantLight, wide, svc)
	openTab(t, v, "connections")
	pressRow(t, v, "VPN")
	waitDialog(t, v, "Password")
	tapDialog(t, v, "Cancel")
	waitUntil(t, "flow to end", func() bool { return !ap.busy.Load() })
	if dialogOpen(v) {
		t.Error("backing out says nothing")
	}
	pressRow(t, v, "Apple Music")
	waitDialog(t, v, "token doesn't work: 401")
	if !strings.Contains(texts(topOverlay(v)), "Could not finish.") {
		t.Errorf("failure dialog:\n%s", texts(topOverlay(v)))
	}
	tapDialog(t, v, "OK")
}

func TestTidalCodeDialogLinkOpensTheBrowserAndCancelBacksOut(t *testing.T) {
	sandbox(t)
	var opened atomic.Value
	old := openURL
	openURL = func(l string) error { opened.Store(l); return nil }
	t.Cleanup(func() { openURL = old })

	release := make(chan error, 1)
	var got atomic.Pointer[error]
	svc := services{account: map[string]func(context.Context, *ui.UI, core.Prompter) error{
		"tidalin": func(ctx context.Context, u *ui.UI, p core.Prompter) error {
			ok, err := p.Confirm(ctx, core.Question{Title: "Sign in anyway?", Detail: "Tidal (your own subscription)\n\n" + core.TidalWarning,
				Yes: "Yes, I understand", No: "No"})
			if err != nil || !ok {
				return err
			}
			err = p.Wait(ctx, core.Info{Title: "Tidal sign-in", Link: "https://link.tidal.com/AB12CD", LinkLabel: "Open Tidal's sign-in page",
				Code: "AB12CD", Detail: "the link already has it filled in", Waiting: "waiting for you to approve it in the browser"},
				func(ctx context.Context) error {
					select {
					case e := <-release:
						return e
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			got.Store(&err)
			if err == nil {
				u.Good("Signed in to Tidal (country DE). Lossless needs a HiFi plan.")
			}
			return err
		},
	}}
	_, v := appView(t, theme.VariantDark, wide, svc)
	openTab(t, v, "settings")
	for i, r := range v.host.View().Screen.Rows {
		if strings.Contains(r.Label, "Tidal") {
			v.send(screen.Event{Set: &screen.Set{Row: i, Press: true}})
		}
	}
	// the terms come first and have to be accepted
	top := waitDialog(t, v, "Tidal's terms do not allow downloading")
	if !strings.Contains(texts(top), "Yes, I understand") {
		t.Errorf("terms dialog:\n%s", texts(top))
	}
	tapDialog(t, v, "Yes, I understand")

	top = waitDialog(t, v, "AB12CD")
	for _, want := range []string{"Tidal sign-in", "The link already has it filled in", "Open Tidal's sign-in page", "Copy code", "Cancel", "waiting for you to approve it in the browser"} {
		if !strings.Contains(texts(top), want) {
			t.Errorf("code dialog lacks %q:\n%s", want, texts(top))
		}
	}
	tapDialog(t, v, "Open Tidal's sign-in page")
	if opened.Load() != "https://link.tidal.com/AB12CD" {
		t.Errorf("opened %v", opened.Load())
	}

	// approved in the browser: the dialog closes by itself and the result is shown
	release <- nil
	waitDialog(t, v, "Signed in to Tidal (country DE)")
	tapDialog(t, v, "OK")
	waitNoDialog(t, v)

	// Cancel while waiting: the flow gets "back", the dialog goes, nothing is reported
	pressRow(t, v, "Tidal")
	waitDialog(t, v, "Tidal's terms")
	tapDialog(t, v, "Yes, I understand")
	waitDialog(t, v, "AB12CD")
	tapDialog(t, v, "Cancel")
	waitNoDialog(t, v)
	waitUntil(t, "back", func() bool { e := got.Load(); return e != nil && errors.Is(*e, core.ErrBack) })
}

// The real core flow for a WireGuard config, through the real dialogs: pick the provider,
// then a file chooser question with the path typed in, then the result.
func TestRealWireGuardImportThroughDialogs(t *testing.T) {
	sandbox(t)
	dir := t.TempDir()
	conf := "[Interface]\nPrivateKey = HQyoVBT/Dr7kqjfMTb0FrvqL0R4SgdQ6USHMkcM8t2k=\nAddress = 10.8.0.2/32\nDNS = 10.8.0.1\n\n" +
		"[Peer]\nPublicKey = bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.5:51820\n"
	if err := os.WriteFile(filepath.Join(dir, "exit.conf"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	_, v := appView(t, theme.VariantLight, wide, coreServices())
	openTab(t, v, "connections")
	pressRow(t, v, "VPN")

	top := waitDialog(t, v, "Download through a VPN?")
	for _, want := range []string{"Proton VPN", "Mullvad (account number only)", "My own WireGuard config (any provider)", "No VPN"} {
		if !strings.Contains(texts(top), want) {
			t.Errorf("provider list lacks %q:\n%s", want, texts(top))
		}
	}
	tapDialog(t, v, "My own WireGuard config (any provider)")
	tapDialog(t, v, "Choose")

	top = waitDialog(t, v, "WireGuard config")
	if !strings.Contains(texts(top), "Choose file...") || !strings.Contains(texts(top), "Choose folder...") {
		t.Errorf("file question:\n%s", texts(top))
	}
	// a path that is not there is refused inside the question
	es := dialogEntries(v)
	test.Type(es[0], filepath.Join(dir, "missing"))
	tapDialog(t, v, "OK")
	// (the importer's own complaint comes back as a retry question)
	waitDialog(t, v, "Try another path?")
	tapDialog(t, v, "Yes")
	waitDialog(t, v, "WireGuard config")
	es = dialogEntries(v)
	test.Type(es[0], dir)
	tapDialog(t, v, "OK")

	waitDialog(t, v, "Imported 1 config file")
	tapDialog(t, v, "OK")
	waitNoDialog(t, v)
	if p := wgconf.New(); !p.SignedIn() || p.Files() != 1 {
		t.Fatalf("imported %d files", p.Files())
	}
	waitUntil(t, "the page to show it", func() bool {
		for _, r := range v.host.View().Screen.Rows {
			if r.Label == "VPN" && strings.Contains(r.Value, "WireGuard") {
				return true
			}
		}
		return false
	})
}

// The real YouTube Premium setup: the warning has to be accepted before anything else.
func TestRealPremiumSetupAsksForTheWarningFirst(t *testing.T) {
	sandbox(t)
	_, v := appView(t, theme.VariantDark, wide, coreServices())
	openTab(t, v, "connections")
	pressRow(t, v, "YouTube Premium upgrade")
	top := waitDialog(t, v, "Turn it on anyway?")
	if !strings.Contains(texts(top), "EXPERIMENTAL") || !strings.Contains(texts(top), "Yes, I understand") {
		t.Errorf("warning dialog:\n%s", texts(top))
	}
	tapDialog(t, v, "No")
	waitNoDialog(t, v)
	waitUntil(t, "idle", func() bool { return true })
	if config.Load().Premium {
		t.Error("declining the warning must not turn anything on")
	}
}

// ---- the Library page -----------------------------------------------------------------------

func TestLibraryPageListsTheImportsAndStartingOneShowsDownloads(t *testing.T) {
	sandbox(t)
	var ran sync.Map
	preview := func(id string) flowFn {
		return func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
			ran.Store(id, true)
			ok, err := p.Confirm(ctx, core.Question{Title: "Download?", Detail: "2 songs: 1 liked, 1 saved.\nSaving to ~/Music as opus.", Yes: "Download", No: "Back", Default: true})
			if err != nil {
				return err
			}
			if !ok {
				return core.ErrBack
			}
			o.Sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "a", Label: "Daft Punk - One More Time", Source: "youtube"})
			o.Sink.Emit(jobs.Event{Kind: jobs.Queued, ID: "b", Label: "Air - Kelly Watch the Stars", Source: "apple"})
			o.Sink.Emit(jobs.Event{Kind: jobs.Done, ID: "a", Path: "/m/a.opus", Quality: "opus"})
			o.Sink.Emit(jobs.Event{Kind: jobs.Failed, ID: "b", Reason: "Apple Music doesn't offer it to your account"})
			return nil
		}
	}
	svc := services{library: map[string]flowFn{screen.LibYouTube: preview(screen.LibYouTube), screen.LibAll: preview(screen.LibAll)}}
	_, v := appView(t, theme.VariantLight, wide, svc)
	openTab(t, v, "library")
	p := page(v)
	for _, want := range []string{"Library", "Download from all my accounts", "YouTube Music library", "Bandcamp purchases", "Import a Takeout export",
		"Find my YouTube library on Apple Music", "Read library...", "Choose export...", "Reads every account that is set up"} {
		if !strings.Contains(p, want) {
			t.Errorf("library page lacks %q:\n%s", want, p)
		}
	}

	pressRow(t, v, "YouTube Music library")
	// the page switches to Downloads at once, saying what it is doing; the question is a dialog
	waitUntil(t, "Downloads", func() bool { return v.host.View().Screen.Title == "downloads" })
	top := waitDialog(t, v, "2 songs: 1 liked, 1 saved.")
	if !strings.Contains(page(v), "Reading your YouTube Music library") {
		t.Errorf("downloads page while asking:\n%s", page(v))
	}
	tapDialog(t, v, "Download")
	_ = top
	waitUntil(t, "finished", func() bool { return v.dl.Snapshot().State == "done" })
	p = page(v)
	for _, want := range []string{"Daft Punk - One More Time", "Air - Kelly Watch the Stars", "Apple Music doesn't offer it", "Summary", "Failed"} {
		if !strings.Contains(p, want) {
			t.Errorf("queue lacks %q:\n%s", want, p)
		}
	}

	// Back at the question: nothing starts, the page is as it was
	openTab(t, v, "library")
	pressRow(t, v, "Download from all my accounts")
	waitDialog(t, v, "Download?")
	tapDialog(t, v, "Back")
	waitUntil(t, "idle again", func() bool { return v.dl.Snapshot().State == "" })
	if s := v.dl.Snapshot(); s.Error != "" || s.Summary != nil {
		t.Errorf("backing out leaves no error or summary: %+v", s)
	}
}

func TestLibraryImportRefusedWhileADownloadRuns(t *testing.T) {
	sandbox(t)
	block := make(chan struct{})
	svc := services{library: map[string]flowFn{screen.LibYouTube: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
		<-block
		return nil
	}}}
	_, v := appView(t, theme.VariantLight, wide, svc)
	openTab(t, v, "library")
	pressRow(t, v, "YouTube Music library")
	waitUntil(t, "running", v.dl.Running)
	openTab(t, v, "library")
	pressRow(t, v, "YouTube Music library")
	waitDialog(t, v, "A download is already running")
	tapDialog(t, v, "OK")
	close(block)
	waitUntil(t, "done", func() bool { return !v.dl.Running() })
}

// The real Takeout flow up to the question about downloading: choosing another file, the
// preview in a dialog, and Back.
func TestRealTakeoutFlowPickFilePreviewAndBack(t *testing.T) {
	home := sandbox(t)
	csv := filepath.Join(t.TempDir(), "music library songs.csv") // not in a place that is searched
	if err := os.WriteFile(csv, []byte("Video ID,Song Title,Album Title,Artist Name 1\naaaaaaaaaaa,One,Alb,Artist\nbbbbbbbbbbb,Two,Alb,Artist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, v := appView(t, theme.VariantDark, wide, coreServices())
	openTab(t, v, "library")
	pressRow(t, v, "Import a Takeout export")

	top := waitDialog(t, v, "No export found yet")
	for _, want := range []string{"Choose another file or folder...", "I don't have one yet: show me how to get it", "Looked in Downloads, Desktop and Documents."} {
		if !strings.Contains(texts(top), want) {
			t.Errorf("export list lacks %q:\n%s", want, texts(top))
		}
	}
	tapDialog(t, v, "Choose another file or folder...")
	tapDialog(t, v, "Choose")
	waitDialog(t, v, "Where is it?")
	es := dialogEntries(v)
	test.Type(es[0], filepath.Join(home, "nope.zip"))
	tapDialog(t, v, "OK")
	waitDialog(t, v, "Can't find") // refused inside the question, which stays open
	es = dialogEntries(v)
	es[0].SetText(csv)
	tapDialog(t, v, "OK")

	top = waitDialog(t, v, "2 tracks")
	if !strings.Contains(texts(top), "Saving to") || !strings.Contains(texts(top), "Download") {
		t.Errorf("preview:\n%s", texts(top))
	}
	tapDialog(t, v, "Back")
	waitUntil(t, "back to idle", func() bool { return v.dl.Snapshot().State == "" })
}

// "I don't have one yet" is an information page with the steps and a button for the page.
func TestTakeoutGuideIsAnInfoPageWithALinkButton(t *testing.T) {
	sandbox(t)
	var opened atomic.Value
	old := openURL
	openURL = func(l string) error { opened.Store(l); return nil }
	t.Cleanup(func() { openURL = old })
	_, v := appView(t, theme.VariantLight, wide, coreServices())
	openTab(t, v, "library")
	pressRow(t, v, "Import a Takeout export")
	waitDialog(t, v, "No export found yet")
	tapDialog(t, v, "I don't have one yet: show me how to get it")
	tapDialog(t, v, "Choose")
	top := waitDialog(t, v, "Open takeout.google.com")
	for _, want := range []string{"Get your library from Google Takeout", "Deselect all", "YouTube and YouTube Music", "Create export", "Cancel"} {
		if !strings.Contains(flat(texts(top)), want) {
			t.Errorf("guide lacks %q:\n%s", want, texts(top))
		}
	}
	tapDialog(t, v, "Open takeout.google.com")
	if opened.Load() != "https://takeout.google.com/" {
		t.Errorf("opened %v", opened.Load())
	}
	tapDialog(t, v, "Cancel")
	waitNoDialog(t, v)
	waitUntil(t, "idle", func() bool { return v.dl.Snapshot().State == "" })
}

// ---- the Tools page -------------------------------------------------------------------------

func TestToolsRunWithLogAndCancel(t *testing.T) {
	sandbox(t)
	release := make(chan struct{})
	svc := services{tools: map[string]flowFn{
		screen.ToolDoctor: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
			u.Head("mfetchy dev · linux/amd64")
			u.Good("yt-dlp: /usr/bin/yt-dlp")
			u.Bad("https://www.youtube.com: connection refused")
			u.Hint("→ check your internet connection, proxy or firewall")
			return errors.New("1 problem(s) found")
		},
		screen.ToolUpdate: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
			u.Note("downloading yt-dlp 2026.11.01")
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}}
	ap, v := appView(t, theme.VariantDark, wide, svc)
	openTab(t, v, "tools")
	p := page(v)
	for _, want := range []string{"Tools", "Check that everything works", "Test VPN", "Update download tools", "Fix tags", "Tidy file names and merge duplicate folders",
		"Fetch lyrics for my library", "Write cover and .nfo files for my library"} {
		if !strings.Contains(p, want) {
			t.Errorf("tools page lacks %q:\n%s", want, p)
		}
	}

	// a check that finds a problem: the report is readable, the failure stated
	pressRow(t, v, "Check that everything works")
	waitUntil(t, "the report", func() bool { return strings.Contains(page(v), "Stopped with a problem") })
	p = page(v)
	for _, want := range []string{"Output", "Check that everything works", "yt-dlp: /usr/bin/yt-dlp", "connection refused", "check your internet connection", "1 problem(s) found", "Dismiss"} {
		if !strings.Contains(p, want) {
			t.Errorf("report lacks %q:\n%s", want, p)
		}
	}
	tapText(t, v, "Dismiss")
	waitUntil(t, "the output to go", func() bool { return !strings.Contains(page(v), "Output") })

	// a long run shows progress and can be cancelled
	pressRow(t, v, "Update download tools")
	waitUntil(t, "running", func() bool {
		return strings.Contains(page(v), "downloading yt-dlp") && strings.Contains(page(v), "Running")
	})
	pressRow(t, v, "Test VPN") // another tool while one runs: ignored
	if ap.tools.Screen().Task.Title != "Update download tools" {
		t.Error("a second tool must not replace the running one")
	}
	tapText(t, v, "Cancel")
	waitUntil(t, "cancelled", func() bool { return strings.Contains(page(v), "Cancelled") })
	close(release)
}

// The real Fix tags form, in a dialog: the folder is checked inline, the modes are a list,
// and Back ends it without touching anything.
func TestRealFixTagsFormDialog(t *testing.T) {
	home := sandbox(t)
	_, v := appView(t, theme.VariantLight, wide, coreServices())
	openTab(t, v, "tools")
	pressRow(t, v, "Fix tags")
	top := waitDialog(t, v, "Which folder?")
	for _, want := range []string{"Every audio file under it is checked.", "Show what it would change (writes nothing)", "Fix the tags",
		"Undo the last fix", "Look songs up on MusicBrainz?", "Online", "Offline"} {
		if !strings.Contains(texts(top), want) {
			t.Errorf("form lacks %q:\n%s", want, texts(top))
		}
	}
	es := dialogEntries(v)
	es[0].SetText(filepath.Join(home, "no such folder"))
	tapDialog(t, v, "Continue")
	waitDialog(t, v, "That isn't a folder")
	tapDialog(t, v, "Back") // the mode "Back" in the list
	es = dialogEntries(v)
	es[0].SetText(home)
	tapDialog(t, v, "Offline")
	tapDialog(t, v, "Continue")
	waitNoDialog(t, v)
	waitUntil(t, "the tool to end quietly", func() bool { return !v.tools.Running() })
	if s := v.host.View().Screen; s.Task != nil {
		t.Errorf("backing out leaves no output: %+v", s.Task)
	}
}

func TestSettingsTestVPNRunsOnTheToolsPage(t *testing.T) {
	sandbox(t)
	svc := services{tools: map[string]flowFn{
		screen.ToolVPNTest: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
			u.Item("Proton NL#12", "exits at 203.0.113.7")
			u.Item("Proton CH#3", "exits at 203.0.113.9")
			return nil
		},
	}}
	ap, v := appView(t, theme.VariantLight, wide, svc)
	ap.onAction("test")
	waitUntil(t, "tools page", func() bool { return v.host.View().Screen.Title == "tools" })
	waitUntil(t, "the exits", func() bool { return strings.Contains(page(v), "exits at 203.0.113.9") })
}

func TestResultText(t *testing.T) {
	if got := resultText("\n  ♪ Apple Music login\n  ✓ Signed in to Apple Music · store GB\n", nil); got != "Signed in to Apple Music · store GB" {
		t.Errorf("got %q", got)
	}
	if got := resultText("  ✗ nope\n", errors.New("not signed in; try again in Settings")); got != "Could not finish.\n\nNot signed in; try again in Settings" {
		t.Errorf("got %q", got)
	}
	if got := resultText("", core.ErrBack); got != "" {
		t.Errorf("backing out says nothing, got %q", got)
	}
}

// ---- screenshots ----------------------------------------------------------------------------

// MFY_SHOT_DIR=/dir writes every new page and dialog in light and dark, wide and narrow.
func TestNewScreensScreenshots(t *testing.T) {
	dir := appname.Getenv("SHOT_DIR")
	if dir == "" {
		t.Skip("set MFY_SHOT_DIR=/dir")
	}
	narrow := fyne.NewSize(700, 760)
	type shot struct {
		name string
		do   func(t *testing.T, ap *mfy, v *view)
	}
	svc := services{
		account: map[string]func(context.Context, *ui.UI, core.Prompter) error{
			"signin": fakeSignIn,
			"tidalin": func(ctx context.Context, u *ui.UI, p core.Prompter) error {
				return p.Wait(ctx, core.Info{Title: "Tidal sign-in", Link: "https://link.tidal.com/AB12CD", LinkLabel: "Open Tidal's sign-in page",
					Code: "AB12CD", Detail: "the link already has it filled in", Waiting: "waiting for you to approve it in the browser"},
					func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
			},
			"premium": core.PremiumSetup,
		},
		library: map[string]flowFn{
			screen.LibTakeout: core.TakeoutFlow,
			screen.LibAll: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
				_, err := p.Confirm(ctx, core.Question{Title: "Read the library of 3 accounts?", Yes: "Read", No: "Back", Default: true,
					Detail: "• Ready: YouTube Music (reads Firefox)\n• Ready: Apple Music (store GB)\n• Ready: Bandcamp (reads Firefox)\n• Skipped: Tidal (not signed in)\n\n" +
						"Each account is read only from its own service, from your own connection (not the VPN).\nBrowser sign-ins are read once, stay in memory and are never saved."})
				return err
			},
			screen.LibAppleMatch: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
				_, err := p.Select(ctx, "Download which songs from Apple Music?", "1,102 of 1,248 songs are on Apple Music: 1,040 sure, 62 less certain, 146 not found.",
					[]core.Option{{Value: "sure", Label: "The sure matches (1040)"}, {Value: "all", Label: "All matches, also the less certain ones (1102)"}, {Value: "none", Label: "Nothing for now"}}, "sure")
				return err
			},
			screen.LibBandcamp: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
				<-ctx.Done()
				return ctx.Err()
			},
			screen.LibYouTube: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
				_, err := p.Confirm(ctx, core.Question{Title: "Download?", Yes: "Download", No: "Back", Default: true,
					Detail: "1,248 songs: 912 liked, 336 saved to the library, 61 albums, 14 playlists.\nSaving to ~/Music as opus.\nAlso: complete albums, retry unavailable songs, fix tags, fetch lyrics."})
				return err
			},
		},
		tools: map[string]flowFn{
			screen.ToolDoctor: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
				u.Head("mfetchy dev · linux/amd64")
				u.Head("Settings")
				u.Good("/home/me/.config/mfetchy/config.json")
				u.Good("music folder /home/me/Music is writable")
				u.Head("Tools · mfetchy downloads any that are missing on first use")
				u.Good("yt-dlp: /usr/bin/yt-dlp · fallback for videos mfetchy's own client can't open, and Premium downloads")
				u.Good("ffmpeg: /usr/bin/ffmpeg · remuxes downloads and writes tags and cover art; makes mp3/flac")
				u.Note("deno: not on PATH · optional: helps yt-dlp with YouTube's JavaScript challenges")
				u.Head("Network")
				u.Good("https://github.com reachable")
				u.Bad("https://www.youtube.com: dial tcp: lookup www.youtube.com: no such host")
				u.Hint("→ check your internet connection, proxy or firewall")
				u.Styled("\n  ✗ 1 problem(s) found")
				return errors.New("1 problem(s) found")
			},
			screen.ToolUpdate: func(ctx context.Context, u *ui.UI, o core.Options, p core.Prompter) error {
				u.Note("fetching yt-dlp 2026.11.01")
				<-ctx.Done()
				return ctx.Err()
			},
			screen.ToolTag: core.TagFlow,
		},
	}
	variants := []struct {
		suffix string
		v      fyne.ThemeVariant
		size   fyne.Size
	}{{"dark-wide", theme.VariantDark, wide}, {"light-wide", theme.VariantLight, wide}, {"light-narrow", theme.VariantLight, narrow}, {"dark-narrow", theme.VariantDark, narrow}}
	shots := []shot{
		{"library", func(t *testing.T, ap *mfy, v *view) { openTab(t, v, "library") }},
		{"tools", func(t *testing.T, ap *mfy, v *view) { openTab(t, v, "tools") }},
		{"tools-report", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "tools")
			pressRow(t, v, "Check that everything works")
			waitUntil(t, "report", func() bool { return strings.Contains(page(v), "Stopped with a problem") })
		}},
		{"tools-running", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "tools")
			pressRow(t, v, "Update download tools")
			waitUntil(t, "running", func() bool { return strings.Contains(page(v), "fetching yt-dlp") })
		}},
		{"settings", func(t *testing.T, ap *mfy, v *view) { openTab(t, v, "settings") }},
		{"connections", func(t *testing.T, ap *mfy, v *view) { openTab(t, v, "connections") }},
		{"dialog-proton", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "connections")
			pressRow(t, v, "VPN")
			waitDialog(t, v, "Password")
			es := dialogEntries(v)
			test.Type(es[1], "hunter2")
			tapDialog(t, v, "OK")
			waitDialog(t, v, "Required")
		}},
		{"dialog-tidal", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "settings")
			for i, r := range v.host.View().Screen.Rows {
				if strings.Contains(r.Label, "Tidal") {
					v.send(screen.Event{Set: &screen.Set{Row: i, Press: true}})
				}
			}
			waitDialog(t, v, "AB12CD")
		}},
		{"dialog-premium-warning", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "connections")
			pressRow(t, v, "YouTube Premium upgrade")
			waitDialog(t, v, "EXPERIMENTAL")
		}},
		{"dialog-takeout-list", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "Import a Takeout export")
			waitDialog(t, v, "No export found yet")
		}},
		{"dialog-takeout-file", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "Import a Takeout export")
			waitDialog(t, v, "No export found yet")
			tapDialog(t, v, "Choose another file or folder...")
			tapDialog(t, v, "Choose")
			waitDialog(t, v, "Where is it?")
			es := dialogEntries(v)
			test.Type(es[0], "/home/me/Downloads/takeout-2026.zip")
			tapDialog(t, v, "OK")
			waitDialog(t, v, "Can't find")
		}},
		{"dialog-takeout-guide", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "Import a Takeout export")
			waitDialog(t, v, "No export found yet")
			tapDialog(t, v, "I don't have one yet: show me how to get it")
			tapDialog(t, v, "Choose")
			waitDialog(t, v, "Open takeout.google.com")
		}},
		{"dialog-library-preview", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "YouTube Music library")
			waitDialog(t, v, "1,248 songs")
		}},
		{"dialog-all-accounts", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "Download from all my accounts")
			waitDialog(t, v, "Read the library of 3 accounts?")
		}},
		{"dialog-apple-match", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "Find my YouTube library on Apple Music")
			waitDialog(t, v, "Download which songs from Apple Music?")
		}},
		{"downloads-reading", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "library")
			pressRow(t, v, "Bandcamp purchases")
			waitUntil(t, "downloads page", func() bool { return strings.Contains(page(v), "Reading your Bandcamp purchases") })
		}},
		{"dialog-fix-tags", func(t *testing.T, ap *mfy, v *view) {
			openTab(t, v, "tools")
			pressRow(t, v, "Fix tags")
			waitDialog(t, v, "Which folder?")
		}},
	}
	for _, s := range shots {
		for _, c := range variants {
			if strings.HasPrefix(s.name, "dialog-") && c.suffix == "dark-narrow" {
				continue
			}
			t.Run(s.name+"-"+c.suffix, func(t *testing.T) {
				sandbox(t)
				ap, v := appView(t, c.v, c.size, svc)
				s.do(t, ap, v)
				time.Sleep(30 * time.Millisecond)
				pump()
				v.draw(v.host.View())
				pump()
				writePNG(t, filepath.Join(dir, s.name+"-"+c.suffix+".png"), v)
			})
		}
	}
}
