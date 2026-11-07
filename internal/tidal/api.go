package tidal

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
)

// Track is a Tidal song with what ytm tags and files it by.
type Track struct {
	ID          int64
	Title       string // with the version in brackets when there is one ("Song (Live)")
	Artists     []string
	Album       string
	AlbumID     int64
	AlbumArtist string
	TrackNo     int
	TrackTotal  int
	DiscNo      int
	DiscTotal   int
	DurationSec int
	ISRC        string
	Explicit    bool
	Cover       string // the image uuid, with dashes
	ReleaseDate string // YYYY-MM-DD
	Copyright   string
	Atmos       bool // a Dolby Atmos edition (not a stereo master)
}

// URL is the song's page.
func (t Track) URL() string { return "https://tidal.com/track/" + strconv.FormatInt(t.ID, 10) }

// CoverURL is the album art at 1280 px, or "".
func (t Track) CoverURL() string { return CoverURL(t.Cover) }

// CoverURL turns an image uuid ("ab-cd-ef") into its 1280 px URL.
func CoverURL(uuid string) string {
	if uuid == "" {
		return ""
	}
	return imageBase + "/" + strings.ReplaceAll(uuid, "-", "/") + "/1280x1280.jpg"
}

// Catalog is the track as ytm's source-agnostic song.
func (t Track) Catalog() catalog.Track {
	return catalog.Track{
		Title: t.Title, Artists: t.Artists, Album: t.Album, AlbumArtist: t.AlbumArtist,
		TrackNo: t.TrackNo, TrackTotal: t.TrackTotal, DiscNo: t.DiscNo, DiscTotal: t.DiscTotal,
		DurationMillis: t.DurationSec * 1000, ISRC: t.ISRC, ArtworkURL: t.CoverURL(), ReleaseDate: t.ReleaseDate,
		Origin: catalog.Tidal, OriginID: strconv.FormatInt(t.ID, 10),
	}
}

// --- wire shapes ---------------------------------------------------------------

type jArtist struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type jAlbum struct {
	ID              int64     `json:"id"`
	Title           string    `json:"title"`
	Cover           string    `json:"cover"`
	ReleaseDate     string    `json:"releaseDate"`
	NumberOfTracks  int       `json:"numberOfTracks"`
	NumberOfVolumes int       `json:"numberOfVolumes"`
	Artist          jArtist   `json:"artist"`
	Artists         []jArtist `json:"artists"`
}

type jTrack struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Version     string    `json:"version"`
	Duration    int       `json:"duration"`
	TrackNumber int       `json:"trackNumber"`
	VolumeNo    int       `json:"volumeNumber"`
	ISRC        string    `json:"isrc"`
	Explicit    bool      `json:"explicit"`
	Copyright   string    `json:"copyright"`
	Artist      jArtist   `json:"artist"`
	Artists     []jArtist `json:"artists"`
	Album       jAlbum    `json:"album"`
	AudioModes  []string  `json:"audioModes"`
	Allow       *bool     `json:"allowStreaming"`
}

func (j jTrack) track() Track {
	t := Track{
		ID: j.ID, Title: j.Title, Album: j.Album.Title, AlbumID: j.Album.ID, TrackNo: j.TrackNumber,
		DiscNo: j.VolumeNo, DurationSec: j.Duration, ISRC: strings.ToUpper(strings.TrimSpace(j.ISRC)),
		Explicit: j.Explicit, Cover: j.Album.Cover, ReleaseDate: j.Album.ReleaseDate, Copyright: j.Copyright,
	}
	if v := strings.TrimSpace(j.Version); v != "" && !strings.Contains(strings.ToLower(t.Title), strings.ToLower(v)) {
		t.Title += " (" + v + ")"
	}
	for _, a := range j.Artists {
		if a.Name != "" && (a.Type == "" || a.Type == "MAIN") {
			t.Artists = append(t.Artists, a.Name)
		}
	}
	if len(t.Artists) == 0 && j.Artist.Name != "" {
		t.Artists = []string{j.Artist.Name}
	}
	if len(j.AudioModes) > 0 {
		t.Atmos = true
		for _, m := range j.AudioModes {
			if m == "STEREO" {
				t.Atmos = false
			}
		}
	}
	t.fromAlbum(j.Album)
	return t
}

