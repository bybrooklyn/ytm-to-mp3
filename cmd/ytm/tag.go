package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

func runTag(ctx context.Context, u *ui.UI, o opts, dir string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, func(f string, a ...any) { u.Printf(f, a...) })
	if err != nil {
		return err
	}
	return tag.Run(ctx, tag.Options{
		Dir: dir, Apply: o.apply, Rename: o.rename, Undo: o.undo, Offline: o.offline,
		FFmpeg: ffmpeg, FFprobe: ffprobe, UI: u,
	})
}
