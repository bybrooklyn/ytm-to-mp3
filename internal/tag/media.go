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
	Tags     map[string]string // lower-case keys as ffprobe reports them
	Duration float64
	HasCover bool
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
			if _, dup := info.Tags[strings.ToLower(k)]; !dup {
				info.Tags[strings.ToLower(k)] = v
			}
		}
	}
	add(j.Format.Tags)
	for _, s := range j.Streams {
		if s.CodecType == "audio" {
			add(s.Tags) // Ogg/Opus keep tags on the stream
		}
		if s.CodecType == "video" && s.Disposition["attached_pic"] == 1 {
			info.HasCover = true
		}
	}
	return info, nil
}

// Write rewrites path in place with the given tag changes, copying streams
// untouched. cover, if non-empty, replaces the embedded picture. Existing tags
// not named in set are preserved.
func Write(ctx context.Context, ffmpeg, path string, set map[string]string, cover string) error {
	ext := filepath.Ext(path)
	// A fresh, uniquely named temp file next to the original (same filesystem, so the final
	// rename is atomic). Its name stays short so long file names can't push it over limits.
	tf, err := os.CreateTemp(filepath.Dir(path), ".ytm-*"+ext)
	if err != nil {
		return err
	}
	tmp := tf.Name()
	tf.Close()
	defer os.Remove(tmp)

	// Only jpeg/png/webp art is ever handed to ffmpeg's decoders.
	dec := ""
	if cover != "" {
		if dec = names.ImageDecoder(cover); dec == "" {
			cover = "" // not a recognised image: keep the existing art
		}
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-i", names.FFPath(path)}
	if cover != "" {
		args = append(args, "-protocol_whitelist", "file,pipe", "-f", "image2", "-pattern_type", "none",
			"-c:v", dec, "-i", names.FFPath(cover),
			"-map", "0:a", "-map", "1:v:0", "-map_metadata", "0",
			"-c", "copy", "-disposition:v:0", "attached_pic")
	} else {
		args = append(args, "-map", "0", "-map_metadata", "0", "-c", "copy")
	}
	if ext == ".mp3" {
		args = append(args, "-id3v2_version", "3", "-write_id3v1", "1")
	}
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
