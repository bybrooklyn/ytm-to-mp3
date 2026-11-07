// Package ytmusic reads a person's YouTube Music library (liked songs, songs saved to the
// library, saved albums, playlists) live from their signed-in session, instead of from a
// Google Takeout export.
//
// It talks to the same internal "browse" endpoint the YouTube Music web player uses, with
// the browser's cookies and a SAPISIDHASH Authorization header. It only ever reads lists
// (a handful of requests plus one per saved album or playlist) and is meant to be called
// from the user's own address, not through the VPN: an account-bound request from a changing
// address looks like a stolen session. Cookies stay in memory; nothing is saved.
//
// The response format is not a published API. Parsing is therefore deliberately tolerant:
// it looks for the few renderer objects that matter wherever they are, and recognises
// artists and albums by the kind of id they link to, not by their position.
package ytmusic

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/model"
)

const (
	origin   = "https://music.youtube.com"
	endpoint = origin + "/youtubei/v1/browse?prettyPrint=false"

	searchEndpoint = origin + "/youtubei/v1/search?prettyPrint=false"
	agent          = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
)

// ErrSignedOut means YouTube answered as if nobody were signed in.
var ErrSignedOut = errors.New("YouTube Music didn't recognise that browser's sign-in (it may have expired: open music.youtube.com there and try again)")

// Client reads one account's library.
type Client struct {
	hc      *http.Client
	cookie  string
	sapisid string
	now     func() time.Time
	url     string // browse endpoint; tests point it elsewhere
	search  string // search endpoint
}

// New makes a client from a browser's YouTube cookies.
func New(hc *http.Client, cookies []browsercookies.Cookie) (*Client, error) {
	var sapisid string
	for _, c := range cookies {
		if c.Name == "SAPISID" {
			sapisid = c.Value
			break
		}
	}
	if sapisid == "" {
		for _, c := range cookies {
			if c.Name == "__Secure-3PAPISID" || c.Name == "__Secure-1PAPISID" {
				sapisid = c.Value
				break
			}
		}
	}
	if sapisid == "" {
		return nil, browsercookies.ErrNotSignedIn
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{hc: hc, cookie: browsercookies.CookieHeader(cookies), sapisid: sapisid, now: time.Now, url: endpoint, search: searchEndpoint}, nil
}

// NewAnonymous makes a client with no account: enough for search and for album and
// playlist pages, which anyone can open. hc decides the route (a VPN exit, say).
func NewAnonymous(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{hc: hc, now: time.Now, url: endpoint, search: searchEndpoint}
}

// authorization is the SAPISIDHASH header YouTube's web clients send.
func (c *Client) authorization() string {
	ts := c.now().Unix()
	sum := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", ts, c.sapisid, origin)))
	return fmt.Sprintf("SAPISIDHASH %d_%s", ts, hex.EncodeToString(sum[:]))
}

func (c *Client) context() map[string]any {
	return map[string]any{"client": map[string]any{
		"clientName":    "WEB_REMIX",
		"clientVersion": "1." + c.now().UTC().Format("20060102") + ".01.00",
		"hl":            "en",
	}}
}

// post sends one browse request and decodes the answer.
func (c *Client) post(ctx context.Context, endpointURL string, body map[string]any) (any, error) {
	body["context"] = c.context()
	b, _ := json.Marshal(body)
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 1500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agent)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Origin", origin)
		req.Header.Set("X-Goog-AuthUser", "0")
		if c.sapisid != "" {
			req.Header.Set("Cookie", c.cookie)
			req.Header.Set("Authorization", c.authorization())
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			last = err
			continue
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, ErrSignedOut
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("YouTube Music: HTTP %d", resp.StatusCode)
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("YouTube Music: HTTP %d", resp.StatusCode)
		case rerr != nil:
			last = rerr
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("YouTube Music sent something unreadable: %w", err)
		}
		return v, nil
	}
	return nil, last
}

// maxPages bounds how many continuation pages one list is followed for (a library of 5,000
// songs is about 100 pages): a feed that never ends can't hold a read up forever.
const maxPages = 400

// page fetches a browse page and every continuation of it, calling take on each response.
func (c *Client) page(ctx context.Context, browseID string, take func(resp any)) error {
	v, err := c.post(ctx, c.url, map[string]any{"browseId": browseID})
	if err != nil {
		return err
	}
	take(v)
	seen := map[string]bool{}
	for pages := 1; ; pages++ {
		tok := continuation(v)
		if tok == "" || seen[tok] || pages >= maxPages || ctx.Err() != nil {
			return ctx.Err()
		}
		seen[tok] = true
		u := c.url + "&ctoken=" + url.QueryEscape(tok) + "&continuation=" + url.QueryEscape(tok) + "&type=next"
		if v, err = c.post(ctx, u, map[string]any{}); err != nil {
			return err
		}
		take(v)
	}
}

