package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

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

// tagFlow is the menu's way into `mfy tag`: pick what to do, then a preview that can be
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
