package engine

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ytmusic"
)

// nativeSearch reports whether searches and album/playlist listings try ytm's own
// YouTube Music client first (about 0.3 s, against ~2 s for a yt-dlp process). yt-dlp is
// always the fallback when the native answer is an error or empty, so a change in
// YouTube's response format costs speed, not results. YTM_SEARCH=yt-dlp turns it off.
func nativeSearch() bool { return !strings.EqualFold(os.Getenv("YTM_SEARCH"), "yt-dlp") }

const nativeTimeout = 25 * time.Second

// music returns a client that goes out through y's exit.
func (y ytdlp) music() (*ytmusic.Client, error) {
	hc, err := vpn.NewClient(y.proxy)
	if err != nil {
		return nil, err
	}
	return ytmusic.NewAnonymous(hc), nil
}

// nativeSearchMusic is SearchMusic without yt-dlp. ok=false means "ask yt-dlp".
func (y ytdlp) nativeSearchMusic(ctx context.Context, query string, n int) ([]musicHit, bool) {
	if !nativeSearch() {
		return nil, false
	}
	c, err := y.music()
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
	defer cancel()
	hits, err := c.SearchSongs(ctx, query, n)
	if err != nil || len(hits) == 0 {
		return nil, false
	}
	out := make([]musicHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, musicHit{ID: h.ID, Title: h.Title})
	}
	return out, true
}

// nativeExpand lists a playlist page or a music.youtube.com album page. Anything else (a
// watch link with a list, a channel, ...) is left to yt-dlp.
func (y ytdlp) nativeExpand(ctx context.Context, link string) ([]model.Track, bool) {
	if !nativeSearch() {
		return nil, false
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" {
		return nil, false
	}
	switch strings.ToLower(u.Hostname()) {
	case "music.youtube.com", "www.youtube.com", "youtube.com", "m.youtube.com":
	default:
		return nil, false
	}
	c, err := y.music()
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
	defer cancel()
	switch {
	case u.Path == "/playlist" && u.Query().Get("list") != "":
		ts, err := c.Playlist(ctx, u.Query().Get("list"))
		return ts, err == nil && len(ts) > 0
	case strings.HasPrefix(u.Path, "/browse/MPREb_"):
		a, err := c.Album(ctx, strings.TrimPrefix(u.Path, "/browse/"))
		if err != nil {
			return nil, false
		}
		var out []model.Track
		for _, t := range a.Tracks {
			out = append(out, model.Track{ID: t.ID, Title: t.Title})
		}
		return out, len(out) > 0
	}
	return nil, false
}

// nativeFindAlbum is FindAlbum's search and page reading without yt-dlp. ok=false means
// "ask yt-dlp"; ok=true with nil is a real "not found".
func (y ytdlp) nativeFindAlbum(ctx context.Context, q, album string, saved []model.Track) (*Album, bool, error) {
	if !nativeSearch() {
		return nil, false, nil
	}
	c, err := y.music()
	if err != nil {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*nativeTimeout)
	defer cancel()
	hits, err := c.SearchAlbums(ctx, q, albumCandidates)
	if err != nil || len(hits) == 0 {
		return nil, false, nil
	}
	read := 0
	var found *Album
	for _, h := range hits {
		// Once one edition matched, only other editions of the same album are worth opening.
		if found != nil && names.EditionKey(albumTitle(h.Title)) != names.EditionKey(album) {
			continue
		}
		p, err := c.Album(ctx, h.BrowseID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, true, ctx.Err()
			}
			continue
		}
		read++
		a := &Album{Playlist: h.BrowseID, Title: albumTitle(p.Title), Year: p.Year}
		best := 0
		for _, t := range p.Thumbs {
			if t.W != t.H || t.W > 1200 || t.W == 0 || !AlbumCoverURL(t.URL) {
				continue
			}
			score := t.W
			if strings.Contains(t.URL, "?") {
				score += 10000
			}
			if score > best {
				a.Cover, best = t.URL, score
			}
		}
		a.Cover = bigCover(a.Cover)
		for _, t := range p.Tracks {
			a.Tracks = append(a.Tracks, AlbumTrack{ID: t.ID, Title: t.Title})
		}
		if albumMatches(album, a, saved) {
			found = fullerAlbum(found, a, saved) // several editions match: the fullest one
		}
	}
	if found != nil {
		return found, true, nil
	}
	// No page could be read at all: more likely a format change than "not found".
	return nil, read > 0, nil
}

var coverSize = regexp.MustCompile(`=w\d+-h\d+`)

// bigCover asks Google's image server for the 1200 px version of an album cover: the page
// only lists up to 544 px, but the size is part of the URL and the server resizes.
func bigCover(u string) string {
	p, err := url.Parse(u)
	if err != nil || !strings.HasSuffix(strings.ToLower(p.Hostname()), ".googleusercontent.com") {
		return u
	}
	return coverSize.ReplaceAllString(u, "=w1200-h1200")
}
