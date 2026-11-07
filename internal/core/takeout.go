package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/export"
	"github.com/bybrooklyn/mfetchy/internal/takeout"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// ChooseExport finds the YouTube Music exports on this computer and lets the person pick
// one (or another file, or shows how to get one). ErrBack = backed out.
func ChooseExport(ctx context.Context, u *ui.UI, p Prompter) (takeout.Source, error) {
	var srcs []takeout.Source
	if err := p.Working(ctx, "Looking for your export in Downloads, Desktop and Documents...", func(context.Context) error {
		srcs = takeout.Discover(export.SearchDirs())
		return nil
	}); err != nil {
		return takeout.Source{}, err
	}

	var opts []Option
	for i, s := range srcs {
		opts = append(opts, Option{Value: fmt.Sprint(i), Label: describeSource(s)})
	}
	opts = append(opts,
		Option{Value: "pick", Label: "Choose another file or folder..."},
		Option{Value: "guide", Label: "I don't have one yet: show me how to get it"},
		Option{Value: "back", Label: "Back"},
	)
	title := "Pick your export"
	desc := "These look like YouTube Music exports on this computer."
	if len(srcs) == 0 {
		title = "No export found yet"
		desc = "Looked in Downloads, Desktop and Documents."
	}
	choice, err := p.Select(ctx, title, desc, opts, opts[0].Value)
	if err != nil {
		return takeout.Source{}, err
	}

	switch choice {
	case "back":
		return takeout.Source{}, ErrBack
	case "pick":
		return askForExport(ctx, u, p)
	case "guide":
		path, err := TakeoutGuide(ctx, u, p)
		if err != nil {
			return takeout.Source{}, err
		}
		var s takeout.Source
		err = p.Working(ctx, "Reading it...", func(context.Context) (e error) { s, e = takeout.Inspect([]string{path}); return })
		return s, err
	}
	var i int
	fmt.Sscan(choice, &i)
	if i < 0 || i >= len(srcs) {
		return takeout.Source{}, ErrBack
	}
	return srcs[i], nil
}

// TakeoutGuide explains how to request the export (with a link to the page), then waits for
// the zip to arrive in the download folders and returns its path.
func TakeoutGuide(ctx context.Context, u *ui.UI, p Prompter) (string, error) {
	var path string
	err := p.Wait(ctx, Info{
		Title: export.GuideTitle, Steps: export.GuideSteps,
		Link: export.TakeoutURL, LinkLabel: "Open takeout.google.com", AutoOpen: true,
		Waiting: "mfetchy notices the zip and carries on by itself.",
	}, func(ctx context.Context) (e error) {
		path, e = export.WaitForZip(ctx, u)
		return
	})
	return path, err
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

// exportPaths reads the paths in what was typed or chosen: one path exactly as given, or
// several dragged in together.
func exportPaths(s string) []string {
	s = strings.TrimSpace(s)
	if _, err := os.Stat(s); err == nil {
		return []string{s}
	}
	return takeout.SplitPaths(s)
}

// askForExport asks for a path until it points at something with tracks in it.
func askForExport(ctx context.Context, u *ui.UI, p Prompter) (takeout.Source, error) {
	var found takeout.Source
	_, err := p.File(ctx, FileRequest{
		Title: "Where is it?",
		Detail: "Drag the file or folder here, or paste its path. A Takeout .zip, an extracted folder,\n" +
			"a library .csv, or a text file of links all work.",
		File: true, Folder: true, Exts: []string{".zip", ".csv", ".txt"},
		Validate: func(s string) error {
			paths := exportPaths(s)
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
		},
	})
	return found, err
}

// TakeoutFlow finds (or helps get) a Takeout export, shows what is in it, and downloads it.
func TakeoutFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	src, err := ChooseExport(ctx, u, p)
	if err != nil {
		return err
	}
	iop := o.internal(p)
	head := fmt.Sprintf("%d tracks (%d with song details) in %s", src.Tracks, src.WithMeta, src.Name())
	where := fmt.Sprintf("Saving to %s as %s", tilde(iop.out), iop.format)
	u.Styled(fmt.Sprintf("\n  %s %s %s in %s\n    %s %s %s %s\n", ui.OK("✓"), ui.Bold(fmt.Sprint(src.Tracks)),
		ui.Dim(fmt.Sprintf("tracks (%d with song details)", src.WithMeta)), src.Name(),
		ui.Dim("saving to"), tilde(iop.out), ui.Dim("as"), ui.Cyan(iop.format)))
	u.Styled(fmt.Sprintf("    %s%s%s\n", cDim, extras(iop), cReset))
	ok, err := p.Confirm(ctx, Question{
		Title: "Download?", Detail: head + "\n" + where + ".\n" + capitalFirst(strings.TrimSuffix(extras(iop), "  (change in settings)")) + ".",
		Yes: "Download", No: "Back", Default: true})
	if err != nil {
		return err
	}
	if !ok {
		return ErrBack
	}
	_, err = runDownload(ctx, u, iop, nil, src.Paths)
	return err
}

// AppleMatchFlow picks a YouTube Music export and finds its songs on Apple Music.
func AppleMatchFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	src, err := ChooseExport(ctx, u, p)
	if err != nil {
		return err
	}
	return appleMatch(ctx, u, o.internal(p), src.Paths)
}

// Wizard is what plain `mfy` does with no terminal menu: use an export found lying around
// (after saying what it is and being told yes), or ask for one, or walk through getting one.
// It returns the path of the export.
func Wizard(ctx context.Context, u *ui.UI, p Prompter) (string, error) {
	// An export found lying around is only used after the person has seen what it is and
	// said yes; with nobody to ask, it is never adopted automatically.
	if path, ok := findExisting(); ok && u.Interactive() {
		use, err := p.Confirm(ctx, Question{Title: "Found " + export.Describe(path) + "\n  at " + path + "\nDownload it?", Default: true})
		if err != nil {
			return "", err
		}
		if use {
			return path, nil
		}
	}
	if !u.Interactive() {
		return "", errors.New("no export given")
	}
	u.Warn("mfetchy needs your YouTube Music export (a Google Takeout file).")
	ans, err := p.Input(ctx, "Path to it, or press Enter to get one now:", "", "")
	if err != nil {
		return "", err
	}
	if ans != "" {
		return strings.Trim(ans, `"' `), nil
	}
	return TakeoutGuide(ctx, u, p)
}

func capitalFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