// fromAlbum fills what the album knows and the track doesn't.
func (t *Track) fromAlbum(a jAlbum) {
	if a.ReleaseDate != "" {
		t.ReleaseDate = a.ReleaseDate
	}
	if a.NumberOfTracks > 0 {
		t.TrackTotal = a.NumberOfTracks
	}
	if a.NumberOfVolumes > 0 {
		t.DiscTotal = a.NumberOfVolumes
	}
	if t.AlbumArtist == "" {
		for _, x := range a.Artists {
			if x.Name != "" && (x.Type == "" || x.Type == "MAIN") {
				t.AlbumArtist = joinNames(t.AlbumArtist, x.Name)
			}
		}
		if t.AlbumArtist == "" {
			t.AlbumArtist = a.Artist.Name
		}
	}
	if a.Cover != "" {
		t.Cover = a.Cover
	}
}

func joinNames(a, b string) string {
	if a == "" {
		return b
	}
	return a + ", " + b
}

// --- links ---------------------------------------------------------------------

// Link is a parsed tidal.com URL.
type Link struct {
	Kind string // "track", "album" or "playlist"
	ID   string
	// Track is the song within an album link (".../album/1/track/2"), or 0.
	Track int64
}

var linkRE = regexp.MustCompile(`^/(?:browse/)?(track|album|playlist)/([0-9A-Za-z-]+)(?:/track/(\d+))?/?$`)

// ParseURL understands tidal.com links to a track, album or playlist.
func ParseURL(raw string) (Link, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Link{}, false
	}
	switch strings.ToLower(u.Host) {
	case "tidal.com", "www.tidal.com", "listen.tidal.com", "open.tidal.com":
	default:
		return Link{}, false
	}
	m := linkRE.FindStringSubmatch(u.Path)
	if m == nil {
		return Link{}, false
	}
	l := Link{Kind: m[1], ID: m[2]}
	if m[3] != "" {
		l.Track, _ = strconv.ParseInt(m[3], 10, 64)
	}
	if l.Kind != "playlist" {
		if _, err := strconv.ParseInt(l.ID, 10, 64); err != nil {
			return Link{}, false
		}
	}
	return l, true
}

// IsURL reports whether s is a Tidal track, album or playlist link.
func IsURL(s string) bool {
	_, ok := ParseURL(s)
	return ok
}

// --- lookups -------------------------------------------------------------------

type page struct {
	Items []struct {
		Type string `json:"type"`
		Item jTrack `json:"item"`
	} `json:"items"`
	Total int `json:"totalNumberOfItems"`
}

func (c *Client) album(ctx context.Context, id int64) (jAlbum, error) {
	var a jAlbum
	err := c.get(ctx, fmt.Sprintf("/albums/%d", id), nil, &a)
	return a, err
}

func (c *Client) pages(ctx context.Context, path string) ([]jTrack, error) {
	var out []jTrack
	for offset := 0; ; {
		var p page
		if err := c.get(ctx, path, url.Values{"limit": {"100"}, "offset": {strconv.Itoa(offset)}}, &p); err != nil {
			return nil, err
		}
		for _, it := range p.Items {
			if it.Type == "" || it.Type == "track" {
				out = append(out, it.Item)
			}
		}
		offset += len(p.Items)
		if len(p.Items) == 0 || offset >= p.Total {
			return out, nil
		}
	}
}

// Track looks one song up, with its album's details.
func (c *Client) Track(ctx context.Context, id int64) (Track, error) {
	var j jTrack
	if err := c.get(ctx, fmt.Sprintf("/tracks/%d", id), nil, &j); err != nil {
		return Track{}, err
	}
	t := j.track()
	c.Enrich(ctx, &t)
	return t, nil
}

// Enrich fills the album details (release date, album artist, track total) that a search
// or playlist entry lacks. A failure leaves the track as it was.
func (c *Client) Enrich(ctx context.Context, t *Track) {
	if t.AlbumID == 0 || (t.AlbumArtist != "" && t.ReleaseDate != "" && t.TrackTotal > 0) {
		return
	}
	if a, err := c.album(ctx, t.AlbumID); err == nil {
		t.fromAlbum(a)
	}
}

