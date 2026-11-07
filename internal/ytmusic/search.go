package ytmusic

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/model"
)

// Search filters (the "params" YouTube Music's own search box sends for "Songs" and "Albums").
const (
	filterSongs  = "EgWKAQIIAWoMEA4QChADEAQQCRAF"
	filterAlbums = "EgWKAQIYAWoMEA4QChADEAQQCRAF"
)

// ErrNothing means the page held nothing usable (not signed in, removed, or a format change).
var ErrNothing = errors.New("YouTube Music returned nothing usable")

// Hit is one song found by a search or listed on a page.
type Hit struct{ ID, Title string }

// SearchSongs looks query up in YouTube Music's song search and returns up to n results.
func (c *Client) SearchSongs(ctx context.Context, query string, n int) ([]Hit, error) {
	v, err := c.post(ctx, c.search, map[string]any{"query": query, "params": filterSongs})
	if err != nil {
		return nil, err
	}
	var out []Hit
	for _, t := range songs(v, "musicShelfRenderer") {
		out = append(out, Hit{ID: t.ID, Title: t.Title})
		if len(out) == n {
			break
		}
	}
	return out, nil
}

// AlbumHit is one album found by a search.
type AlbumHit struct{ BrowseID, Title string }

// SearchAlbums looks query up in YouTube Music's album search.
func (c *Client) SearchAlbums(ctx context.Context, query string, n int) ([]AlbumHit, error) {
	v, err := c.post(ctx, c.search, map[string]any{"query": query, "params": filterAlbums})
	if err != nil {
		return nil, err
	}
	var out []AlbumHit
	seen := map[string]bool{}
	walk(v, func(k string, o obj) {
		if k != "musicShelfRenderer" {
			return
		}
		for _, it := range asList(o["contents"]) {
			r := asObj(asObj(it)["musicResponsiveListItemRenderer"])
			if r == nil {
				continue
			}
			id := str(get(r, "navigationEndpoint", "browseEndpoint", "browseId"))
			if !strings.HasPrefix(id, "MPREb") || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, AlbumHit{BrowseID: id, Title: runsText(columnRuns(asList(r["flexColumns"]), 0))})
		}
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// Thumb is one size of an image.
type Thumb struct {
	URL  string
	W, H int
}

// AlbumPage is an album as its page describes it.
type AlbumPage struct {
	Title  string
	Year   int
	Thumbs []Thumb
	Tracks []Hit
}

// Album reads an album page (browse id MPREb_…).
func (c *Client) Album(ctx context.Context, browseID string) (*AlbumPage, error) {
	if !strings.HasPrefix(browseID, "MPREb_") || strings.ContainsAny(browseID, "/.?&# ") {
		return nil, errors.New("not an album id")
	}
	var a AlbumPage
	var head obj
	err := c.page(ctx, browseID, func(r any) {
		if head == nil {
			head = headerObj(r)
		}
		for _, t := range songs(r, "musicShelfRenderer") {
			a.Tracks = append(a.Tracks, Hit{ID: t.ID, Title: t.Title})
		}
	})
	if err != nil {
		return nil, err
	}
	if head == nil || len(a.Tracks) == 0 {
		return nil, ErrNothing
	}
	a.Title = runsText(runsOf(get(head, "title", "runs")))
	a.Year = yearIn(runsOf(get(head, "subtitle", "runs")))
	a.Thumbs = thumbsIn(head)
	return &a, nil
}

// Playlist lists the songs of a playlist (an id like PL…, OLAK5uy_…, or VL…).
func (c *Client) Playlist(ctx context.Context, id string) ([]model.Track, error) {
	id = strings.TrimPrefix(id, "VL")
	if id == "" || strings.ContainsAny(id, "/.?&# ") {
		return nil, errors.New("not a playlist id")
	}
	var out []model.Track
	seen := map[string]bool{}
	err := c.page(ctx, "VL"+id, func(r any) {
		for _, t := range songs(r, "musicPlaylistShelfRenderer") {
			if !seen[t.ID] {
				seen[t.ID] = true
				out = append(out, model.Track{ID: t.ID, Title: t.Title})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNothing
	}
	return out, nil
}

// headerObj finds the page's header renderer.
func headerObj(resp any) obj {
	var h obj
	walk(resp, func(k string, o obj) {
		if h != nil {
			return
		}
		switch k {
		case "musicResponsiveHeaderRenderer", "musicDetailHeaderRenderer", "musicImmersiveHeaderRenderer":
			h = o
		}
	})
	return h
}

var yearRe = regexp.MustCompile(`^(19|20)\d\d$`)

func yearIn(runs []obj) int {
	for _, r := range runs {
		if t := strings.TrimSpace(str(r["text"])); yearRe.MatchString(t) {
			n, _ := strconv.Atoi(t)
			return n
		}
	}
	return 0
}

var sizeRe = regexp.MustCompile(`=w(\d+)-h(\d+)`)

// thumbsIn collects every image under o, taking a missing width/height from the "=w544-h544"
// size suffix Google image URLs carry.
func thumbsIn(o obj) []Thumb {
	var out []Thumb
	var rec func(v any)
	rec = func(v any) {
		switch x := v.(type) {
		case obj:
			if list, ok := x["thumbnails"].([]any); ok {
				for _, it := range list {
					t := asObj(it)
					u := str(t["url"])
					if u == "" {
						continue
					}
					w, h := int(num(t["width"])), int(num(t["height"]))
					if m := sizeRe.FindStringSubmatch(u); m != nil && (w == 0 || h == 0) {
						w, _ = strconv.Atoi(m[1])
						h, _ = strconv.Atoi(m[2])
					}
					out = append(out, Thumb{URL: u, W: w, H: h})
				}
			}
			for _, c := range x {
				rec(c)
			}
		case []any:
			for _, c := range x {
				rec(c)
			}
		}
	}
	rec(o)
	return out
}

func num(v any) float64 { f, _ := v.(float64); return f }
