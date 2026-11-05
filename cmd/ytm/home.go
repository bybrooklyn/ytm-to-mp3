package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/export"
	"github.com/bybrooklyn/ytm-to-mp3/internal/spotify"
	"github.com/bybrooklyn/ytm-to-mp3/internal/takeout"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// errBack means "go back to the menu" from a sub-flow.
var errBack = errors.New("back")

// runHome is what plain `ytm` does in a terminal: a short menu that leads to the right
// flow, so nothing has to be remembered.
func runHome(ctx context.Context, u *ui.UI, o opts) error {
	if !config.Exists() {
		first := true
		if err := one(ctx, huh.NewConfirm().Title("Welcome to ytm. Set it up first? (takes a minute)").
			Description("Pick your music folder and sign in to Proton VPN. You can skip and change it later.").
			Affirmative("Set up").Negative("Skip").Value(&first)); err != nil {
			return err
		}
		if first {
			if err := runSetup(ctx, u, false); err != nil {
				return err
			}
			o = refreshOpts(o)
		}
	}
	for {
		fmt.Println(header(o))
		var choice string
		err := one(ctx, huh.NewSelect[string]().Title("What do you want to do?").Options(
			huh.NewOption("Download my YouTube Music library", "library"),
			huh.NewOption("Download a link (YouTube, Apple Music or Spotify)", "link"),
			huh.NewOption("Apple Music...", "apple"),
			huh.NewOption("Settings", "settings"),
			huh.NewOption("More tools...", "tools"),
			huh.NewOption("Quit", "quit"),
		).Value(&choice))
		if err != nil {
			return err
		}
		switch choice {
		case "library":
			err = libraryFlow(ctx, u, o)
		case "link":
			err = linkFlow(ctx, u, o)
		case "settings":
			if err = runSetup(ctx, u, false); err == nil {
				o = refreshOpts(o)
			}
			continue // settings are saved as you go; straight back to the menu
		case "tools":
			err = toolsMenu(ctx, u, o)
		case "apple":
			err = appleMenu(ctx, u, o)
		default:
			return nil
		}
		if errors.Is(err, errBack) {
			continue
		}
		if err != nil {
			return err
		}
		if choice == "library" || choice == "link" {
			return nil // a download ran; we're done
		}
		back := true
		if one(ctx, huh.NewConfirm().Title("Back to the menu?").Affirmative("Menu").Negative("Quit").Value(&back)) != nil || !back {
			return nil
		}
	}
}

// toolsMenu holds everything that isn't downloading.
func toolsMenu(ctx context.Context, u *ui.UI, o opts) error {
	var choice string
	if err := one(ctx, huh.NewSelect[string]().Title("More tools").Options(
		huh.NewOption("Fix tags in my music folder (MusicBrainz)", "tag"),
		huh.NewOption("Tidy file names (Artist/Album/01 - Title)", "tidy"),
		huh.NewOption("YouTube Premium upgrade now (experimental, slow)", "premium"),
		huh.NewOption("Update yt-dlp, ffmpeg and deno", "update"),
		huh.NewOption("Check that everything works", "doctor"),
		huh.NewOption("Back", "back"),
	).Value(&choice)); err != nil {
		return err
	}
	switch choice {
	case "tag":
		return tagFlow(ctx, u, o)
	case "tidy":
		return tidyFlow(ctx, u, o)
	case "premium":
		if !config.Load().Premium {
			if err := premiumSetup(ctx, u); err != nil || !config.Load().Premium {
				return err
			}
		}
		return premiumUpgrade(ctx, u, o, false)
	case "update":
		return deps.Update(ctx, func(f string, a ...any) { u.Printf(f, a...) })
	case "doctor":
		return runDoctor(ctx, u, o)
	}
	return errBack
}

// refreshOpts re-reads saved settings after setup, keeping flags the user passed.
func refreshOpts(o opts) opts {
	cfg := config.Load()
	if cfg.Out != "" {
		o.out = config.ExpandPath(cfg.Out)
	}
	if cfg.Format != "" {
		o.format = cfg.Format
	}
	o.albums, o.retry, o.tagAfter, o.premiumAfter = !cfg.NoAlbums, !cfg.NoRetry, !cfg.NoTagAfter, !cfg.NoPremiumAfter
	return o
}

// extras says, in one line, what a library download does besides downloading.
func extras(o opts) string {
	var on []string
	if o.albums {
		on = append(on, "complete albums")
	}
	if o.retry {
		on = append(on, "retry unavailable songs")
	}
	if o.tagAfter {
		on = append(on, "fix tags")
	}
	if o.premiumAfter {
		if config.Load().Premium {
			on = append(on, "Premium upgrade")
		} else {
			on = append(on, "Premium upgrade (once you sign in, in settings)")
		}
	}
	if len(on) == 0 {
		return "just download (extras are off in settings)"
	}
	return "also: " + strings.Join(on, ", ") + "  (change in settings)"
}

func header(o opts) string {
	pc := protonClient()
	cfg := config.Load()
	vpn := "VPN off"
	if pc.SignedIn() && !cfg.NoVPN {
		vpn = "Proton VPN, " + connectionsLabel(cfg, pc) + " connections"
	}
	return fmt.Sprintf("\n  \x1b[1mytm\x1b[0m  \x1b[2m%s  ·  %s  ·  %s\x1b[0m\n", tilde(o.out), o.format, vpn)
}

