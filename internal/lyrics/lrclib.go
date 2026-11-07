package lyrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// LRCLIBName and LRCLIBBase: https://lrclib.net, a free community database of synced lyrics.
const (
	LRCLIBName = "LRCLIB"
	LRCLIBBase = "https://lrclib.net"
)

// LRCLIB asks lrclib.net for lyrics. It is told the title, artist, album and length of the
// song, as its API takes them, and nothing else.
type LRCLIB struct {
	Client *http.Client
	Base   string // default LRCLIBBase
}

func (p *LRCLIB) Name() string { return LRCLIBName }

func (p *LRCLIB) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return newClient()
}

func (p *LRCLIB) base() string {
	if p.Base != "" {
		return p.Base
	}
	return LRCLIBBase
}

// Fetch tries the exact lookup first (title, artist, album and length all agree), then a
// search; the ranking chooses among whatever comes back.
func (p *LRCLIB) Fetch(ctx context.Context, q Query) ([]Candidate, error) {
	artist := primaryArtist(q.Artist)
	if q.Title == "" || artist == "" {
		return nil, nil
	}
	if q.Album != "" && q.Duration > 0 {
		params := url.Values{"track_name": {searchTitle(q.Title)}, "artist_name": {artist}, "album_name": {q.Album},
			"duration": {strconv.Itoa(int(q.Duration.Round(time.Second) / time.Second))}}
		body, err := get(ctx, p.client(), LRCLIBName, p.base(), "/api/get", params)
		if err != nil {
			return nil, err
		}
		if c := parseLRCLIB(body); len(c) > 0 {
			return c, nil
		}
	}
	params := url.Values{"track_name": {searchTitle(q.Title)}, "artist_name": {artist}}
	body, err := get(ctx, p.client(), LRCLIBName, p.base(), "/api/search", params)
	if err != nil {
		return nil, err
	}
	return parseLRCLIB(body), nil
}

type lrclibRecord struct {
	TrackName    string   `json:"trackName"`
	Name         string   `json:"name"`
	ArtistName   string   `json:"artistName"`
	AlbumName    string   `json:"albumName"`
	Duration     *float64 `json:"duration"`
	Instrumental bool     `json:"instrumental"`
	Plain        *string  `json:"plainLyrics"`
	Synced       *string  `json:"syncedLyrics"`
}

// parseLRCLIB reads /api/get (one object) or /api/search (an array).
func parseLRCLIB(body []byte) []Candidate {
	var recs []lrclibRecord
	if json.Unmarshal(body, &recs) != nil {
		var one lrclibRecord
		if json.Unmarshal(body, &one) != nil {
			return nil
		}
		recs = []lrclibRecord{one}
	}
	var out []Candidate
	for _, r := range recs {
		c := Candidate{Provider: LRCLIBName, Title: r.TrackName, Artist: r.ArtistName, Album: r.AlbumName, Instrumental: r.Instrumental}
		if c.Title == "" {
			c.Title = r.Name
		}
		if r.Duration != nil && *r.Duration > 0 {
			c.Duration = time.Duration(*r.Duration * float64(time.Second))
		}
		if r.Synced != nil {
			c.Synced, _ = ParseLRC(*r.Synced)
		}
		if r.Plain != nil {
			c.Plain = *r.Plain
		}
		if !c.Instrumental && len(c.Synced) == 0 && c.Plain == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}
