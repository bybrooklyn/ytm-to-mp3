package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/ytmusic"
)

// liveLibraryFlow reads the YouTube Music library from a browser that is signed in to it,
// shows what it found and downloads it, instead of going through a Takeout export.
func liveLibraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	lib, err := readLiveLibrary(ctx, o.prompt)
	if err != nil {
		return err
	}
	fmt.Printf("\n  %s %s songs %s\n    %s %s %s %s\n\n", ui.OK("✓"), ui.Bold(fmt.Sprint(len(lib.Tracks))),
		ui.Dim(fmt.Sprintf("(%d liked · %d saved to the library · %s · %s)", lib.Liked, lib.Saved,
			count(lib.Albums, "album"), count(lib.Playlists, "playlist"))),
		ui.Dim("saving to"), tilde(o.out), ui.Dim("as"), ui.Cyan(o.format))
	if lib.Skipped > 0 {
		fmt.Printf("    %s%s couldn't be read and were left out%s\n", cDim, count(lib.Skipped, "album or playlist"), cReset)
	}
	fmt.Printf("    %s%s%s\n\n", cDim, extras(o), cReset)
	ok, err := o.prompt.Confirm(ctx, Question{Title: "Download?", Yes: "Download", No: "Back", Default: true})
	if err != nil {
		return err
	}
	if !ok {
		return ErrBack
	}
	o.preset = lib.Tracks
	_, err = runDownload(ctx, u, o, nil, nil)
	return err
}

// ChooseYouTubeBrowser asks which browser is signed in to YouTube Music and remembers the
// answer (only the browser's name, never its cookies).
func ChooseYouTubeBrowser(ctx context.Context, p Prompter) error {
	_, err := pickBrowser(ctx, p)
	return err
}

// ChooseBandcampBrowser asks which browser is signed in to Bandcamp and remembers it.
func ChooseBandcampBrowser(ctx context.Context, p Prompter) error {
	_, err := pickBandcampBrowser(ctx, p)
	return err
}

// pickBrowser lets the user choose the browser signed in to YouTube Music.
func pickBrowser(ctx context.Context, pr Prompter) (browsercookies.Browser, error) {
	return pickBrowserFor(ctx, pr, browserPurpose{
		title: "Which browser is signed in to YouTube Music?",
		desc:  "mfetchy reads that browser's youtube.com sign-in once, only to read your library, and keeps it in memory.\nNothing is saved but the browser's name. Requests go from your own connection, not the VPN.\nThe browser must have music.youtube.com open or recently used (a signed-out one won't work).",
		get:   func(c config.Config) string { return c.YouTubeBrowser },
		set:   func(c *config.Config, id string) { c.YouTubeBrowser = id },
		none:  "no supported browser found (Firefox, Chrome, Chromium, Brave, Edge, ...): export from Google Takeout instead",
	})
}

// pickBandcampBrowser lets the user choose the browser signed in to Bandcamp.
func pickBandcampBrowser(ctx context.Context, pr Prompter) (browsercookies.Browser, error) {
	return pickBrowserFor(ctx, pr, browserPurpose{
		title: "Which browser is signed in to Bandcamp?",
		desc:  "mfetchy reads that browser's bandcamp.com sign-in only to download music you have bought in its original\nquality (FLAC when offered). It stays in memory and goes only to Bandcamp; nothing is saved but the browser's name.\nMusic you haven't bought still streams at 128 kbps.",
		get:   func(c config.Config) string { return c.BandcampBrowser },
		set:   func(c *config.Config, id string) { c.BandcampBrowser = id },
		none:  "no supported browser found (Firefox, Chrome, Chromium, Brave, Edge, ...)",
	})
}

// browserPurpose says what a browser is being picked for and where the choice is remembered.
type browserPurpose struct {
	title, desc, none string
	get               func(config.Config) string
	set               func(*config.Config, string)
}

// pickBrowserFor lets the user choose a browser (the remembered one first) and remembers it.
func pickBrowserFor(ctx context.Context, pr Prompter, p browserPurpose) (browsercookies.Browser, error) {
	browsers := browsercookies.Detect()
	if len(browsers) == 0 {
		return browsercookies.Browser{}, errors.New(p.none)
	}
	cfg := config.Load()
	remembered := p.get(cfg)
	// The remembered browser goes first.
	for i, b := range browsers {
		if b.ID == remembered && i > 0 {
			browsers[0], browsers[i] = browsers[i], browsers[0]
			break
		}
	}
	var opts []Option
	for i, b := range browsers {
		label := "Use " + b.Name
		if i == 0 && b.ID == remembered {
			label += " (last time)"
		}
		if b.Note != "" {
			label += " (" + b.Note + ")"
		}
		opts = append(opts, Option{Value: b.ID, Label: label})
	}
	pick, err := pr.Select(ctx, p.title, p.desc, opts, browsers[0].ID)
	if err != nil {
		return browsercookies.Browser{}, err
	}
	var chosen browsercookies.Browser
	for _, b := range browsers {
		if b.ID == pick {
			chosen = b
		}
	}
	if remembered != pick {
		p.set(&cfg, pick)
		_ = cfg.Save()
	}
	return chosen, nil
}

// readLiveLibrary picks a browser, reads its YouTube sign-in and the library behind it.
// The cookies stay in memory.
func readLiveLibrary(ctx context.Context, pr Prompter) (*ytmusic.Library, error) {
	chosen, err := pickBrowser(ctx, pr)
	if err != nil {
		return nil, err
	}
	var lib *ytmusic.Library
	err = pr.Working(ctx, "Reading your YouTube Music library from "+chosen.Name+"...", func(ctx context.Context) error {
		cs, err := chosen.Import()
		if err != nil {
			return err
		}
		c, err := ytmusic.New(&http.Client{Timeout: 45 * time.Second}, cs)
		if err != nil {
			return err
		}
		lib, err = c.Library(ctx, ytmusic.Options{Albums: true, Playlists: true})
		return err
	})
	if err != nil {
		if errors.Is(err, browsercookies.ErrNotSignedIn) {
			return nil, fmt.Errorf("%s isn't signed in to YouTube: open music.youtube.com there, sign in, and try again", chosen.Name)
		}
		return nil, err
	}
	return lib, nil
}
