package tag

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Strict matching: ytm only touches a file when MusicBrainz is essentially certain.
const (
	minScore    = 98  // MusicBrainz's own search score
	maxDurDelta = 2.0 // seconds between file and recording length
)

type mbRecording struct {
	ID           string   `json:"id"`
	Score        int      `json:"score"`
	Title        string   `json:"title"`
	Length       int      `json:"length"` // ms
	ISRCs        []string `json:"isrcs"`
	ArtistCredit []struct {
		Name   string `json:"name"`
		Artist struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"artist-credit"`
	Tags []struct {
		Count int    `json:"count"`
		Name  string `json:"name"`
	} `json:"tags"`
	Releases []mbRelease `json:"releases"`
}

type mbRelease struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Date         string `json:"date"`
	Status       string `json:"status"`
	ReleaseGroup struct {
		PrimaryType    string   `json:"primary-type"`
		SecondaryTypes []string `json:"secondary-types"`
	} `json:"release-group"`
	// The release's own credit: set when it differs from the recording's (a compilation's
	// "Various Artists", a soundtrack).
	ArtistCredit []struct {
		Name   string `json:"name"`
		Artist struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"artist-credit"`
	Media []struct {
		Position   int `json:"position"`
		TrackCount int `json:"track-count"`
		Track      []struct {
			Number string `json:"number"`
		} `json:"track"`
	} `json:"media"`
}

// Client talks to MusicBrainz politely: one request per second, cached on disk.
type Client struct {
	HTTP     *http.Client
	Base     string // overridable for tests
	CacheDir string
	mu       sync.Mutex
	last     time.Time
	gap      time.Duration
}

func NewClient(cacheDir string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Base: "https://musicbrainz.org", CacheDir: cacheDir, gap: 1100 * time.Millisecond}
}

func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	key := ""
	if c.CacheDir != "" {
		h := sha1.Sum([]byte(u))
		key = filepath.Join(c.CacheDir, hex.EncodeToString(h[:])+".json")
		if b, err := os.ReadFile(key); err == nil {
			return b, nil
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		c.mu.Lock()
		if d := c.gap - time.Since(c.last); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				c.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		c.last = time.Now()
		c.mu.Unlock()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "ytm-to-mp3/1 ( https://github.com/bybrooklyn/ytm-to-mp3 )")
		req.Header.Set("Accept", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
			time.Sleep(time.Duration(attempt+1) * 3 * time.Second)
			continue
		}
		if resp.StatusCode != http.StatusOK || rerr != nil {
			return nil, fmt.Errorf("musicbrainz: %s", resp.Status)
		}
		if key != "" {
			_ = os.MkdirAll(c.CacheDir, 0o700)
			_ = os.WriteFile(key, b, 0o600)
		}
		return b, nil
	}
	return nil, fmt.Errorf("musicbrainz unavailable")
}

// SearchRecordings looks a song up by title and artist.
func (c *Client) SearchRecordings(ctx context.Context, title, artist string) ([]mbRecording, error) {
	q := fmt.Sprintf(`recording:"%s" AND artist:"%s"`, lucene(title), lucene(artist))
	u := c.Base + "/ws/2/recording?fmt=json&limit=10&query=" + url.QueryEscape(q)
	b, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var r struct {
		Recordings []mbRecording `json:"recordings"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return r.Recordings, nil
}

func lucene(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// Match is a MusicBrainz result we trust enough to write.
type Match struct {
	Recording mbRecording
	Release   *mbRelease // nil when the album can't be pinned down unambiguously
	Track     string
	Disc      int
}

// Decide returns a Match only when everything lines up: same title, same primary
// artist, duration within a couple of seconds, a top search score, and no rival
// candidate. Otherwise it returns nil and a short reason.
func Decide(title, artist, album string, duration float64, recs []mbRecording) (*Match, string) {
	if title == "" || artist == "" {
		return nil, "missing title or artist"
	}
	if duration <= 0 {
		return nil, "unknown duration"
	}
	wantT := norm(title)
	var wantA []string
	for _, a := range splitArtists(artist) {
		wantA = append(wantA, norm(a))
	}
	if wantT == "" || len(wantA) == 0 {
		return nil, "unusable title or artist"
	}
	var ok []mbRecording
	for _, r := range recs {
		if r.Score < minScore || r.Length <= 0 || norm(r.Title) != wantT {
			continue
		}
		if math.Abs(float64(r.Length)/1000-duration) > maxDurDelta {
			continue
		}
		credited := false
		for _, ac := range r.ArtistCredit {
			if n := norm(ac.Artist.Name); n == wantA[0] || norm(ac.Name) == wantA[0] {
				credited = true
			}
		}
		if credited {
			ok = append(ok, r)
		}
	}
	switch len(ok) {
	case 0:
		return nil, "no candidate matches title, artist and length"
	case 1:
	default:
		// The same song can be several recordings with near-identical lengths; refuse to guess.
		sort.Slice(ok, func(i, j int) bool { return ok[i].Score > ok[j].Score })
		if ok[0].Score == ok[1].Score {
			return nil, "ambiguous: several equally good recordings"
		}
	}
	m := &Match{Recording: ok[0]}
	m.Release, m.Track, m.Disc = pickRelease(ok[0].Releases, album)
	return m, ""
}

// pickRelease chooses an album only if it is unambiguous: the release whose title
// equals the file's current album, or the single official studio album.
func pickRelease(rels []mbRelease, album string) (*mbRelease, string, int) {
	var cands []mbRelease
	for _, r := range rels {
		if r.Status == "Official" && r.ReleaseGroup.PrimaryType == "Album" && len(r.ReleaseGroup.SecondaryTypes) == 0 {
			cands = append(cands, r)
		}
	}
	var chosen *mbRelease
	if album != "" {
		for i := range cands {
			if norm(cands[i].Title) == norm(album) {
				if chosen != nil && norm(chosen.Title) == norm(cands[i].Title) && chosen.Date != cands[i].Date {
					// Same-titled reissues: take the earliest.
					if cands[i].Date != "" && (chosen.Date == "" || cands[i].Date < chosen.Date) {
						chosen = &cands[i]
					}
					continue
				}
				if chosen == nil {
					chosen = &cands[i]
				}
			}
		}
	} else {
		titles := map[string]bool{}
		for _, r := range cands {
			titles[norm(r.Title)] = true
		}
		if len(titles) == 1 {
			first := &cands[0]
			for i := range cands {
				if cands[i].Date != "" && (first.Date == "" || cands[i].Date < first.Date) {
					first = &cands[i]
				}
			}
			chosen = first
		}
	}
	if chosen == nil {
		return nil, "", 0
	}
	track, disc := "", 0
	for _, m := range chosen.Media {
		if len(m.Track) > 0 {
			track, disc = m.Track[0].Number, m.Position
			break
		}
	}
	return chosen, track, disc
}

// CoverURL is the Cover Art Archive front image for a release.
func CoverURL(releaseID string) string {
	return "https://coverartarchive.org/release/" + releaseID + "/front-1200"
}
