//go:build cgo

package main

import (
	"image/png"
	"os"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/screen"
)

func testView(t *testing.T) (*view, *screen.Host, *config.Config) {
	t.Helper()
	var last config.Config
	host := screen.NewHost(
		&screen.Tab{Name: "settings", Keep: true, Make: func() screen.Model {
			return screen.NewSettings(config.Config{}, false, "", 0,
				func(c config.Config) error { last = c; return nil }, func(string) error { return nil })
		}},
		&screen.Tab{Name: "connections", Make: func() screen.Model {
			return screen.NewConnections([]screen.Connection{{ID: "a", Name: "Apple Music", Status: "not signed in", Action: "applein"}}, 0)
		}},
	)
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(ytmTheme{theme.DefaultTheme()})
	win := test.NewWindow(nil)
	t.Cleanup(win.Close)
	win.Resize(fyne.NewSize(900, 720))
	return newView(win, host), host, &last
}

func TestKeysAndClicksDriveTheHost(t *testing.T) {
	v, host, last := testView(t)
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
	v, host, last := testView(t)
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
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyReturn}) // an action a window can't run
	if host.View().Notice == "" {
		t.Fatal("expected a notice")
	}
}

// YTM_SHOT=/path.png writes what the window looks like, for a look.
func TestScreenshot(t *testing.T) {
	path := os.Getenv("YTM_SHOT")
	if path == "" {
		t.Skip("set YTM_SHOT=/path.png")
	}
	v, _, _ := testView(t)
	v.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyDown})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, v.win.Canvas().Capture()); err != nil {
		t.Fatal(err)
	}
}
