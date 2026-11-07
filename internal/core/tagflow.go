package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/sidecar"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// TidyFlow renames the music folder to the current scheme (Artist/Album/NN - Title.ext,
// no [id]) without touching tags or going online: first the folders that are one under two
// spellings, then a preview of the renames, then the renames once confirmed.
func TidyFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	op := o.internal(p)
	if err := mergeFlow(ctx, u, op); err != nil {
		return err
	}
	op.rename, op.offline, op.apply = true, true, false
	if err := runTag(ctx, u, op, op.out); err != nil {
		return err
	}
	fix, err := p.Confirm(ctx, Question{Title: "Rename these files?",
		Detail: "Undo with \"Fix tags\" → \"Undo the last fix\".", Yes: "Rename", No: "Not now"})
	if err != nil || !fix {
		return err
	}
	op.apply = true
	return runTag(ctx, u, op, op.out)
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
	merge, err := o.prompt.Confirm(ctx, Question{Title: "Merge these folders?",
		Detail: "The spelling downloaded first is kept; the fullest edition takes the others. Nothing is deleted.\nUndo with \"Fix tags\" → \"Undo the last fix\".",
		Yes:    "Merge", No: "Not now"})
	if err != nil || !merge {
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

// TagFlow is "Fix tags": pick the folder and what to do, then a preview that can be applied
// straight away. Undo reverts the last fix.
func TagFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	op := o.internal(p)
	ans, err := p.Form(ctx, FormSpec{Title: "Fix tags", Submit: "Continue", Fields: []Field{
		{Key: "dir", Label: "Which folder?", Detail: "Every audio file under it is checked.", Default: tilde(op.out),
			Validate: func(s string) error {
				if st, err := os.Stat(config.ExpandPath(s)); err != nil || !st.IsDir() {
					return errors.New("that isn't a folder")
				}
				return nil
			}},
		{Key: "mode", Kind: FieldChoice, Label: "What should it do?", Default: "preview", Options: []Option{
			{Value: "preview", Label: "Show what it would change (writes nothing)"},
			{Value: "apply", Label: "Fix the tags"},
			{Value: "rename", Label: "Fix the tags and rename files to Artist/Album/NN - Title"},
			{Value: "undo", Label: "Undo the last fix"},
			{Value: "back", Label: "Back"},
		}},
		{Key: "online", Kind: FieldYesNo, Label: "Look songs up on MusicBrainz?", Default: "yes", Yes: "Online", No: "Offline",
			Detail: "Online: adds release dates, track numbers and corrects names it is sure about.\nOffline: only tidies what is already in the files."},
	}})
	if err != nil {
		return err
	}
	mode := ans["mode"]
	if mode == "back" {
		return ErrBack
	}
	op.apply, op.rename, op.undo, op.offline = mode == "apply" || mode == "rename", mode == "rename", mode == "undo", ans["online"] == "no"
	dir := config.ExpandPath(ans["dir"])
	if err := runTag(ctx, u, op, dir); err != nil || mode != "preview" {
		return err
	}
	fix, err := p.Confirm(ctx, Question{Title: "Apply these changes?", Yes: "Apply", No: "Not now"})
	if err != nil || !fix {
		return err
	}
	op.apply = true
	return runTag(ctx, u, op, dir)
}

// SidecarLibraryFlow is "Write cover and .nfo files for my library": a preview of what
// would be written, then the writing once confirmed. A second run changes nothing, and a
// file you made yourself is never touched.
func SidecarLibraryFlow(ctx context.Context, u *ui.UI, o Options, p Prompter) error {
	op := o.internal(p)
	so, ok := sidecarOptions(config.Load())
	if !ok {
		u.Note("album art and .nfo files are all switched off in Settings")
		return nil
	}
	files := audioFiles(op.out)
	if len(files) == 0 {
		u.Note("no songs in %s yet", tilde(op.out))
		return nil
	}
	var err error
	if so.FFmpeg, so.FFprobe, err = deps.EnsureMedia(ctx, u.Logf); err != nil {
		return err
	}
	u.Head("Looking at the albums of %s", count(len(files), "song"))
	res, err := sidecar.Sync(ctx, so, op.out, files, false)
	if err != nil {
		return err
	}
	u.Styled("Would write " + count(res.JPG+res.PNG, "album art file") + ", " + count(res.AlbumNFO, "album.nfo") + " and " + count(res.ArtistNFO, "artist.nfo") +
		" across " + count(res.Albums, "album"))
	if res.Kept > 0 {
		u.Hint("%s you made yourself will not be touched", count(res.Kept, "file"))
	}
	if res.Changed() == 0 {
		u.Note("everything is already there")
		return nil
	}
	write, err := p.Confirm(ctx, Question{Title: "Write these files?",
		Detail: "Files mfetchy wrote before are updated; files you made are left alone.", Yes: "Write", No: "Not now"})
	if err != nil || !write {
		return err
	}
	res, err = sidecar.Sync(ctx, so, op.out, files, true)
	if err != nil {
		return err
	}
	u.Head("Wrote %s", count(res.Changed(), "file"))
	return nil
}
