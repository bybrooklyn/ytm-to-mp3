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
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_format", "-show_streams", "-of", "json", path).Output()
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
	tmp := strings.TrimSuffix(path, ext) + ".ytm-tmp" + ext
	defer os.Remove(tmp)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", path}
	if cover != "" {
		args = append(args, "-i", cover, "-map", "0:a", "-map", "1:v:0", "-map_metadata", "0",
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
		args = append(args, "-metadata", k+"="+set[k]) // empty value clears the tag (used by undo)
	}
	args = append(args, tmp)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(lastLine(stderr.String())))
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

// ExtractCover saves the embedded picture of path to dst.
func ExtractCover(ctx context.Context, ffmpeg, path, dst string) error {
	return exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", path, "-an", "-map", "0:v:0", "-c", "copy", "-f", "image2", dst).Run()
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