// tilde shortens a path under the home folder for display.
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return "~/" + rel
		}
	}
	return p
}

// libraryFlow finds (or helps get) a Takeout export, shows what's in it, and downloads it.
func libraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	src, err := chooseExport(ctx, u)
	if err != nil {
		return err
	}
	fmt.Printf("\n  \x1b[32m✓\x1b[0m %d tracks (%d with song details) in %s\n    saving to %s as %s\n\n",
		src.Tracks, src.WithMeta, src.Name(), tilde(o.out), o.format)
	fmt.Printf("    %s%s%s\n\n", cDim, extras(o), cReset)
	go_ := true
	if err := one(ctx, huh.NewConfirm().Title("Download?").Affirmative("Download").Negative("Back").Value(&go_)); err != nil {
		return err
	}
	if !go_ {
		return errBack
	}
	return download(ctx, u, o, src.Paths...)
}

// chooseExport finds the YouTube Music exports on this computer and lets you pick one
// (or another file, or shows how to get one). errBack = backed out.
func chooseExport(ctx context.Context, u *ui.UI) (takeout.Source, error) {
	var srcs []takeout.Source
	if err := working(ctx, "Looking for your export in Downloads, Desktop and Documents...", func(context.Context) error {
		srcs = takeout.Discover(export.SearchDirs())
		return nil
	}); err != nil {
		return takeout.Source{}, err
	}

	var opts []huh.Option[string]
	for i, s := range srcs {
		opts = append(opts, huh.NewOption(describeSource(s), fmt.Sprint(i)))
	}
	opts = append(opts,
		huh.NewOption("Choose another file or folder...", "pick"),
		huh.NewOption("I don't have one yet: show me how to get it", "guide"),
		huh.NewOption("Back", "back"),
	)
	title := "Pick your export"
	desc := "These look like YouTube Music exports on this computer."
	if len(srcs) == 0 {
		title = "No export found yet"
		desc = "Looked in Downloads, Desktop and Documents."
	}
	var choice string
	if err := one(ctx, huh.NewSelect[string]().Title(title).Description(desc).Options(opts...).Value(&choice)); err != nil {
		return takeout.Source{}, err
	}

	switch choice {
	case "back":
		return takeout.Source{}, errBack
	case "pick":
		return askForExport(ctx)
	case "guide":
		p, err := export.Guide(ctx, u)
		if err != nil {
			return takeout.Source{}, err
		}
		var s takeout.Source
		err = working(ctx, "Reading it...", func(context.Context) (e error) { s, e = takeout.Inspect([]string{p}); return })
		return s, err
	}
	var i int
	fmt.Sscan(choice, &i)
	return srcs[i], nil
}

func describeSource(s takeout.Source) string {
	where := tilde(filepath.Dir(s.Paths[0]))
	kind := ""
	if s.Kind == "folder" {
		kind = "folder, "
	}
	return fmt.Sprintf("%s  ·  %d tracks  ·  %s%s ago  ·  %s", s.Name(), s.Tracks, kind, humanAgo(time.Since(s.Mod)), where)
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// askForExport asks for a path until it points at something with tracks in it.
func askForExport(ctx context.Context) (takeout.Source, error) {
	var path string
	var found takeout.Source
	err := one(ctx, huh.NewInput().Title("Where is it?").
		Description("Drag the file or folder here, or paste its path. A Takeout .zip, an extracted folder,\na library .csv, or a text file of links all work.").
		Value(&path).Validate(func(s string) error {
		paths := takeout.SplitPaths(s)
		if len(paths) == 0 {
			return errors.New("type or paste a path")
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("can't find %s", p)
			}
		}
		src, err := takeout.Inspect(paths)
		if err != nil {
			return err
		}
		found = src
		return nil
	}))
	return found, err
}

// linkFlow asks for one or more YouTube, Apple Music or Spotify links and downloads them.
func linkFlow(ctx context.Context, u *ui.UI, o opts) error {
	var in string
	var links []string
	where := "YouTube, since you're not signed in to Apple Music"
	if appleSignedIn() {
		where = "Apple Music (256 kbps AAC) when it has the song, else YouTube"
	}
	if err := one(ctx, huh.NewInput().Title("Paste a link").
		Description("A video, playlist or album from YouTube or YouTube Music, or an album, song or playlist\n"+
			"from Apple Music or Spotify (those come from "+where+").\nSeveral links separated by spaces work too.").
		Value(&in).Validate(func(s string) error {
		links = links[:0]
		for _, f := range strings.Fields(s) {
			f = strings.Trim(f, `"'`)
			if apple.IsURL(f) || spotify.IsURL(f) {
				links = append(links, f)
				continue
			}
			l, ok := asURL(f)
			if !ok {
				return fmt.Errorf("%q isn't a YouTube, Apple Music or Spotify link", f)
			}
			links = append(links, l)
		}
		if len(links) == 0 {
			return errors.New("paste a link")
		}
		return nil
	})); err != nil {
		return err
	}
	return download(ctx, u, o, links...)
}
