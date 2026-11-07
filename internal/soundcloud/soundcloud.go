// Package soundcloud is a small client for the public API behind soundcloud.com: it turns a
// track, playlist/album or artist link into songs and fetches their audio. No login, no
// cookies, public tracks only. It says what it is (User-Agent "ytm") and does not pretend to
// be a browser: if SoundCloud answers with a challenge page instead of data, that is an
// error and the caller falls back to yt-dlp.
//
// The API needs a client_id, which the web app carries in its JavaScript. It rotates, so it
// is found at run time, kept for the session, and found again when the API refuses it.
package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	userAgent   = "ytm"
	defaultAPI  = "https://api-v2.soundcloud.com"
	defaultSite = "https://soundcloud.com/"
	maxPages    = 100 // an artist's tracks, 50 per page
)

// ErrNoStream means no usable audio format was offered; callers fall back to another tool.
var ErrNoStream = errors.New("soundcloud: no usable audio format offered")

// Track is one song with what SoundCloud says about it.
type Track struct {
	ID           int64
	URL          string
	Title        string
	Artist       string
	Album        string
	AlbumArtist  string
	Genre        string
	Date         string // YYYY-MM-DD
	ISRC         string
	Cover        string
	TrackNo      int
	TrackTotal   int
	Duration     float64 // seconds
	Downloadable bool    // the uploader enabled downloads (recorded only)
	Policy       string  // SoundCloud's policy field, e.g. MONETIZE (recorded only)
}

// Client talks to SoundCloud. Use one per exit; it is safe for concurrent use.
type Client struct {
	HC   *http.Client
	API  string // base of the API (tests point this at a fake)
	Site string // page the client_id is looked for from

	// Sleep waits between retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	clientID string
}

// New returns a client that uses hc (nil = http.DefaultClient).
func New(hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{HC: hc, API: defaultAPI, Site: defaultSite, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var (
	scriptRe   = regexp.MustCompile(`<script[^>]+src="(https://[^"]+\.js)"`)
	clientIDRe = regexp.MustCompile(`client_id\s*[:=]\s*"([0-9a-zA-Z]{32})"`)
)

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return c.HC.Do(req)
}

// getRetry is get with backoff on 429 (honouring Retry-After) and on 5xx.
func (c *Client) getRetry(ctx context.Context, u string) (*http.Response, error) {
	delay := time.Second
	for try := 0; ; try++ {
		resp, err := c.get(ctx, u)
		if err != nil {
			return nil, err
		}
		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500) || try >= 4 {
			return resp, nil
		}
		wait := delay
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 && s <= 60 {
			wait = time.Duration(s) * time.Second
		}
		resp.Body.Close()
		if err := c.Sleep(ctx, wait); err != nil {
			return nil, err
		}
		delay *= 2
	}
}

// discover finds a client_id in the web app's scripts (the last scripts hold it).
func (c *Client) discover(ctx context.Context) (string, error) {
	resp, err := c.getRetry(ctx, c.Site)
	if err != nil {
		return "", err
	}
	page, err := readAll(resp, 4<<20)
	if err != nil {
		return "", err
	}
	scripts := scriptRe.FindAllStringSubmatch(string(page), -1)
	for i := len(scripts) - 1; i >= 0; i-- {
		resp, err := c.getRetry(ctx, scripts[i][1])
		if err != nil {
			return "", err
		}
		js, err := readAll(resp, 8<<20)
		if err != nil {
			continue
		}
		if m := clientIDRe.FindSubmatch(js); m != nil {
			return string(m[1]), nil
		}
	}
	return "", errors.New("soundcloud: no client_id found in the web app")
}

