package soundcloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/grafov/m3u8"
)

// choice is a transcoding ytm can take, with the file extension it produces.
type choice struct {
	t    transcoding
	ext  string
	rank int // lower is better
}

// pick chooses the audio to fetch: a progressive file (one plain HTTP download) first, then
// an HLS stream whose pieces simply follow one another (MP3, or AAC in fragmented MP4 whose
// init segment goes first). Ogg/Opus HLS, adaptive master playlists and unknown types are
// not taken; with nothing usable ErrNoStream lets the caller fall back.
func pick(ts []transcoding) (choice, error) {
	var best *choice
	for _, t := range ts {
		mime := strings.ToLower(t.Format.MimeType)
		hls := t.Format.Protocol == "hls"
		var ext string
		switch {
		case strings.HasPrefix(mime, "audio/mpeg"):
			ext = ".mp3"
		case strings.HasPrefix(mime, "audio/mp4"):
			ext = ".m4a"
		case !hls && (strings.HasPrefix(mime, "audio/ogg") || strings.Contains(mime, "opus")):
			ext = ".opus"
		default:
			continue
		}
		if t.Format.Protocol != "progressive" && !hls || strings.HasPrefix(t.Preset, "abr_") || t.URL == "" {
			continue
		}
		rank := 10
		if hls {
			rank = 20
		}
		if ext == ".m4a" {
			rank += 5
		}
		if strings.Contains(t.Preset, "160") || strings.HasPrefix(t.Preset, "mp3_0") {
			rank-- // the better preset of its kind
		}
		if best == nil || rank < best.rank {
			best = &choice{t, ext, rank}
		}
	}
	if best == nil {
		return choice{}, ErrNoStream
	}
	return *best, nil
}

// Fetch downloads the audio of t to base+ext and returns that path. onProgress is told the
// bytes so far and the total (0 when unknown).
func (c *Client) Fetch(ctx context.Context, t Track, base string, onProgress func(got, total int64)) (string, error) {
	// Resolve again: the stream links and track_authorization are short-lived.
	var obj apiObject
	if err := c.getJSON(ctx, "/resolve", url.Values{"url": {t.URL}}, &obj); err != nil {
		return "", err
	}
	if obj.Kind != "track" {
		return "", errors.New("soundcloud: not a track")
	}
	ch, err := pick(obj.Media.Transcodings)
	if err != nil {
		return "", err
	}
	var stream struct {
		URL string `json:"url"`
	}
	if err := c.getJSON(ctx, ch.t.URL, url.Values{"track_authorization": {obj.TrackAuthorization}}, &stream); err != nil {
		return "", err
	}
	if !strings.HasPrefix(stream.URL, "https://") {
		return "", errors.New("soundcloud: no stream link")
	}
	path := base + ch.ext
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if ch.t.Format.Protocol == "progressive" {
		err = c.copyURL(ctx, stream.URL, f, 0, onProgress)
	} else {
		err = c.hls(ctx, stream.URL, f, onProgress)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// counter reports bytes written across several copies.
type counter struct {
	w     io.Writer
	n     *int64
	total int64
	cb    func(got, total int64)
}

func (c counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	*c.n += int64(n)
	if c.cb != nil {
		c.cb(*c.n, c.total)
	}
	return n, err
}

// copyURL appends the body of u to w.
func (c *Client) copyURL(ctx context.Context, u string, w io.Writer, total int64, cb func(got, total int64)) error {
	var n int64
	return c.copyURLN(ctx, u, w, &n, total, cb)
}

func (c *Client) copyURLN(ctx context.Context, u string, w io.Writer, n *int64, total int64, cb func(got, total int64)) error {
	resp, err := c.getRetry(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("soundcloud: HTTP %d fetching audio", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return errors.New("soundcloud: got a web page instead of audio")
	}
	if total == 0 {
		total = resp.ContentLength
		if total < 0 {
			total = 0
		}
	}
	_, err = io.Copy(counter{w, n, total, cb}, resp.Body)
	return err
}

// hls downloads a media playlist's pieces one after another into w. Only plain playlists
// are taken: no encryption, no byte ranges, no variant (master) playlists.
func (c *Client) hls(ctx context.Context, playlistURL string, w io.Writer, cb func(got, total int64)) error {
	resp, err := c.getRetry(ctx, playlistURL)
	if err != nil {
		return err
	}
	b, err := readAll(resp, 4<<20)
	if err != nil {
		return err
	}
	pl, kind, err := m3u8.DecodeFrom(bytes.NewReader(b), true)
	if err != nil || kind != m3u8.MEDIA {
		return ErrNoStream
	}
	mp := pl.(*m3u8.MediaPlaylist)
	base, err := url.Parse(playlistURL)
	if err != nil {
		return err
	}
	abs := func(ref string) string {
		r, err := url.Parse(ref)
		if err != nil {
			return ""
		}
		return base.ResolveReference(r).String()
	}
	var parts []string
	for _, s := range mp.Segments {
		if s == nil {
			continue
		}
		if s.Key != nil && s.Key.Method != "" && s.Key.Method != "NONE" || s.Limit > 0 || s.Offset > 0 {
			return ErrNoStream
		}
		if s.Map != nil && s.Map.Limit == 0 && len(parts) == 0 {
			parts = append(parts, abs(s.Map.URI)) // fMP4: the init segment comes first
		}
		parts = append(parts, abs(s.URI))
	}
	if mp.Key != nil && mp.Key.Method != "" && mp.Key.Method != "NONE" {
		return ErrNoStream
	}
	if len(parts) == 0 {
		return ErrNoStream
	}
	var got int64
	for _, p := range parts {
		if p == "" {
			return ErrNoStream
		}
		if err := c.copyURLN(ctx, p, w, &got, 0, cb); err != nil {
			return err
		}
	}
	return nil
}
