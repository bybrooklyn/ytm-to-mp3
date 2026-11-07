package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/lyrics"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// runTag runs `ytm tag` on dir, or only on files (under dir) when given.
func runTag(ctx context.Context, u *ui.UI, o opts, dir string, files ...string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, u.Logf)
	if err != nil {
		return err
	}
	// Renamed files are recorded in the folder's ytm database (if it has one), so they're
	// still known as downloaded.
	// The album lookups there also number old files that have no track number.
	var onMove func(from, to string)
	var trackNo func(artist, album, title, id string) (int, int)
	if _, err := os.Stat(filepath.Join(appname.StateDir(dir), "state.db")); err == nil {
		st, err := store.Open(appname.StateDir(dir))
		if err != nil {
			return err
		}
		defer st.Close()
		onMove = func(from, to string) { _ = st.MovePath(from, to) }
		trackNo = albumNumbers(st)
	}
	// a renamed song takes its lyrics file with it
	moved := onMove
	onMove = func(from, to string) {
		lyrics.MoveSidecars(from, to)
		if moved != nil {
			moved(from, to)
		}
	}
	// Private mode means no online lookups at all. Otherwise, with the VPN on, they go out
	// through it (and stop if it can't connect) rather than from your own IP.
	offline := o.offline || config.Load().Private || o.undo
	run := func() error {
		return tag.Run(ctx, tag.Options{
			Dir: dir, Apply: o.apply, Rename: o.rename, Undo: o.undo, Offline: offline,
			FFmpeg: ffmpeg, FFprobe: ffprobe, UI: u, OnMove: onMove, Files: files, TrackNumber: trackNo,
		})
	}
	if offline {
		return run()
	}
	return withTagNetwork(ctx, u, o, run)
}

// tidyFlow renames the music folder to the current scheme (Artist/Album/NN - Title.ext,
// no [id]) without touching tags or going online: a preview first, then apply.
func tidyFlow(ctx context.Context, u *ui.UI, o opts) error {
	if err := mergeFlow(ctx, u, o); err != nil {
		return err
	}
	o.rename, o.offline, o.apply = true, true, false
	if err := runTag(ctx, u, o, o.out); err != nil {
		return err
	}
	fix := false
	if err := one(ctx, huh.NewConfirm().Title("Rename these files?").
		Description("Undo with \"Fix tags\" → \"Undo the last fix\".").
		Affirmative("Rename").Negative("Not now").Value(&fix)); err != nil || !fix {
		return err
	}
	o.apply = true
	return runTag(ctx, u, o, o.out)
}

// mergeFlow finds artist and album folders that are one under two spellings (differing by
// case, or a standard and a deluxe edition), shows the merge, and applies it when told to.
// Nothing is deleted; duplicates go to .mfetchy/replaced, and "Undo the last fix" reverts it.
func mergeFlow(ctx context.Context, u *ui.UI, o opts) error {
	groups, err := tag.PlanMerges(o.out)
	if err != nil || len(groups) == 0 {
		return err
	}
	to := tag.Options{Dir: o.out, UI: u}
	if _, err := tag.MergeFolders(ctx, to); err != nil {
		return err
	}
	merge := false
	if err := one(ctx, huh.NewConfirm().Title("Merge these folders?").
		Description("The spelling downloaded first is kept; the fullest edition takes the others. Nothing is deleted.\nUndo with \"Fix tags\" → \"Undo the last fix\".").
		Affirmative("Merge").Negative("Not now").Value(&merge)); err != nil || !merge {
		return err
	}
	if _, err := os.Stat(filepath.Join(appname.StateDir(o.out), "state.db")); err == nil {
		st, err := store.Open(appname.StateDir(o.out))
		if err != nil {
			return err
		}
		defer st.Close()
		to.OnMove = func(from, dst string) { _ = st.MovePath(from, dst) }
	}
	to.Apply = true
	_, err = tag.MergeFolders(ctx, to)
	return err
}

// tagFlow is the menu's way into `ytm tag`: pick what to do, then a preview that can be
// applied straight away.
func tagFlow(ctx context.Context, u *ui.UI, o opts) error {
	dir := tilde(o.out)
	mode, online := "preview", true
	err := runForm(ctx, huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Which folder?").Description("Every audio file under it is checked.").
			Value(&dir).Validate(func(s string) error {
			if st, err := os.Stat(config.ExpandPath(s)); err != nil || !st.IsDir() {
				return errors.New("that isn't a folder")
			}
			return nil
		}),
		huh.NewSelect[string]().Title("What should it do?").Options(
			huh.NewOption("Show what it would change (writes nothing)", "preview"),
			huh.NewOption("Fix the tags", "apply"),
			huh.NewOption("Fix the tags and rename files to Artist/Album/NN - Title", "rename"),
			huh.NewOption("Undo the last fix", "undo"),
			huh.NewOption("Back", "back"),
		).Value(&mode),
		huh.NewConfirm().Title("Look songs up on MusicBrainz?").
			Description("Online: adds release dates, track numbers and corrects names it is sure about.\nOffline: only tidies what is already in the files.").
			Affirmative("Online").Negative("Offline").Value(&online),
	)))
	if err != nil {
		return err
	}
	if mode == "back" {
		return errBack
	}
	o.apply, o.rename, o.undo, o.offline = mode == "apply" || mode == "rename", mode == "rename", mode == "undo", !online
	dir = config.ExpandPath(dir)
	if err := runTag(ctx, u, o, dir); err != nil || mode != "preview" {
		return err
	}
	fix := false
	if err := one(ctx, huh.NewConfirm().Title("Apply these changes?").Affirmative("Apply").Negative("Not now").Value(&fix)); err != nil || !fix {
		return err
	}
	o.apply = true
	return runTag(ctx, u, o, dir)
}