// Album is every song of an album, in order.
func (c *Client) Album(ctx context.Context, id int64) ([]Track, error) {
	a, err := c.album(ctx, id)
	if err != nil {
		return nil, err
	}
	js, err := c.pages(ctx, fmt.Sprintf("/albums/%d/items", id))
	if err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(js))
	for _, j := range js {
		if j.Album.ID == 0 {
			j.Album.ID = a.ID
		}
		t := j.track()
		t.Album = firstNonEmpty(t.Album, a.Title)
		t.fromAlbum(a)
		out = append(out, t)
	}
	return out, nil
}

// Playlist is every song of a playlist, in order.
func (c *Client) Playlist(ctx context.Context, uuid string) ([]Track, error) {
	js, err := c.pages(ctx, "/playlists/"+url.PathEscape(uuid)+"/items")
	if err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(js))
	for _, j := range js {
		out = append(out, j.track())
	}
	return out, nil
}

// ResolveURL turns a Tidal link into its songs. A song's link gives the song alone unless
// whole is set, which gives the album it is on.
func (c *Client) ResolveURL(ctx context.Context, link string, whole bool) ([]Track, error) {
	l, ok := ParseURL(link)
	if !ok {
		return nil, fmt.Errorf("%q isn't a Tidal track, album or playlist link", link)
	}
	switch l.Kind {
	case "playlist":
		return c.Playlist(ctx, l.ID)
	case "album":
		id, _ := strconv.ParseInt(l.ID, 10, 64)
		ts, err := c.Album(ctx, id)
		if err != nil || l.Track == 0 || whole {
			return ts, err
		}
		for _, t := range ts {
			if t.ID == l.Track {
				return []Track{t}, nil
			}
		}
		return ts, nil
	}
	id, _ := strconv.ParseInt(l.ID, 10, 64)
	t, err := c.Track(ctx, id)
	if err != nil {
		return nil, err
	}
	if whole && t.AlbumID != 0 {
		if ts, err := c.Album(ctx, t.AlbumID); err == nil && len(ts) > 0 {
			return ts, nil
		}
	}
	return []Track{t}, nil
}

// Search finds songs by free text.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Track, error) {
	var r struct {
		Items []jTrack `json:"items"`
	}
	if err := c.get(ctx, "/search/tracks", url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}}, &r); err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(r.Items))
	for _, j := range r.Items {
		if j.Allow != nil && !*j.Allow {
			continue
		}
		out = append(out, j.track())
	}
	return out, nil
}

// Match finds the Tidal edition of a song: the ISRC first, else the same title and artist
// (and length, when both are known). ok is false when nothing is certain.
func (c *Client) Match(ctx context.Context, want catalog.Track) (Track, bool, error) {
	q := strings.TrimSpace(want.Title + " " + want.PrimaryArtist())
	cands, err := c.Search(ctx, q, 15)
	if err != nil {
		return Track{}, false, err
	}
	t, ok := Pick(want, cands)
	return t, ok, nil
}

// Pick chooses the candidate that is the wanted song: an ISRC match, else a name (and
// length) match. Among equals a stereo edition beats Atmos, explicit beats clean.
func Pick(want catalog.Track, cands []Track) (Track, bool) {
	best, bestScore := Track{}, 0
	for _, t := range cands {
		score := 0
		switch {
		case want.ISRC != "" && t.ISRC != "" && strings.EqualFold(want.ISRC, t.ISRC):
			score = 100
		case want.ISRC != "" && t.ISRC != "":
			continue // different recordings
		case sameName(want, t):
			score = 50
		default:
			continue
		}
		if !t.Atmos {
			score += 4
		}
		if t.Explicit {
			score += 2
		}
		if score > bestScore {
			best, bestScore = t, score
		}
	}
	return best, bestScore > 0
}

func sameName(want catalog.Track, t Track) bool {
	if t.Title == "" || catalog.NameKey(want.Title, "") != catalog.NameKey(t.Title, "") {
		return false
	}
	named := false
	for _, a := range t.Artists {
		for _, w := range want.Artists {
			if catalog.NameKey("", a) == catalog.NameKey("", w) {
				named = true
			}
		}
	}
	if !named {
		return false
	}
	if want.DurationMillis > 0 && t.DurationSec > 0 {
		d := want.DurationMillis - t.DurationSec*1000
		if d > 4000 || d < -4000 {
			return false
		}
	}
	return true
}
