package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/core"
	batch "github.com/bybrooklyn/mfetchy/internal/inputs"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// runHome is what plain `mfetchy` does in a terminal: a short menu that leads to the right
// flow, so nothing has to be remembered.
func runHome(ctx context.Context, u *ui.UI, o opts) error {
	if !config.Exists() {
		first := true
		if err := one(ctx, huh.NewConfirm().Title("Welcome to mfetchy. Set it up first? (takes a minute)").
			Description("Pick your music folder and set up a VPN (Proton, Mullvad or your own WireGuard config). You can skip and change it later.").
			Affirmative("Set up").Negative("Skip").Value(&first)); err != nil {
			if !errors.Is(err, errBack) {
				return err
			}
			first = false // Esc: skip
		}
		if first {
			if err := runSettings(ctx, u); err != nil && !errors.Is(err, errBack) {
				return err
			}
			o = refreshOpts(o)
		}
	}
	for again := false; ; again = true {
		if again {
			// Coming back from a screen: start the menu on a clean screen instead of stacking
			// another header (and the left-overs of what ran) under the last one.
			fmt.Print("\x1b[H\x1b[2J")
		}
		fmt.Println(header(o, u.Width()))
		var choice string
		err := one(ctx, huh.NewSelect[string]().Title("What do you want to do?").Options(
			huh.NewOption("Download my YouTube Music library", "library"),
			huh.NewOption("Download from all my accounts", "all"),
			huh.NewOption("Download a link (YouTube, Apple Music, Spotify, SoundCloud, Bandcamp)", "link"),
			huh.NewOption("Apple Music...", "apple"),
			huh.NewOption("Connections (accounts and sources)", "connections"),
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
		case "all":
			err = allAccountsFlow(ctx, u, o)
		case "link":
			err = linkFlow(ctx, u, o)
		case "connections":
			err = runConnections(ctx, u)
		case "settings":
			if err = runSettings(ctx, u); err == nil {
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
		switch {
		case errors.Is(err, errBack):
			continue
		case ctx.Err() != nil || errors.Is(err, context.Canceled):
			return err // Ctrl-C: leave
		case err != nil:
			// A tool that failed shouldn't take the whole app with it: say why, and offer
			// the menu again.
			fmt.Println("\n" + ui.Mark(ui.Fail("✗"), ui.Sanitize(err.Error())))
		case choice == "library" || choice == "link" || choice == "all":
			return nil // a download ran; we're done
		}
		back := true
		if err := one(ctx, huh.NewConfirm().Title("Back to the menu?").Affirmative("Menu").Negative("Quit").Value(&back)); err != nil {
			if !errors.Is(err, errBack) {
				return nil
			}
			back = true // Esc: the menu
		}
		if !back {
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
		huh.NewOption("Fetch lyrics for my library", "lyrics"),
		huh.NewOption("Write cover and .nfo files for my library", "sidecars"),
		huh.NewOption("YouTube Premium upgrade now (experimental, slow)", "premium"),
		huh.NewOption("Update yt-dlp, ffmpeg and deno", "update"),
		huh.NewOption("Check that everything works", "doctor"),
		huh.NewOption("Back", "back"),
	).Value(&choice)); err != nil {
		return err
	}
	switch choice {
	case "tag":
		return core.TagFlow(ctx, u, o.core(), newPrompter(u))
	case "tidy":
		return core.TidyFlow(ctx, u, o.core(), newPrompter(u))
	case "lyrics":
		return lyricsLibraryFlow(ctx, u, o)
	case "sidecars":
		return core.SidecarLibraryFlow(ctx, u, o.core(), newPrompter(u))
	case "premium":
		if !config.Load().Premium {
			if err := core.PremiumSetup(ctx, u, newPrompter(u)); err != nil || !config.Load().Premium {
				return err
			}
		}
		return premiumUpgrade(ctx, u, o, false)
	case "update":
		return core.UpdateTools(ctx, u)
	case "doctor":
		return runDoctor(ctx, u, o)
	}
	return errBack
}

// refreshOpts re-reads saved settings after setup, keeping flags the user passed.
func refreshOpts(o opts) opts {
	cfg := config.Load()
	if cfg.Out != "" && !o.given["o"] {
		o.out = config.ExpandPath(cfg.Out)
	}
	if cfg.Format != "" && !o.given["f"] {
		o.format = cfg.Format
	}
	if !o.given["j"] {
		o.jobs = cfg.Jobs
	}
	set := func(flag string, dst *bool, saved bool) {
		if !o.given[flag] {
			*dst = saved
		}
	}
	set("albums", &o.albums, !cfg.NoAlbums)
	set("retry", &o.retry, !cfg.NoRetry)
	set("tag-after", &o.tagAfter, !cfg.NoTagAfter)
	set("premium-after", &o.premiumAfter, !cfg.NoPremiumAfter)
	set("lyrics-after", &o.lyricsAfter, !cfg.NoLyricsAfter)
	return o
}

// extras says, in one line, what a library download does besides downloading.
func extras(o opts) string { return o.core().Extras() }

func header(o opts, width int) string {
	cfg := config.Load()
	v := currentVPN(cfg)
	vpn, vpnShort := "VPN off", "no VPN"
	dot := ui.Dim("○")
	if v.SignedIn && !cfg.NoVPN && !o.noVPN {
		dot = ui.Green("●")
		n := connectionsLabel(cfg, v)
		if o.conns > 0 { // --connections for this run
			n = fmt.Sprint(min(o.conns, screen.MaxConns))
		}
		name := providerLabel(providerID(cfg))
		vpn, vpnShort = name+", "+n+" connections", "VPN ×"+n
		if n == "1" {
			vpn = name + ", 1 connection"
		}
	}
	// Never wrap: on a narrow terminal shorten the VPN part, then the folder.
	folder := tilde(o.out)
	line := func() string { return "  ♪ mfetchy  " + folder + "  ·  " + o.format + "  ·  ● " + vpn }
	if len([]rune(line())) > width-1 {
		vpn = vpnShort
	}
	if over := len([]rune(line())) - (width - 1); over > 0 {
		r := []rune(folder)
		if keep := len(r) - over - 1; keep > 3 {
			folder = "…" + string(r[len(r)-keep:])
		} else {
			folder = filepath.Base(o.out)
		}
	}
	sep := ui.Dim("  ·  ")
	return fmt.Sprintf("\n  %s %s  %s%s%s%s%s %s\n", ui.Accent("♪"), ui.Bold("mfetchy"),
		ui.Dim(folder), sep, ui.Cyan(o.format), sep, dot, ui.Dim(vpn))
}

// tilde shortens a path under the home folder for display.
// libraryFlow finds (or helps get) a Takeout export, shows what's in it, and downloads it.
func libraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	var how string
	if err := one(ctx, huh.NewSelect[string]().Title("Where is your YouTube Music library?").Options(
		huh.NewOption("Read it from my signed-in browser (no export needed)", "live"),
		huh.NewOption("From a Google Takeout export", "takeout"),
		huh.NewOption("Back", "back"),
	).Value(&how)); err != nil {
		return err
	}
	switch how {
	case "back":
		return errBack
	case "live":
		return liveLibraryFlow(ctx, u, o)
	}
	return core.TakeoutFlow(ctx, u, o.core(), newPrompter(u))
}

// linkFlow asks for one or more YouTube, Apple Music, Spotify, SoundCloud or Bandcamp links and downloads them.
func linkFlow(ctx context.Context, u *ui.UI, o opts) error {
	var in string
	var links []string
	where := "Not signed in to Apple Music: Apple and Spotify songs come from YouTube."
	if appleSignedIn() {
		where = "Apple and Spotify songs come from Apple Music when it has them, else YouTube."
	}
	if err := one(ctx, huh.NewText().Title("Paste links or a file").Lines(6).
		Description("Songs, albums or playlists from YouTube, Apple Music, Spotify, Tidal, SoundCloud or Bandcamp, mixed.\n"+
			"Paste a whole list (one per line or separated by spaces), or drag in a text file of links or a Takeout .zip.\n"+where+
			"\nEnter to start · alt+enter for a new line").
		Value(&in).Validate(func(s string) error {
		res := batch.Parse([]string{s})
		links = res.Values()
		if len(links) > 0 {
			return nil // lines that are no use are reported when the run starts
		}
		if len(res.Bad) > 0 {
			return fmt.Errorf("%s", res.Bad[0])
		}
		return errors.New("paste a link")
	})); err != nil {
		return err
	}
	return download(ctx, u, o, links...)
}
