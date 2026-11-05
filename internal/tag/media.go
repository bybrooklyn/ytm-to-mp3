package tag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
)

// Info is what we know about an audio file.
type Info struct {
	Path     string
	Tags     map[string]string // ytm's canonical names (names.CanonicalTag)
	Duration float64
	HasCover bool
	// CoverW, CoverH are the embedded picture's size in pixels (0 when unknown).
	CoverW, CoverH int
}

// coverPx is the size every cover ytm writes is squared and scaled down to (at most).
const coverPx = 1200

// GoodCover reports whether the file already has art worth keeping: a square picture of a
// decent size. A file with a small or non-square one gets album art when there is some.
func (i Info) GoodCover() bool {
	if !i.HasCover {
		return false
	}
	if i.CoverW == 0 || i.CoverH == 0 {
		return true // size unknown: don't churn
	}
	d := i.CoverW - i.CoverH
	return d*d <= 4 && i.CoverW >= 500
}

func (i Info) get(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(i.Tags[k]); v != "" {
			return v
		}
	}
	return ""
}

// Probe reads tags, duration and cover presence with ffprobe.
func Probe(ctx context.Context, ffprobe, path string) (Info, error) {
	out, err := exec.CommandContext(ctx, ffprobe, probeArgs(path)...).Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe %s: %w", filepath.Base(path), err)
	}
	var j struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecType   string            `json:"codec_type"`
			Width       int               `json:"width"`
			Height      int               `json:"height"`
			Disposition map[string]int    `json:"disposition"`
			Tags        map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &j); err != nil {
		return Info{}, err
	}
	info := Info{Path: path, Tags: map[string]string{}}
	info.Duration, _ = strconv.ParseFloat(j.Format.Duration, 64)
	add := func(m map[string]string) {
		for k, v := range m {
			if c := names.CanonicalTag(k); info.Tags[c] == "" {
				info.Tags[c] = v
			}
		}
	}
	add(j.Format.Tags)
	for _, s := range j.Streams {
		if s.CodecType == "audio" {
			add(s.Tags) // Ogg/Opus keep tags on the stream
		}
		if s.CodecType == "video" && s.Disposition["attached_pic"] == 1 && !info.HasCover {
			info.HasCover, info.CoverW, info.CoverH = true, s.Width, s.Height
		}
	}
	return info, nil
}

// Write rewrites path in place with the given tag changes, copying streams
// untouched. cover, if non-empty, replaces the embedded picture. Existing tags
// not named in set are preserved.
func Write(ctx context.Context, ffmpeg, ffprobe, path string, set map[string]string, cover string) error {
	ext := filepath.Ext(path)
	if strings.EqualFold(ext, ".opus") || strings.EqualFold(ext, ".ogg") {
		return writeOgg(ctx, ffmpeg, ffprobe, path, set, cover)
	}
	// A fresh, uniquely named temp file next to the original (same filesystem, so the final
	// rename is atomic). Its name stays short so long file names can't push it over limits.
	tf, err := os.CreateTemp(filepath.Dir(path), ".ytm-*"+ext)
	if err != nil {
		return err
	}
	tmp := tf.Name()
	tf.Close()
	defer os.Remove(tmp)

	// Only jpeg/png/webp art is ever handed to ffmpeg's decoders, and it goes in as a square
	// JPEG of at most coverPx, like every cover ytm writes.
	dec := ""
	if cover != "" {
		sq := tmp + ".cover.jpg"
		defer os.Remove(sq)
		if a := names.SquareCoverArgs(cover, sq, coverPx); a != nil && exec.CommandContext(ctx, ffmpeg, a...).Run() == nil {
			cover, dec = sq, "mjpeg"
		} else {
			cover = "" // not a usable image: keep the existing art
		}
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-i", names.FFPath(path)}
	if cover != "" {
		args = append(args, "-protocol_whitelist", "file,pipe", "-f", "image2", "-pattern_type", "none",
			"-c:v", dec, "-i", names.FFPath(cover),
			"-map", "0:a", "-map", "1:v:0", "-map_metadata", "0",
			"-c", "copy", "-disposition:v:0", "attached_pic",
			"-metadata:s:v:0", "title=Album cover", "-metadata:s:v:0", "comment=Cover (front)")
	} else {
		args = append(args, "-map", "0", "-map_metadata", "0", "-c", "copy")
	}
	if ext == ".mp3" {
		args = append(args, "-id3v2_version", "3", "-write_id3v1", "1")
	}
	set = names.TagsFor(ext, set)
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-metadata", k+"="+names.StripControl(set[k])) // empty value clears the tag (used by undo)
	}
	args = append(args, names.FFPath(tmp))
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %s", names.Clean(strings.TrimSpace(lastLine(stderr.String()))))
	}
	if st, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp, st.Mode().Perm()) // CreateTemp made it 0600
	}
	return store.Replace(tmp, path) // atomic replace on unix
}

