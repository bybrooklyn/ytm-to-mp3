package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Formats ytm can write.
var Formats = []string{"mp3", "m4a", "opus", "flac"}

// ValidFormat reports whether f is a supported output format.
func ValidFormat(f string) bool {
	for _, v := range Formats {
		if v == f {
			return true
		}
	}
	return false
}

// encodeArgs builds the ffmpeg command line that converts/remuxes src into dst
// with tags and (optionally) square cover art. Streams are copied untouched
// when the source codec already matches the target format.
func encodeArgs(src, cover, dst, format, srcCodec string, meta map[string]string) []string {
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", src}
	withCover := cover != "" && format != "opus" // ffmpeg can't embed pictures in Ogg Opus
	if withCover {
		a = append(a, "-i", cover)
	}
	a = append(a, "-map", "0:a:0")
	if withCover {
		a = append(a, "-map", "1:v:0")
	}
	a = append(a, "-map_metadata", "-1") // drop YouTube's own tags

	switch format {
	case "mp3":
		a = append(a, "-c:a", "libmp3lame", "-q:a", "0", "-id3v2_version", "3", "-write_id3v1", "1")
	case "m4a":
		if strings.HasPrefix(srcCodec, "mp4a") {
			a = append(a, "-c:a", "copy")
		} else {
			a = append(a, "-c:a", "aac", "-b:a", "256k")
		}
		a = append(a, "-movflags", "+faststart")
	case "opus":
		if strings.HasPrefix(srcCodec, "opus") {
			a = append(a, "-c:a", "copy")
		} else {
			a = append(a, "-c:a", "libopus", "-b:a", "160k")
		}
	case "flac":
		a = append(a, "-c:a", "flac", "-compression_level", "5")
	}
	if withCover {
		a = append(a,
			"-c:v", "mjpeg", "-q:v", "2",
			"-vf", "crop='min(iw,ih)':'min(iw,ih)',scale='min(1000,iw)':-1",
			"-disposition:v:0", "attached_pic",
			"-metadata:s:v:0", "title=Album cover", "-metadata:s:v:0", "comment=Cover (front)")
	}
	keys := make([]string, 0, len(meta))
	for k, v := range meta {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		a = append(a, "-metadata", k+"="+meta[k])
	}
	return append(a, dst)
}

// runFFmpeg executes ffmpeg and returns its stderr tail on failure.
func runFFmpeg(ctx context.Context, ffmpeg string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return nil
}
