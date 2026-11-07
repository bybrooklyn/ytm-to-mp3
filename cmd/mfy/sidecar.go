package main

import (
	"context"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/sidecar"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// sidecarLibraryFlow is "Write cover and .nfo files for my library": a preview of what
// would be written, then the writing once confirmed. A second run changes nothing, and a
// file you made yourself is never touched.
func sidecarLibraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	so, ok := sidecarOptions(config.Load())
	if !ok {
		u.Note("album art and .nfo files are all switched off in Settings")
		return nil
	}
	files := audioFiles(o.out)
	if len(files) == 0 {
		u.Note("no songs in %s yet", tilde(o.out))
		return nil
	}
	var err error
	if so.FFmpeg, so.FFprobe, err = deps.EnsureMedia(ctx, u.Logf); err != nil {
		return err
	}
	u.Head("Looking at the albums of %s", count(len(files), "song"))
	res, err := sidecar.Sync(ctx, so, o.out, files, false)
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
	write := false
	if err := one(ctx, huh.NewConfirm().Title("Write these files?").
		Description("Files mfetchy wrote before are updated; files you made are left alone.").
		Affirmative("Write").Negative("Not now").Value(&write)); err != nil || !write {
		return err
	}
	res, err = sidecar.Sync(ctx, so, o.out, files, true)
	if err != nil {
		return err
	}
	u.Head("Wrote %s", count(res.Changed(), "file"))
	return nil
}
