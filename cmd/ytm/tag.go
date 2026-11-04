package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// runTag runs `ytm tag` on dir, or only on files (under dir) when given.
func runTag(ctx context.Context, u *ui.UI, o opts, dir string, files ...string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, func(f string, a ...any) { u.Printf(f, a...) })
	if err != nil {
		return err
	}
	// Renamed files are recorded in the folder's ytm database (if it has one), so they're
	// still known as downloaded.
	// The album lookups there also number old files that have no track number.
	var onMove func(from, to string)
	var trackNo func(artist, album, title, id string) (int, int)
	if _, err := os.Stat(filepath.Join(dir, ".ytm", "state.db")); err == nil {
		st, err := store.Open(filepath.Join(dir, ".ytm"))
		if err != nil {
			return err
		}
		defer st.Close()
		onMove = func(from, to string) { _ = st.MovePath(from, to) }
		trackNo = albumNumbers(st)
	}
	return tag.Run(ctx, tag.Options{
		Dir: dir, Apply: o.apply, Rename: o.rename, Undo: o.undo, Offline: o.offline,
		FFmpeg: ffmpeg, FFprobe: ffprobe, UI: u, OnMove: onMove, Files: files, TrackNumber: trackNo,
	})
}

// tidyFlow renames the music folder to the current scheme (Artist/Album/NN - Title.ext,
// no [id]) without touching tags or going online: a preview first, then apply.
func tidyFlow(ctx context.Context, u *ui.UI, o opts) error {
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

// tagFlow is the menu's way into `ytm tag`: pick what to do, then a preview that can be
// applied straight away.
func tagFlow(ctx context.Context, u *ui.UI, o opts) error {
	dir := tilde(o.out)
	mode, online := "preview", true
	err := huh.NewForm(huh.NewGroup(
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
	)).RunWithContext(ctx)
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
