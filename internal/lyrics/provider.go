package lyrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UserAgent names mfetchy to the services it asks (LRCLIB asks clients to).
const UserAgent = "mfetchy (https://github.com/bybrooklyn/mfetchy)"

// Provider is one lyrics source. It is handed a Query (title, artist, album, length) and
// nothing else, and returns every record it found; Choose does the ranking. Not finding
// anything is an empty answer, never an error; errors are for outages worth retrying.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, q Query) ([]Candidate, error)
}

// ErrUnavailable marks a provider that could not be reached or answered with an error
// status; such an answer is never cached as "no lyrics".
var ErrUnavailable = errors.New("lyrics service unavailable")

func newClient() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

const maxBody = 4 << 20

// get fetches base+path with params, returning the body, or (nil, nil) for a 404. A 429 or
// 503 is retried once after the server's Retry-After (at most 5 s).
func get(ctx context.Context, c *http.Client, name, base, path string, params url.Values) ([]byte, error) {
	u := strings.TrimRight(base, "/") + path + "?" + params.Encode()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%s: %w", name, ErrUnavailable)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return nil, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
			if attempt == 0 {
				wait := time.Second
				if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
					wait = time.Duration(min(s, 5)) * time.Second
				}
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, fmt.Errorf("%s: HTTP %d: %w", name, resp.StatusCode, ErrUnavailable)
		case resp.StatusCode/100 != 2:
			return nil, fmt.Errorf("%s: HTTP %d: %w", name, resp.StatusCode, ErrUnavailable)
		case rerr != nil:
			return nil, fmt.Errorf("%s: %w", name, ErrUnavailable)
		}
		return body, nil
	}
}

// primaryArtist is the first artist of "A, B" / "A & B" / "A feat. B".
func primaryArtist(s string) string {
	a := splitArtists(s)
	if len(a) > 1 {
		return a[0]
	}
	return strings.TrimSpace(s)
}

var (
	bracketRe = regexp.MustCompile(`\s*[(\[{][^()\[\]{}]*[)\]}]`)
	featRe    = regexp.MustCompile(`(?i)\s+(feat\.?|ft\.?|featuring)\s.*$`)
)

// searchTitle drops upload decoration from a title before it is used as a search term:
// bracket groups made of noise words ("(Remastered 2011)", "(feat. X)"), a "feat." tail and
// a noise dash suffix. Version brackets such as "(Live)" stay: they name another recording.
func searchTitle(t string) string {
	t = bracketRe.ReplaceAllStringFunc(t, func(g string) string {
		for _, w := range words(g) {
			if noiseWords[w] {
				return ""
			}
		}
		return g
	})
	t = featRe.ReplaceAllString(t, "")
	if i := strings.LastIndex(t, " - "); i >= 0 {
		decor := true
		for _, w := range words(t[i+3:]) {
			if !noiseWords[w] && !allDigits(w) {
				decor = false
			}
		}
		if decor {
			t = t[:i]
		}
	}
	return strings.TrimSpace(t)
}
