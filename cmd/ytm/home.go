package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/export"
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
		err := one(ctx, huh.NewSelect[string]().Title("What do you want to do?").Description("Library = your Google Takeout export. Link = a video, playlist or album.").Options(
			huh.NewOption("Import my YouTube Music library", "library"),
			huh.NewOption("Download a link", "link"),
			huh.NewOption("Fix tags in my music folder (MusicBrainz)", "tag"),
			huh.NewOption("Tidy file names (Artist/Album/01 - Title, no [id])", "tidy"),
			huh.NewOption("Upgrade to YouTube Premium quality (experimental, slow)", "premium"),
			huh.NewOption("Settings", "settings"),
			huh.NewOption("Update yt-dlp, ffmpeg and deno", "update"),
			huh.NewOption("Check that everything works", "doctor"),
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
		case "tag":
			err = tagFlow(ctx, u, o)
		case "tidy":
			err = tidyFlow(ctx, u, o)
		case "premium":
			if !config.Load().Premium {
				if err = premiumSetup(ctx, u); err == nil && config.Load().Premium {
					err = premiumUpgrade(ctx, u, o)
				}
			} else {
				err = premiumUpgrade(ctx, u, o)
			}
		case "update":
			err = deps.Update(ctx, func(f string, a ...any) { u.Printf(f, a...) })
		case "settings":
			if err = runSetup(ctx, u, false); err == nil {
				o = refreshOpts(o)
			}
		case "doctor":
			err = runDoctor(ctx, u, o)
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
		if choice == "settings" {
			continue // settings are saved as you go; straight back to the menu
		}
		back := true
		if one(ctx, huh.NewConfirm().Title("Back to the menu?").Affirmative("Menu").Negative("Quit").Value(&back)) != nil || !back {
			return nil
		}
	}
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
	return o
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
	var srcs []takeout.Source
	if err := working(ctx, "Looking for your export in Downloads, Desktop and Documents...", func(context.Context) error {
		srcs = takeout.Discover(export.SearchDirs())
		return nil
	}); err != nil {
		return err
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
		return err
	}

	var src takeout.Source
	switch choice {
	case "back":
		return errBack
	case "pick":
		s, err := askForExport(ctx)
		if err != nil {
			return err
		}
		src = s
	case "guide":
		p, err := export.Guide(ctx, u)
		if err != nil {
			return err
		}
		var s takeout.Source
		if err := working(ctx, "Reading it...", func(context.Context) (e error) { s, e = takeout.Inspect([]string{p}); return }); err != nil {
			return err
		}
		src = s
	default:
		var i int
		fmt.Sscan(choice, &i)
		src = srcs[i]
	}

	fmt.Printf("\n  \x1b[32m✓\x1b[0m %d tracks (%d with song details) in %s\n    saving to %s as %s\n\n",
		src.Tracks, src.WithMeta, src.Name(), tilde(o.out), o.format)
	go_ := true
	var extras []string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().Title("Options").Description("space to toggle · enter to continue").Options(
			huh.NewOption("Retry songs that were unavailable last time", "retry"),
			huh.NewOption("Also get the rest of every album I have a song from", "albums"),
			huh.NewOption("Fix tags with MusicBrainz afterwards", "tag"),
		).Value(&extras),
		huh.NewConfirm().Title("Download them?").Affirmative("Download").Negative("Back").Value(&go_),
	)).RunWithContext(ctx); err != nil {
		return err
	}
	if !go_ {
		return errBack
	}
	has := func(k string) bool { return slices.Contains(extras, k) }
	o.retry = o.retry || has("retry")
	o.albums = o.albums || has("albums")
	if err := download(ctx, u, o, src.Paths...); err != nil || !has("tag") {
		return err
	}
	o.apply = true
	return runTag(ctx, u, o, o.out)
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

// linkFlow asks for one or more YouTube links and downloads them.
func linkFlow(ctx context.Context, u *ui.UI, o opts) error {
	var in string
	var links []string
	if err := one(ctx, huh.NewInput().Title("Paste a link").
		Description("A video, playlist or album from YouTube or YouTube Music. Several links separated by spaces work too.").
		Value(&in).Validate(func(s string) error {
		links = links[:0]
		for _, f := range strings.Fields(s) {
			l, ok := asURL(strings.Trim(f, `"'`))
			if !ok {
				return fmt.Errorf("%q isn't a YouTube link", f)
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
