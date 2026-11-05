package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/export"
	"github.com/bybrooklyn/ytm-to-mp3/internal/takeout"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// errBack means "go back to the menu" from a sub-flow.
var errBack = errors.New("back")

// runHome is what plain `ytm` does in a terminal: one screen with everything ytm does.
// An action runs in the plain terminal (so its output stays readable), then the screen
// comes back.
func runHome(ctx context.Context, u *ui.UI, o opts) error {
	if !config.Exists() {
		first := true
		if err := one(ctx, huh.NewConfirm().Title("Welcome to ytm. Set it up first? (takes a minute)").
			Description("Pick your music folder and sign in to Proton VPN. You can skip and change it later.").
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
	cur := 0
	for {
		m := newHomeModel(o, cur, "")
		final, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		fm := final.(*homeModel)
		cur = fm.cur
		if fm.action == "" {
			return nil
		}
		err = homeAction(ctx, u, o, fm.action, fm.inputs)
		if fm.action == "settings" {
			o = refreshOpts(o)
		}
		switch {
		case errors.Is(err, errBack):
			continue // backed out: straight back to the screen
		case ctx.Err() != nil || errors.Is(err, context.Canceled):
			return err // Ctrl-C: leave
		case err != nil:
			// A failure shouldn't take the whole app with it: say why, then the screen again.
			fmt.Printf("\n  %s✗%s %s\n", red, reset, ui.Sanitize(err.Error()))
		}
		if fm.action == "settings" && err == nil {
			continue // settings are saved as you go, nothing to read: back to the screen
		}
		// Leave what ran on screen until it has been read.
		back := true
		if err := one(ctx, huh.NewConfirm().Title("Back to ytm?").Affirmative("Back").Negative("Quit").Value(&back)); err != nil {
			if !errors.Is(err, errBack) {
				return nil
			}
			back = true // Esc: back
		}
		if !back {
			return nil
		}
	}
}

// homeAction runs what was picked on the home screen.
func homeAction(ctx context.Context, u *ui.UI, o opts, action string, inputs []string) error {
	fmt.Println()
	switch action {
	case "input":
		return download(ctx, u, o, inputs...)
	case "library":
		return libraryFlow(ctx, u, o)
	case "apple":
		if !appleSignedIn() {
			fmt.Printf("  %sApple Music%s gives 256 kbps AAC with your own Apple Music subscription.\n", cAccent, cReset)
			fmt.Printf("  %sSign in at music.apple.com in your browser (and play a song), then continue.%s\n\n", cDim, cReset)
			in := true
			if err := one(ctx, huh.NewConfirm().Title("Sign in to Apple Music now?").
				Affirmative("Sign in").Negative("Back").Value(&in)); err != nil {
				return err
			}
			if !in {
				return errBack
			}
			if err := appleLogin(ctx, u); err != nil {
				return err
			}
		}
		src, err := chooseExport(ctx, u)
		if err != nil {
			return err
		}
		return appleMatch(ctx, u, o, src.Paths)
	case "premium":
		if !config.Load().Premium {
			if err := premiumSetup(ctx, u); err != nil || !config.Load().Premium {
				return err
			}
		}
		return premiumUpgrade(ctx, u, o, false)
	case "tag":
		return tagFlow(ctx, u, o)
	case "tidy":
		return tidyFlow(ctx, u, o)
	case "settings":
		return runSettings(ctx, u)
	case "doctor":
		return runDoctor(ctx, u, o)
	case "update":
		return deps.Update(ctx, func(f string, a ...any) { u.Printf(f, a...) })
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
