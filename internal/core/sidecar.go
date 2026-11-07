package core

import (
	"context"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/sidecar"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// sidecarOptions is what Settings says to write; ok is false when all of it is off.
func sidecarOptions(cfg config.Config) (o sidecar.Options, ok bool) {
	o = sidecar.Options{JPG: !cfg.NoFolderJpg, NFO: !cfg.NoNFO, PNG: cfg.CoverPNG}
	return o, o.JPG || o.NFO || o.PNG
}

// writeSidecars writes folder.jpg / album.nfo / artist.nfo for the albums holding files
// (once per album, not per song), as far as Settings allows. Failures are only reported.
func writeSidecars(ctx context.Context, u *ui.UI, root string, files []string) {
	o, ok := sidecarOptions(config.Load())
	if !ok || len(files) == 0 {
		return
	}
	var err error
	if o.FFmpeg, o.FFprobe, err = deps.EnsureMedia(ctx, u.Logf); err != nil {
		u.Bad("cover and .nfo files: %v", err)
		return
	}
	res, err := sidecar.Sync(ctx, o, root, files, true)
	if err != nil && ctx.Err() == nil {
		u.Bad("cover and .nfo files: %v", err)
		return
	}
	if res.Changed() > 0 {
		u.Hint("Wrote %s for %s · turn off in settings", count(res.Changed(), "cover/.nfo file"), count(res.Albums, "album"))
	}
}
