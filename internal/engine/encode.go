package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
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
// when the source codec already matches the target format. For opus, cover is not an
// image but an ffmetadata file from names.WriteOggMeta (Ogg carries art as a comment).
func encodeArgs(src, cover, dst, format, srcCodec string, meta map[string]string) []string {
	// Inputs are untrusted media: only local files / pipes may be opened (no network, no
	// concat/subfile tricks through the playlist protocols), and paths are always given as
	// absolute "file:" paths so a name starting with '-' is never an option.
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-i", names.FFPath(src)}
	// Cover art comes from the network: decode it only if it really is a jpeg/png/webp,
	// with the demuxer and decoder forced so ffmpeg never probes it as something else.
	dec := ""
	if cover != "" && format != "opus" {
		dec = names.ImageDecoder(cover)
	}
	withCover := dec != ""
	switch {
	case withCover:
		a = append(a, "-protocol_whitelist", "file,pipe", "-f", "image2", "-pattern_type", "none",
			"-c:v", dec, "-i", names.FFPath(cover), "-map", "0:a:0", "-map", "1:v:0", "-map_metadata", "-1")
	case format == "opus" && cover != "":
		a = append(a, names.OggMetaArgs(cover)...) // the cover comment; YouTube's tags dropped
	default:
		a = append(a, "-map", "0:a:0", "-map_metadata", "-1") // drop YouTube's own tags
	}

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
	return append(a, names.FFPath(dst))
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
		return fmt.Errorf("ffmpeg: %s", names.Clean(msg))
	}
	return nil
}
