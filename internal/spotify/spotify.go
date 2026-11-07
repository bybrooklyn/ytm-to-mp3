// Package spotify resolves public Spotify albums, playlists and tracks to
// canonical track metadata, with no login and no API key.
//
// Spotify's own audio is DRM-encrypted and is not downloaded here; Spotify is
// used only as a source of truth for what a release contains, and ytm then
// fetches the audio from the best available source (Apple Music, YouTube).
//
// The metadata comes from Spotify's public embed pages
// (open.spotify.com/embed/<kind>/<id>), whose HTML carries the full track list
// as JSON in a __NEXT_DATA__ script. This needs no token: the official Web API
// now rejects anonymous tokens (429) and the web player's token endpoint is
// gated behind a rotating TOTP, so the embed page is both simpler and steadier.
package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/names"
)

// Track is one Spotify song reduced to what ytm needs to find and tag it.
// Spotify embed data has no ISRC, and playlist entries carry no per-track
// album, so those fields may be empty.
type Track struct {
	ID             string
	Title          string
	Artists        []string
	Album          string
	DurationMillis int
	ArtworkURL     string
	ReleaseDate    string
	// TrackNo/TrackTotal: the position on the album, for an album link (the embed lists an
	// album's songs in order); 0 for playlists and single tracks.
	TrackNo, TrackTotal int
}

// ErrNotSpotifyURL is returned for a link that is not a recognised Spotify
// album, playlist or track.
var ErrNotSpotifyURL = errors.New("not a Spotify URL")

const browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// client is the HTTP client used for embed requests; SetProxy swaps it.
var client = http.DefaultClient

// SetProxy routes Spotify requests through proxyURL (e.g. a Proton exit's
// "socks5://host:port"); "" means a direct connection.
func SetProxy(proxyURL string) error {
	c, err := proxyClient(proxyURL)
	if err != nil {
		return err
	}
	client = c
	return nil
}

// spotifyURL matches album/playlist/track links and URIs, with an optional
// /intl-xx/ locale segment: open.spotify.com/intl-de/album/<id>,
// spotify:track:<id>, etc.
var spotifyURL = regexp.MustCompile(`(?:open\.spotify\.com/(?:intl-[a-z]{2}/)?|spotify:)(album|playlist|track)[:/]([A-Za-z0-9]+)`)

// Parse returns the kind ("album", "playlist" or "track") and id of a Spotify
// link or URI.
func Parse(link string) (kind, id string, ok bool) {
	m := spotifyURL.FindStringSubmatch(link)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// IsURL reports whether link is a Spotify album, playlist or track reference.
func IsURL(link string) bool {
	_, _, ok := Parse(link)
	return ok
}

// Resolve expands a Spotify album, playlist or track link into its tracks.
func Resolve(ctx context.Context, link string) ([]Track, error) {
	kind, id, ok := Parse(link)
	if !ok {
		return nil, ErrNotSpotifyURL
	}
	ent, err := fetchEntity(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "track":
		return []Track{trackFromEntity(ent)}, nil
	default:
		return tracksFromList(ent), nil
	}
}

// --- embed page parsing ----------------------------------------------------

// entity is the slice of __NEXT_DATA__ that matters: the album/playlist/track
// at props.pageProps.state.data.entity.
type entity struct {
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	URI         string      `json:"uri"`
	Title       string      `json:"title"`
	Subtitle    string      `json:"subtitle"`
	ReleaseDate releaseDate `json:"releaseDate"`
	Duration    int         `json:"duration"`
	Artists     []artist    `json:"artists"`
	CoverArt    sources     `json:"coverArt"`
	Visual      visual      `json:"visualIdentity"`
	TrackList   []listItem  `json:"trackList"`
}

type artist struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

type listItem struct {
	URI      string `json:"uri"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"` // artist(s), comma-joined
	Duration int    `json:"duration"`
}

type releaseDate struct {
	ISODate string `json:"isoString"`
}

type sources struct {
	Sources []image `json:"sources"`
}
type visual struct {
	Image []image `json:"image"`
}
type image struct {
	URL       string `json:"url"`
	MaxHeight int    `json:"maxHeight"`
}

var nextData = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)

func fetchEntity(ctx context.Context, kind, id string) (*entity, error) {
	url := "https://open.spotify.com/embed/" + kind + "/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("spotify %s %s not found (is it public?)", kind, id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify embed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	m := nextData.FindSubmatch(body)
	if m == nil {
		return nil, errors.New("spotify embed: no data found (page format changed?)")
	}
	var doc struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity entity `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(m[1], &doc); err != nil {
		return nil, fmt.Errorf("spotify embed: %w", err)
	}
	e := doc.Props.PageProps.State.Data.Entity
	if e.Type == "" && len(e.TrackList) == 0 && e.Name == "" && e.Title == "" {
		return nil, errors.New("spotify embed: empty entity")
	}
	return &e, nil
}

func trackFromEntity(e *entity) Track {
	var artists []string
	for _, a := range e.Artists {
		if a.Name != "" {
			artists = append(artists, a.Name)
		}
	}
	name := e.Name
	if name == "" {
		name = e.Title
	}
	return Track{
		ID: idFromURI(e.URI), Title: name, Artists: artists,
		DurationMillis: e.Duration, ReleaseDate: e.ReleaseDate.ISODate,
		ArtworkURL: bestImage(e),
	}
}

func tracksFromList(e *entity) []Track {
	album, date := "", ""
	isAlbum := e.Type == "album"
	if isAlbum {
		album, date = e.Name, e.ReleaseDate.ISODate
	}
	art := bestImage(e)
	out := make([]Track, 0, len(e.TrackList))
	for i, it := range e.TrackList {
		t := Track{
			ID: idFromURI(it.URI), Title: it.Title, Artists: splitArtists(it.Subtitle),
			Album: album, DurationMillis: it.Duration, ArtworkURL: art, ReleaseDate: date,
		}
		if isAlbum {
			t.TrackNo, t.TrackTotal = i+1, len(e.TrackList)
		}
		out = append(out, t)
	}
	return out
}

// splitArtists turns "A, B & C" into ["A","B","C"].
func splitArtists(s string) []string { return names.SplitArtists(s) }

func idFromURI(uri string) string {
	if i := strings.LastIndex(uri, ":"); i >= 0 {
		return uri[i+1:]
	}
	return uri
}

// bestImage returns the largest cover image URL available on the entity.
func bestImage(e *entity) string {
	best, bestH := "", -1
	for _, im := range append(append([]image{}, e.CoverArt.Sources...), e.Visual.Image...) {
		if im.URL != "" && im.MaxHeight >= bestH {
			best, bestH = im.URL, im.MaxHeight
		}
	}
	return best
}
