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

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/export"
	batch "github.com/bybrooklyn/mfetchy/internal/inputs"
	"github.com/bybrooklyn/mfetchy/internal/screen"
	"github.com/bybrooklyn/mfetchy/internal/takeout"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// errBack means "go back to the menu" from a sub-flow.
var errBack = errors.New("back")

// runHome is what plain `ytm` does in a terminal: a short menu that leads to the right
// flow, so nothing has to be remembered.
func runHome(ctx context.Context, u *ui.UI, o opts) error {
	if !config.Exists() {
		first := true
		if err := one(ctx, huh.NewConfirm().Title("Welcome to ytm. Set it up first? (takes a minute)").
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
		return tagFlow(ctx, u, o)
	case "tidy":
		return tidyFlow(ctx, u, o)
	case "lyrics":
		return lyricsLibraryFlow(ctx, u, o)
	case "sidecars":
		return sidecarLibraryFlow(ctx, u, o)
	case "premium":
		if !config.Load().Premium {
			if err := premiumSetup(ctx, u); err != nil || !config.Load().Premium {
				return err
			}
		}
		return premiumUpgrade(ctx, u, o, false)
	case "update":
		return deps.Update(ctx, u.Logf)
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
	if o.lyricsAfter {
		on = append(on, "fetch lyrics")
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
	line := func() string { return "  ♪ ytm  " + folder + "  ·  " + o.format + "  ·  ● " + vpn }
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
	return fmt.Sprintf("\n  %s %s  %s%s%s%s%s %s\n", ui.Accent("♪"), ui.Bold("ytm"),
		ui.Dim(folder), sep, ui.Cyan(o.format), sep, dot, ui.Dim(vpn))
}

// tilde shortens a path under the home folder for display.
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return "~" + string(filepath.Separator) + rel // native separators, so Windows isn't ~/AppData\Roaming
		}
	}
	return p
}

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
	src, err := chooseExport(ctx, u)
	if err != nil {
		return err
	}
	fmt.Printf("\n  %s %s tracks %s in %s\n    %s %s %s %s\n\n", ui.OK("✓"), ui.Bold(fmt.Sprint(src.Tracks)),
		ui.Dim(fmt.Sprintf("(%d with song details)", src.WithMeta)), src.Name(),
		ui.Dim("saving to"), tilde(o.out), ui.Dim("as"), ui.Cyan(o.format))
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
