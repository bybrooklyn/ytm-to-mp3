//go:build cgo

package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/screen"
)

func testView(t *testing.T, variant fyne.ThemeVariant, size fyne.Size) (*view, *screen.Host, *config.Config) {
	t.Helper()
	var last config.Config
	host := screen.NewHost(
		&screen.Tab{Name: "settings", Keep: true, Make: func() screen.Model {
			return screen.NewSettings(config.Config{}, false, "", 0,
				func(c config.Config) error { last = c; return nil }, func(string) error { return nil })
		}},
		&screen.Tab{Name: "connections", Make: func() screen.Model {
			return screen.NewConnections([]screen.Connection{
				{ID: "youtube", Name: "YouTube Music library", Status: "reads Firefox", Good: true, Action: "youtube", Button: "Change browser...",
					Help: "Your library is read from the browser that is signed in to music.youtube.com. Nothing is exported."},
				{ID: "apple", Name: "Apple Music", Status: "not signed in", Action: "applein", Button: "Sign in",
					Help: "Optional. Songs come as 256 kbps AAC, with your own Apple Music subscription."},
				{ID: "proton", Name: "Proton VPN", Status: "not signed in", Action: "signin", Button: "Sign in",
					Help: "Optional, but much faster: downloads go through several servers at once."},
				{ID: "spotify", Name: "Spotify", Status: "ready", Dim: true,
					Help: "No sign-in. Public albums, playlists and songs; the audio comes from Apple Music or YouTube."},
			}, 0)
		}},
	)
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(ytmTheme{Theme: theme.DefaultTheme(), Force: variant, Forced: true})
	win := test.NewWindow(nil)
	t.Cleanup(win.Close)
	win.Resize(size)
	return newView(win, host), host, &last
}

var wide = fyne.NewSize(980, 720)

func TestKeysAndClicksDriveTheHost(t *testing.T) {
	v, host, last := testView(t, theme.VariantDark, wide)
	c := v.win.Canvas()
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyDown})
	if !host.View().Screen.Rows[1].Selected {
		t.Fatal("down arrow should select row 2")
	}
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyRight})
	if last.Format == "" {
		t.Fatal("right arrow should step the format and save it")
	}
	c.OnTypedRune()('j') // the terminal UI's vi key works here too
	if !host.View().Screen.Rows[2].Selected {
		t.Fatal("j should move down")
	}
}

func TestTypingAFolderAndTabs(t *testing.T) {
	v, host, last := testView(t, theme.VariantDark, wide)
	c := v.win.Canvas()
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyReturn}) // edit the folder
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyBackspace})
	for _, r := range "/tmp/x" {
		c.OnTypedRune()(r)
	}
	if !host.View().Editing {
		t.Fatal("should be editing")
	}
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if !strings.HasSuffix(last.Out, "/x") && !strings.HasSuffix(last.Out, "\\x") {
		t.Fatalf("folder not saved: %q", last.Out)
	}
	one := 1
	v.send(screen.Event{Tab: &one})
	if host.View().Screen.Title != "connections" {
		t.Fatal("tab switch")
	}
	var acts []string
	host.OnAction = func(a string) { acts = append(acts, a) }
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyReturn}) // an action the window runs
	if len(acts) != 1 || acts[0] != "youtube" {
		t.Fatalf("actions = %v", acts)
	}
}

// The window's controls reach the model through Set events only.
func TestControlsSetValues(t *testing.T) {
	v, host, last := testView(t, theme.VariantLight, wide)
	rows := host.View().Screen.Rows
	if rows[2].Kind != screen.KindToggle || rows[1].Kind != screen.KindChoice || rows[0].Kind != screen.KindText {
		t.Fatalf("settings rows lack kinds: %+v", rows[:3])
	}
	v.send(screen.Event{Set: &screen.Set{Row: 2, Value: "off"}}) // Complete albums switch
	if !last.NoAlbums {
		t.Fatal("switch off should store NoAlbums")
	}
	v.send(screen.Event{Set: &screen.Set{Row: 1, Value: "flac"}}) // format dropdown
	if last.Format != "flac" {
		t.Fatalf("format = %q", last.Format)
	}
	v.send(screen.Event{Set: &screen.Set{Row: 12, Step: 1}}) // parallel downloads stepper
	if last.Jobs != 1 {
		t.Fatalf("jobs = %d", last.Jobs)
	}
	v.send(screen.Event{Set: &screen.Set{Row: 0, Value: "/tmp/mfetchy-music"}}) // Browse result
	if !strings.HasSuffix(last.Out, "mfetchy-music") {
		t.Fatalf("folder = %q", last.Out)
	}
}

func TestButtonPressReachesTheWindowsActionHandler(t *testing.T) {
	v, host, _ := testView(t, theme.VariantDark, wide)
	var acts []string
	host.OnAction = func(a string) { acts = append(acts, a) }
	one := 1
	v.send(screen.Event{Tab: &one})
	v.send(screen.Event{Set: &screen.Set{Row: 1, Press: true}})
	if len(acts) != 1 || acts[0] != "applein" {
		t.Fatalf("actions = %v", acts)
	}
}

// MFY_SHOT=/path.png writes what the window looks like; MFY_SHOT_DIR=/dir writes a set
// (light and dark, wide and narrow, settings and connections) for a look.
func TestScreenshot(t *testing.T) {
	if p := appname.Getenv("SHOT"); p != "" {
		v, _, _ := testView(t, theme.VariantDark, wide)
		v.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyDown})
		writePNG(t, p, v)
	}
	dir := appname.Getenv("SHOT_DIR")
	if dir == "" {
		t.Skip("set MFY_SHOT_DIR=/dir")
	}
	for _, c := range []struct {
		name    string
		variant fyne.ThemeVariant
		size    fyne.Size
		do      func(*view)
	}{
		{"settings-dark-wide", theme.VariantDark, wide, func(v *view) { v.key(fyne.KeyDown) }},
		{"settings-light-wide", theme.VariantLight, wide, func(v *view) { v.key(fyne.KeyDown) }},
		{"settings-light-narrow", theme.VariantLight, fyne.NewSize(700, 640), func(v *view) {}},
		{"settings-dark-narrow", theme.VariantDark, fyne.NewSize(700, 640), func(v *view) { v.key(fyne.KeyDown); v.key(fyne.KeyDown); v.key(fyne.KeyDown) }},
		{"settings-light-editing", theme.VariantLight, wide, func(v *view) { v.key(fyne.KeyReturn) }},
		{"settings-dark-bottom", theme.VariantDark, fyne.NewSize(980, 560), func(v *view) {
			for range 12 {
				v.key(fyne.KeyDown)
			}
		}},
		{"connections-dark-wide", theme.VariantDark, wide, func(v *view) { zero := 1; v.send(screen.Event{Tab: &zero}) }},
		{"connections-light-wide", theme.VariantLight, wide, func(v *view) {
			one := 1
			v.send(screen.Event{Tab: &one})
			v.key(fyne.KeyDown)
			v.key(fyne.KeyReturn) // the sign-in notice
		}},
		{"connections-light-narrow", theme.VariantLight, fyne.NewSize(700, 640), func(v *view) { one := 1; v.send(screen.Event{Tab: &one}) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, _, _ := testView(t, c.variant, c.size)
			c.do(v)
			writePNG(t, filepath.Join(dir, c.name+".png"), v)
		})
	}
}

func (v *view) key(k fyne.KeyName) { v.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: k}) }

func writePNG(t *testing.T, path string, v *view) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, v.win.Canvas().Capture()); err != nil {
		t.Fatal(err)
	}
}
