package engine

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/innertube"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
)

// goLookup reports whether lookups try the native player API first (YTM_RESOLVER=go).
// yt-dlp stays the default until the native path has proven itself; it is always the
// fallback for whatever the native path can't handle.
func goLookup() bool { return strings.EqualFold(os.Getenv("YTM_RESOLVER"), "go") }

// resolveGo looks t up with one player API request on the least-loaded exit: about 0.2 s,
// against ~2 s for a yt-dlp process. Any failure (bot check, age gate, no plain audio
// stream) returns an error and the track goes the yt-dlp way.
func (e *engine) resolveGo(ctx context.Context, t model.Track) (*Resolved, *lane, int64, error) {
	ln := e.pick()
	if err := ln.gov.Acquire(ctx); err != nil {
		return nil, nil, 0, err
	}
	epoch := ln.epoch.Load()
	v, err := e.player(ctx, ln, t.ID)
	var he *innertube.HTTPError
	switch {
	case err == nil:
		ln.gov.Release(OK)
	case errors.As(err, &he) && he.Status == 429:
		ln.gov.Release(Throttled)
	default:
		ln.gov.Release(Neutral)
	}
	if err != nil {
		return nil, nil, 0, err
	}
	r, err := e.fromPlayer(v)
	return r, ln, epoch, err
}

// player asks the API, carrying the exit's visitor id. YouTube answers a request without
// one with "confirm you're not a bot" but hands one out in that answer: the first lookup on
// an exit is retried once with it, and later ones send it from the start.
func (e *engine) player(ctx context.Context, ln *lane, id string) (*innertube.Video, error) {
	c := innertube.Clients[0]
	visitor, _ := ln.visitor.Load().(string)
	v, err := innertube.Player(ctx, ln.http(), c, id, visitor)
	if v != nil && v.VisitorData != "" && visitor == "" {
		ln.visitor.Store(v.VisitorData)
		var up *innertube.Unplayable
		if errors.As(err, &up) && up.Status == "LOGIN_REQUIRED" {
			v, err = innertube.Player(ctx, ln.http(), c, id, v.VisitorData)
		}
	}
	return v, err
}

// fromPlayer turns a player answer into a Resolved, picking the stream that copies
// straight into the target format.
func (e *engine) fromPlayer(v *innertube.Video) (*Resolved, error) {
	itags, codec := []int{774, 251}, "opus"
	switch e.Format {
	case "m4a":
		itags, codec = []int{141, 140}, "mp4a"
	case "mp3", "flac":
		itags, codec = nil, "" // re-encoded anyway: the best of anything
	}
	f, ok := innertube.BestAudio(v, itags, codec)
	if !ok {
		return nil, innertube.ErrNoAudio
	}
	r := &Resolved{
		URL: f.URL, Headers: map[string]string{"User-Agent": v.UserAgent},
		Ext: f.Ext(), Acodec: f.Codec(), Protocol: "https", Size: f.ContentLength,
		Title: v.Title, Duration: v.Duration, Thumb: v.Thumbnail,
	}
	if m, ok := innertube.ParseMusic(v.Description); ok {
		r.Title, r.Artists, r.Album, r.Date, r.Year = m.Title, m.Artists, m.Album, m.Date, m.Year
	}
	if len(r.Artists) == 0 {
		if a := strings.TrimSuffix(v.Author, " - Topic"); a != "" {
			r.Artists = []string{a}
		}
	}
	if !canFetch(r) {
		return nil, innertube.ErrNoAudio // no size: let yt-dlp download it
	}
	return r, nil
}
