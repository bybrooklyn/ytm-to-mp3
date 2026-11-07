package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/lyrics"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// runTag runs `mfy tag` on dir, or only on files (under dir) when given.
func runTag(ctx context.Context, u *ui.UI, o opts, dir string, files ...string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, u.Logf)
	if err != nil {
		return err
	}
	// Renamed files are recorded in the folder's mfetchy database (if it has one), so they're
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