func readAll(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("soundcloud: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// id returns the session's client_id, finding it first if needed. stale is the one that was
// just refused ("" = none): if another goroutine already replaced it, that one is used.
func (c *Client) id(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clientID != "" && c.clientID != stale {
		return c.clientID, nil
	}
	id, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	c.clientID = id
	return id, nil
}

// getJSON calls an API URL (absolute, or a path under API) with a client_id added, finds a
// new client_id once if the API refuses it, and decodes the JSON answer into v.
func (c *Client) getJSON(ctx context.Context, rawURL string, q url.Values, v any) error {
	if strings.HasPrefix(rawURL, "/") {
		rawURL = c.API + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	stale := ""
	for attempt := 0; attempt < 2; attempt++ {
		id, err := c.id(ctx, stale)
		if err != nil {
			return err
		}
		vals := u.Query()
		for k, vs := range q {
			vals[k] = vs
		}
		vals.Set("client_id", id)
		uu := *u
		uu.RawQuery = vals.Encode()
		resp, err := c.getRetry(ctx, uu.String())
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			stale = id
			continue
		}
		b, err := readAll(resp, 32<<20)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return errors.New("soundcloud: the API did not answer with JSON")
		}
		return nil
	}
	return errors.New("soundcloud: the API refused a fresh client_id")
}

// Resolve expands a SoundCloud link: a track, a playlist or album (set), or an artist's
// page (all their public tracks).
func (c *Client) Resolve(ctx context.Context, link string) ([]Track, error) {
	var obj apiObject
	if err := c.getJSON(ctx, "/resolve", url.Values{"url": {link}}, &obj); err != nil {
		return nil, err
	}
	switch obj.Kind {
	case "track":
		t, ok := mapTrack(obj.apiTrack)
		if !ok {
			return nil, errors.New("soundcloud: not a public track")
		}
		return []Track{t}, nil
	case "playlist":
		return c.playlist(ctx, obj)
	case "user":
		return c.userTracks(ctx, obj.ID)
	}
	return nil, fmt.Errorf("soundcloud: cannot take a %q link", obj.Kind)
}

// playlist maps a set's tracks. Tracks past the first few come as stubs with only an id;
// those are filled in with batched /tracks?ids= calls, keeping the set's order.
func (c *Client) playlist(ctx context.Context, p apiObject) ([]Track, error) {
	full := map[int64]apiTrack{}
	var missing []int64
	for _, t := range p.Tracks {
		if t.Title != "" {
			full[t.ID] = t
		} else {
			missing = append(missing, t.ID)
		}
	}
	for len(missing) > 0 {
		n := min(len(missing), 50)
		ids := make([]string, n)
		for i, id := range missing[:n] {
			ids[i] = strconv.FormatInt(id, 10)
		}
		missing = missing[n:]
		var got []apiTrack
		if err := c.getJSON(ctx, "/tracks", url.Values{"ids": {strings.Join(ids, ",")}}, &got); err != nil {
			return nil, err
		}
		for _, t := range got {
			full[t.ID] = t
		}
	}
	album := p.IsAlbum || p.SetType == "album" || p.SetType == "ep" || p.SetType == "single"
	var out []Track
	for _, st := range p.Tracks {
		at, ok := full[st.ID]
		if !ok {
			continue // removed or private since the set was listed
		}
		t, ok := mapTrack(at)
		if !ok {
			continue
		}
		if album {
			if t.Album == "" {
				t.Album = p.Title
			}
			t.AlbumArtist = firstNonEmpty(p.User.Username, t.Artist)
		}
		out = append(out, t)
	}
	if album {
		for i := range out {
			out[i].TrackNo, out[i].TrackTotal = i+1, len(out)
		}
	}
	return out, nil
}

func (c *Client) userTracks(ctx context.Context, userID int64) ([]Track, error) {
	var out []Track
	next := fmt.Sprintf("/users/%d/tracks", userID)
	q := url.Values{"limit": {"50"}, "linked_partitioning": {"1"}}
	for page := 0; next != "" && page < maxPages; page++ {
		var res struct {
			Collection []apiTrack `json:"collection"`
			NextHref   string     `json:"next_href"`
		}
		if err := c.getJSON(ctx, next, q, &res); err != nil {
			return nil, err
		}
		for _, at := range res.Collection {
			if t, ok := mapTrack(at); ok {
				out = append(out, t)
			}
		}
		next, q = res.NextHref, nil
	}
	return out, nil
}

type apiUser struct {
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type apiTrack struct {
	ID                 int64   `json:"id"`
	Title              string  `json:"title"`
	PermalinkURL       string  `json:"permalink_url"`
	Genre              string  `json:"genre"`
	Duration           float64 `json:"duration"` // milliseconds
	ArtworkURL         string  `json:"artwork_url"`
	ReleaseDate        string  `json:"release_date"`
	DisplayDate        string  `json:"display_date"`
	CreatedAt          string  `json:"created_at"`
	Sharing            string  `json:"sharing"`
	Downloadable       bool    `json:"downloadable"`
	Policy             string  `json:"policy"`
	TrackAuthorization string  `json:"track_authorization"`
	User               apiUser `json:"user"`
	PublisherMetadata  *struct {
		Artist     string `json:"artist"`
		AlbumTitle string `json:"album_title"`
		ISRC       string `json:"isrc"`
	} `json:"publisher_metadata"`
	Media struct {
		Transcodings []transcoding `json:"transcodings"`
	} `json:"media"`
}

type apiObject struct {
	apiTrack
	Kind    string     `json:"kind"`
	SetType string     `json:"set_type"`
	IsAlbum bool       `json:"is_album"`
	Tracks  []apiTrack `json:"tracks"`
}

type transcoding struct {
	URL    string `json:"url"`
	Preset string `json:"preset"`
	Format struct {
		Protocol string `json:"protocol"`
		MimeType string `json:"mime_type"`
	} `json:"format"`
}

// mapTrack turns an API track into a Track; ok=false for a track that is not public.
func mapTrack(a apiTrack) (Track, bool) {
	if a.Sharing != "" && a.Sharing != "public" {
		return Track{}, false
	}
	t := Track{
		ID: a.ID, URL: a.PermalinkURL, Title: a.Title, Genre: a.Genre,
		Duration: a.Duration / 1000, Downloadable: a.Downloadable, Policy: a.Policy,
		Artist: a.User.Username, Date: isoDate(firstNonEmpty(a.ReleaseDate, a.DisplayDate, a.CreatedAt)),
	}
	if pm := a.PublisherMetadata; pm != nil {
		t.Artist = firstNonEmpty(pm.Artist, a.User.Username)
		t.Album = pm.AlbumTitle
		t.ISRC = strings.ToUpper(strings.TrimSpace(pm.ISRC))
	}
	if t.Album != "" {
		t.AlbumArtist = t.Artist
	}
	t.Cover = bigCover(a.ArtworkURL)
	return t, t.URL != "" && t.Title != ""
}

// bigCover asks for the 500 px artwork instead of the 100 px "-large" one.
func bigCover(u string) string {
	if strings.Contains(u, "-large.") {
		return strings.Replace(u, "-large.", "-t500x500.", 1)
	}
	return u
}

func isoDate(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