// Library is everything found in an account, songs de-duplicated by video id.
type Library struct {
	Tracks    []model.Track
	Liked     int // songs on the Liked music list
	Saved     int // songs saved to the library
	Albums    int // saved albums read
	Playlists int // playlists read
	Skipped   int // albums or playlists that couldn't be read
}

// Options says which parts of the library to read.
type Options struct {
	Albums, Playlists bool
	// Progress, when set, is told what is being read ("Liked music", "album 3 of 41").
	Progress func(string)
	// Concurrency of per-album and per-playlist reads (default 3).
	Workers int
}

// Library reads the signed-in account's library.
func (c *Client) Library(ctx context.Context, o Options) (*Library, error) {
	say := o.Progress
	if say == nil {
		say = func(string) {}
	}
	lib := &Library{}
	seen := map[string]bool{}
	add := func(ts []model.Track) int {
		n := 0
		for _, t := range ts {
			if t.ID == "" || seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			lib.Tracks = append(lib.Tracks, t)
			n++
		}
		return n
	}

	say("Liked music")
	if err := c.page(ctx, "VLLM", func(r any) { lib.Liked += add(songs(r, "musicPlaylistShelfRenderer", "musicShelfRenderer")) }); err != nil {
		return nil, err
	}
	say("Songs saved to your library")
	if err := c.page(ctx, "FEmusic_liked_videos", func(r any) { lib.Saved += add(songs(r, "musicShelfRenderer", "musicPlaylistShelfRenderer")) }); err != nil {
		return nil, err
	}

	var albums, lists []card
	if o.Albums {
		say("Saved albums")
		if err := c.page(ctx, "FEmusic_liked_albums", func(r any) { albums = append(albums, cards(r, "MPREb")...) }); err != nil {
			return nil, err
		}
	}
	if o.Playlists {
		say("Playlists")
		if err := c.page(ctx, "FEmusic_liked_playlists", func(r any) { lists = append(lists, cards(r, "VL")...) }); err != nil {
			return nil, err
		}
	}

	type job struct {
		c     card
		album bool
	}
	var albumJobs, listJobs []job
	for _, a := range albums {
		albumJobs = append(albumJobs, job{a, true})
	}
	for _, p := range lists {
		if p.ID == "VLLM" || strings.HasPrefix(p.ID, "VLSE") { // liked music (read above); episodes
			continue
		}
		// Auto-generated mixes and radios (VLRD…) are recommendation feeds, not part of the
		// library, and some run to thousands of songs that keep changing. Playlists YouTube
		// Music's editors made and the user saved (VLRDCLAK…) are kept.
		if strings.HasPrefix(p.ID, "VLRD") && !strings.HasPrefix(p.ID, "VLRDCLAK") {
			continue
		}
		listJobs = append(listJobs, job{p, false})
	}
	total := len(albumJobs) + len(listJobs)
	workers := o.Workers
	if workers <= 0 {
		workers = 3
	}
	var (
		mu   sync.Mutex
		done int
	)
	// Albums first, then playlists: a song on both keeps the album's details (album,
	// track number), whichever request would have finished first.
	run := func(jobs []job) {
		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for _, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func(j job) {
				defer wg.Done()
				defer func() { <-sem }()
				var got []model.Track
				var head header
				shelf := "musicPlaylistShelfRenderer"
				if j.album {
					shelf = "musicShelfRenderer"
				}
				err := c.page(ctx, j.c.ID, func(r any) {
					if head.title == "" {
						head = pageHeader(r)
					}
					got = append(got, songs(r, shelf)...)
				})
				mu.Lock()
				defer mu.Unlock()
				done++
				if err != nil {
					lib.Skipped++
					return
				}
				if j.album {
					for i := range got {
						got[i].Album = firstNonEmpty(head.title, j.c.Title)
						got[i].TrackNo, got[i].TrackTotal = i+1, len(got)
						if got[i].AlbumArtist == "" {
							got[i].AlbumArtist = head.artist
						}
						if len(got[i].Artists) == 0 && head.artist != "" {
							got[i].Artists = []string{head.artist}
						}
					}
					lib.Albums++
				} else {
					lib.Playlists++
				}
				add(got)
				say(fmt.Sprintf("%d of %d albums and playlists", done, total))
			}(j)
		}
		wg.Wait()
	}
	run(albumJobs)
	run(listJobs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(lib.Tracks) == 0 {
		return lib, ErrSignedOut
	}
	return lib, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