// writeOgg is Write for Ogg (Opus/Vorbis) files. Their tags live on the audio stream and
// their cover is a METADATA_BLOCK_PICTURE comment that ffmpeg reads as a picture stream
// but can't write back from one, so a plain stream copy fails or loses the art. Instead
// the complete tag set (current tags with set applied) and the cover (the new one, else
// the current one) go in through an ffmetadata file, and the audio is copied.
func writeOgg(ctx context.Context, ffmpeg, ffprobe, path string, set map[string]string, cover string) error {
	info, err := Probe(ctx, ffprobe, path)
	if err != nil {
		return err
	}
	tags := map[string]string{}
	for k, v := range info.Tags {
		switch k {
		case "encoder", "metadata_block_picture", "duration":
		default:
			tags[k] = v
		}
	}
	for k, v := range set {
		tags[k] = v // an empty value clears the tag (used by undo)
	}
	tags = names.TagsFor(filepath.Ext(path), tags)
	work, err := os.MkdirTemp(filepath.Dir(path), ".ytm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	src := cover
	if src == "" && info.HasCover {
		src = filepath.Join(work, "old.jpg")
		if ExtractCover(ctx, ffmpeg, path, src) != nil {
			src = ""
		}
	}
	jpeg := ""
	if src != "" {
		jpeg = filepath.Join(work, "cover.jpg")
		if args := names.SquareCoverArgs(src, jpeg, coverPx); args == nil || exec.CommandContext(ctx, ffmpeg, args...).Run() != nil {
			jpeg = "" // unusable art: keep the tags, lose nothing else
		}
	}
	meta := filepath.Join(work, "meta.txt")
	if err := names.WriteOggMeta(meta, tags, jpeg); err != nil {
		return err
	}
	tmp := filepath.Join(work, "out"+filepath.Ext(path))
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-i", names.FFPath(path)}, names.OggMetaArgs(meta)...)
	args = append(args, "-c:a", "copy", names.FFPath(tmp))
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %s", names.Clean(strings.TrimSpace(lastLine(stderr.String()))))
	}
	if st, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp, st.Mode().Perm())
	}
	return store.Replace(tmp, path)
}

// ReplaceAudio swaps path's audio for the stream in src (a better download of the same
// song), keeping path's name, tags and cover. Audio that already is the file's codec is
// copied; otherwise it is encoded as the file's format.
func ReplaceAudio(ctx context.Context, ffmpeg, ffprobe, src, path string) error {
	info, err := Probe(ctx, ffprobe, path)
	if err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(path))
	srcExt := strings.ToLower(filepath.Ext(src))
	work, err := os.MkdirTemp(filepath.Dir(path), ".ytm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	tmp := filepath.Join(work, "out"+ext)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-i", names.FFPath(src)}
	if ext == ".opus" || ext == ".ogg" {
		// Ogg: tags and cover go in as one ffmetadata file (see writeOgg).
		jpeg := ""
		if info.HasCover {
			old, sq := filepath.Join(work, "old.jpg"), filepath.Join(work, "cover.jpg")
			if ExtractCover(ctx, ffmpeg, path, old) == nil {
				if a := names.SquareCoverArgs(old, sq, coverPx); a != nil && exec.CommandContext(ctx, ffmpeg, a...).Run() == nil {
					jpeg = sq
				}
			}
		}
		tags := map[string]string{}
		for k, v := range info.Tags {
			if k != "encoder" && k != "duration" {
				tags[k] = v
			}
		}
		meta := filepath.Join(work, "meta.txt")
		if err := names.WriteOggMeta(meta, names.TagsFor(ext, tags), jpeg); err != nil {
			return err
		}
		args = append(args, names.OggMetaArgs(meta)...)
		if srcExt == ".webm" || srcExt == ".opus" {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "libopus", "-b:a", "256k")
		}
	} else {
		args = append(args, "-protocol_whitelist", "file,pipe", "-i", names.FFPath(path),
			"-map", "0:a:0", "-map", "1:v?", "-map_metadata", "1", "-c:v", "copy")
		switch ext {
		case ".m4a":
			if srcExt == ".m4a" {
				args = append(args, "-c:a", "copy")
			} else {
				args = append(args, "-c:a", "aac", "-b:a", "256k")
			}
			args = append(args, "-movflags", "+faststart")
		case ".mp3":
			args = append(args, "-c:a", "libmp3lame", "-q:a", "0", "-id3v2_version", "3", "-write_id3v1", "1")
		case ".flac":
			args = append(args, "-c:a", "flac")
		default:
			return fmt.Errorf("can't upgrade %s files", ext)
		}
	}
	args = append(args, names.FFPath(tmp))
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %s", names.Clean(strings.TrimSpace(lastLine(stderr.String()))))
	}
	if st, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp, st.Mode().Perm())
	}
	return store.Replace(tmp, path)
}

// probeArgs is the ffprobe command line for one file.
func probeArgs(path string) []string {
	return []string{"-v", "error", "-protocol_whitelist", "file,pipe", "-show_format", "-show_streams",
		"-of", "json", "-i", names.FFPath(path)}
}

// ExtractCover saves the embedded picture of path to dst.
func ExtractCover(ctx context.Context, ffmpeg, path, dst string) error {
	return exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-i", names.FFPath(path),
		"-an", "-map", "0:v:0", "-c", "copy", "-f", "image2", "-update", "1", names.FFPath(dst)).Run()
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
