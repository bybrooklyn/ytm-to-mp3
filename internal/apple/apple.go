// Package apple downloads Apple Music tracks as 256 kbps AAC using a
// signed-in account's media-user-token, with no external decryption backend.
//
// It wraps a vendored subset of github.com/zhaarey/apple-music-downloader:
// the Widevine web-playback path (internal/apple/widevine-rip) and the catalog
// API (internal/apple/amp-api). Apple's audio is DRM-encrypted, so a login
// authorises but does not itself decrypt; the vendored path uses a built-in
// Widevine L3 CDM to acquire the content key from Apple's own license server
// and decrypts locally. Lossless (ALAC) is FairPlay-only and is NOT available
// on this path — it needs the wrapper-lite backend, which mfetchy does not run.
package apple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	ampapi "github.com/bybrooklyn/mfetchy/internal/apple/amp-api"
	"github.com/bybrooklyn/mfetchy/internal/apple/download"
	widevinerip "github.com/bybrooklyn/mfetchy/internal/apple/widevine-rip"
)

// ErrNotAppleURL is returned by ResolveURL for a link that is not a
// recognised music.apple.com album, song or playlist.
var ErrNotAppleURL = errors.New("not an Apple Music URL")

// ErrUnavailable reports that Apple offers no downloadable AAC stream for a
// track (e.g. not in the account's storefront, or not streamable).
var ErrUnavailable = widevinerip.ErrUnavailable

// ErrLogin reports that Apple rejected the saved login: nothing else will work until
// the user signs in again.
var ErrLogin = errors.New("Apple rejected the login; sign in again at music.apple.com and sign in again in mfetchy's Settings (Apple Music)")

// Track is one Apple Music song: its catalog adamID plus the metadata needed
// to name and tag the output file.
type Track struct {
	ID             string `json:"id"` // Apple catalog adamID
	Title          string `json:"title"`
	Artist         string `json:"artist"`
	Album          string `json:"album"`
	AlbumArtist    string `json:"album_artist"`
	TrackNumber    int    `json:"track"`
	TrackCount     int    `json:"track_count,omitempty"` // tracks on the album, when known
	DiscNumber     int    `json:"disc"`
	DiscCount      int    `json:"disc_count,omitempty"` // discs on the album, when known
	DurationMillis int    `json:"duration_ms"`
	ISRC           string `json:"isrc,omitempty"`
	ReleaseDate    string `json:"date,omitempty"`
	Genre          string `json:"genre,omitempty"`
	ArtworkURL     string `json:"artwork,omitempty"` // template URL containing {w}x{h}
	Storefront     string `json:"storefront"`
	Composer       string `json:"composer,omitempty"`
	Explicit       bool   `json:"explicit,omitempty"`
	// From the album, when the song came from an album link.
	Label     string `json:"label,omitempty"`
	Copyright string `json:"copyright,omitempty"`
	UPC       string `json:"upc,omitempty"`
}

// URL is the song's page on music.apple.com (kept in the comment tag, like mfetchy keeps
// the YouTube link).
func (t Track) URL() string {
	sf := t.Storefront
	if sf == "" {
		sf = "us"
	}
	return "https://music.apple.com/" + sf + "/song/" + t.ID
}

// Client talks to Apple Music as a signed-in user. It is safe for concurrent use.
type Client struct {
	mediaUserToken string
	cookie         string // full apple cookie jar (Cookie header), optional
	storefront     string
	language       string

	mu     sync.Mutex
	bearer string // developer token, auto-fetched and cached
}

// New builds a client. mediaUserToken is the user's login (the
// media-user-token cookie from music.apple.com); cookie is the full Apple
// cookie jar (Cookie header) when available, which the web-playback endpoint
// needs beyond the token — pass "" if only the token is known. storefront
// defaults to "us". The developer bearer token is fetched lazily so SetProxy
// can run first.
func New(mediaUserToken, cookie, storefront, language string) (*Client, error) {
	mediaUserToken = strings.TrimSpace(mediaUserToken)
	if mediaUserToken == "" {
		return nil, errors.New("no Apple Music login: sign in in mfetchy's Settings (Apple Music)")
	}
	if storefront == "" {
		storefront = "us"
	}
	return &Client{
		mediaUserToken: mediaUserToken, cookie: strings.TrimSpace(cookie),
		storefront: strings.ToLower(storefront), language: language,
	}, nil
}

// Storefront is the account's country code ("us").
func (c *Client) Storefront() string { return c.storefront }

// SetProxy routes all Apple Music traffic through proxyURL (e.g. a Proton
// exit's "socks5://host:port") unless a request's context names another exit
// (WithExit). Empty means a direct connection.
func SetProxy(proxyURL string) error { return download.Init(proxyURL) }

// WithExit returns a context whose Apple requests go through proxyURL, so several
// downloads can run at once over different exits.
func WithExit(ctx context.Context, proxyURL string) context.Context {
	return download.WithProxy(ctx, proxyURL)
}

// SetProgress registers a callback that receives bytes as downloads proceed (with the
// download's context, so the host can tell songs apart, and its size when known), so
// the host can render progress through its own UI.
func SetProgress(fn func(ctx context.Context, n, total int64)) { widevinerip.Progress = fn }

// creds returns the Apple credentials, fetching and caching the developer
// bearer token on first use.
func (c *Client) creds(ctx context.Context) (widevinerip.Credentials, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bearer == "" {
		tok, err := ampapi.GetToken()
		if err != nil {
			return widevinerip.Credentials{}, fmt.Errorf("fetch Apple developer token: %w", err)
		}
		if tok == "" {
			return widevinerip.Credentials{}, errors.New("could not extract Apple developer token from music.apple.com")
		}
		c.bearer = tok
	}
	return widevinerip.Credentials{AuthToken: c.bearer, MediaUserToken: c.mediaUserToken, Cookie: c.cookie,
		StoreFront: storefrontHeader(c.storefront)}, nil
}

// token returns the cached developer bearer token, fetching it if needed.
func (c *Client) token(ctx context.Context) (string, error) {
	cr, err := c.creds(ctx)
	if err != nil {
		return "", err
	}
	return cr.AuthToken, nil
}

// Verify checks the login is a live session by calling an authenticated
// endpoint, and returns the account's storefront (country code) as Apple reports
// it. It returns ErrLogin when Apple rejects the credentials, so a stale token is
// caught before a download reports a confusing "Unavailable".
func (c *Client) Verify(ctx context.Context) (storefront string, err error) {
	cr, err := c.creds(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://amp-api.music.apple.com/v1/me/storefront", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cr.AuthToken)
	req.Header.Set("Music-User-Token", cr.MediaUserToken)
	req.Header.Set("media-user-token", cr.MediaUserToken)
	req.Header.Set("Origin", "https://music.apple.com")
	req.Header.Set("User-Agent", browserUA)
	if cr.Cookie != "" {
		req.Header.Set("Cookie", cr.Cookie)
	}
	resp, err := download.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("%w (%s)", ErrLogin, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("Apple: %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) == nil && len(body.Data) > 0 && validStorefront(body.Data[0].ID) {
		storefront = strings.ToLower(body.Data[0].ID)
		c.storefront = storefront
	}
	return storefront, nil
}

var storefrontRe = regexp.MustCompile(`^[A-Za-z]{2}$`)

func validStorefront(s string) bool { return storefrontRe.MatchString(s) }

// browserUA is the user agent the Apple web player sends.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

// Download fetches track t as 256 kbps AAC and writes it to destPath (.m4a). The exit
// is the one on ctx (WithExit), else the SetProxy default.
func (c *Client) Download(ctx context.Context, t Track, destPath string) error {
	cr, err := c.creds(ctx)
	if err != nil {
		return err
	}
	return widevinerip.DownloadSong(ctx, t.ID, destPath, cr, "")
}

// Permanent reports whether a download error will not go away by retrying (the song
// has no stream for this account, or the license was refused for it).
func Permanent(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "license rejected")
}

// RateLimited reports whether Apple refused a request for coming too fast (HTTP 429).
// The catalog search keeps refusing an address for a long while once it does, so
// retrying right away doesn't help.
func RateLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), "429")
}

// Transient reports whether retrying (ideally over another exit) may help: network
// trouble, timeouts, rate limits and server errors.
func Transient(err error) bool {
	if err == nil || Permanent(err) || errors.Is(err, ErrLogin) || errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	for _, m := range []string{"429", "500", "502", "503", "504", "EOF", "connection reset", "timeout",
		"no such host", "connection refused", "socks", "unexpected end", "parse response"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// --- URL handling ----------------------------------------------------------

var urlPatterns = map[string]*regexp.Regexp{
	"album":    regexp.MustCompile(`music\.apple\.com/(\w{2})/album/[^/]*/?(?:id)?(\d+)`),
	"song":     regexp.MustCompile(`music\.apple\.com/(\w{2})/song/[^/]*/?(?:id)?(\d+)`),
	"playlist": regexp.MustCompile(`music\.apple\.com/(\w{2})/playlist/[^/]*/?(?:id)?(pl\.[\w-]+)`),
}

// IsURL reports whether link looks like a music.apple.com album, song or
// playlist URL.
func IsURL(link string) bool {
	if !strings.Contains(link, "music.apple.com") {
		return false
	}
	for _, re := range urlPatterns {
		if re.MatchString(link) {
			return true
		}
	}
	return false
}

// ResolveURL expands an Apple Music album, song or playlist link into its
// tracks. An album link with "?i=<songID>" resolves to just that track.
func (c *Client) ResolveURL(ctx context.Context, link string) ([]Track, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	if m := urlPatterns["album"].FindStringSubmatch(link); m != nil {
		storefront, albumID := m[1], m[2]
		if i := songIDParam(link); i != "" {
			return c.resolveSong(ctx, storefront, i, token)
		}
		return c.resolveAlbum(ctx, storefront, albumID, token)
	}
	if m := urlPatterns["song"].FindStringSubmatch(link); m != nil {
		return c.resolveSong(ctx, m[1], m[2], token)
	}
	if m := urlPatterns["playlist"].FindStringSubmatch(link); m != nil {
		return c.resolvePlaylist(ctx, m[1], m[2], token)
	}
	return nil, ErrNotAppleURL
}

// ResolveWholeAlbum is ResolveURL, except that a song's link (music.apple.com/…/song/ID,
// or its place on an album, album/…?i=ID) resolves to every song on the album it is from.
// A song Apple lists on no album comes back on its own.
func (c *Client) ResolveWholeAlbum(ctx context.Context, link string) ([]Track, error) {
	storefront, songID := "", ""
	if m := urlPatterns["album"].FindStringSubmatch(link); m != nil {
		if i := songIDParam(link); i != "" {
			return c.resolveAlbumWithSong(ctx, m[1], m[2], i)
		}
	} else if m := urlPatterns["song"].FindStringSubmatch(link); m != nil {
		storefront, songID = m[1], m[2]
	}
	if songID == "" {
		return c.ResolveURL(ctx, link)
	}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := ampapi.GetSongResp(ctx, storefront, songID, c.language, token)
	if err != nil {
		return nil, apiErr(err)
	}
	if len(resp.Data) > 0 {
		if al := resp.Data[0].Relationships.Albums.Data; len(al) > 0 && al[0].ID != "" {
			return c.resolveAlbumWithSong(ctx, storefront, al[0].ID, songID)
		}
	}
	return c.resolveSong(ctx, storefront, songID, token)
}

// resolveAlbumWithSong is the album albumID, or just the song when the album can't be had.
func (c *Client) resolveAlbumWithSong(ctx context.Context, storefront, albumID, songID string) ([]Track, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	if ts, err := c.resolveAlbum(ctx, storefront, albumID, token); err == nil && len(ts) > 0 {
		return ts, nil
	} else if errors.Is(err, ErrLogin) {
		return nil, err
	}
	return c.resolveSong(ctx, storefront, songID, token)
}

func songIDParam(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("i")
}

// apiErr turns a catalog error into ErrLogin when Apple refused the tokens.
func apiErr(err error) error {
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
		return fmt.Errorf("%w (%v)", ErrLogin, err)
	}
	return err
}

func (c *Client) resolveSong(ctx context.Context, storefront, songID, token string) ([]Track, error) {
	resp, err := ampapi.GetSongResp(ctx, storefront, songID, c.language, token)
	if err != nil {
		return nil, apiErr(err)
	}
	if len(resp.Data) == 0 {
		return nil, ErrUnavailable
	}
	d := resp.Data[0]
	a := d.Attributes
	t := Track{
		ID: d.ID, Title: a.Name, Artist: a.ArtistName, Album: a.AlbumName,
		AlbumArtist: a.ArtistName, TrackNumber: a.TrackNumber, DiscNumber: a.DiscNumber,
		DurationMillis: a.DurationInMillis, ISRC: a.Isrc, ReleaseDate: a.ReleaseDate,
		Genre: firstGenre(a.GenreNames), ArtworkURL: a.Artwork.URL, Storefront: storefront,
		Composer: a.ComposerName, Explicit: a.ContentRating == "explicit",
	}
	return []Track{t}, nil
}

func (c *Client) resolveAlbum(ctx context.Context, storefront, albumID, token string) ([]Track, error) {
	resp, err := ampapi.GetAlbumResp(ctx, storefront, albumID, c.language, token)
	if err != nil {
		return nil, apiErr(err)
	}
	if len(resp.Data) == 0 {
		return nil, ErrUnavailable
	}
	albumArtist := resp.Data[0].Attributes.ArtistName
	albumName := resp.Data[0].Attributes.Name
	ts := tracksFrom(resp.Data[0].Relationships.Tracks.Data, storefront, albumArtist, albumName)
	// The track total per disc, for "NN/TT" in the track tag.
	perDisc := map[int]int{}
	discs := 0
	for _, t := range ts {
		perDisc[t.DiscNumber]++
		discs = max(discs, t.DiscNumber)
	}
	al := resp.Data[0].Attributes
	for i := range ts {
		ts[i].TrackCount = perDisc[ts[i].DiscNumber]
		ts[i].DiscCount = discs
		ts[i].Label, ts[i].Copyright, ts[i].UPC = al.RecordLabel, al.Copyright, al.Upc
	}
	return ts, nil
}

func (c *Client) resolvePlaylist(ctx context.Context, storefront, playlistID, token string) ([]Track, error) {
	resp, err := ampapi.GetPlaylistResp(ctx, storefront, playlistID, c.language, token)
	if err != nil {
		return nil, apiErr(err)
	}
	if len(resp.Data) == 0 {
		return nil, ErrUnavailable
	}
	// Playlist tracks carry their own album/artist in attributes.
	return tracksFrom(resp.Data[0].Relationships.Tracks.Data, storefront, "", ""), nil
}

func tracksFrom(data []ampapi.TrackRespData, storefront, albumArtist, albumName string) []Track {
	out := make([]Track, 0, len(data))
	for _, d := range data {
		if d.Type != "" && d.Type != "songs" {
			continue // music videos in a playlist have no AAC song stream
		}
		a := d.Attributes
		aa := albumArtist
		if aa == "" {
			aa = a.ArtistName
		}
		an := a.AlbumName
		if an == "" {
			an = albumName
		}
		out = append(out, Track{
			ID: d.ID, Title: a.Name, Artist: a.ArtistName, Album: an,
			AlbumArtist: aa, TrackNumber: a.TrackNumber, DiscNumber: a.DiscNumber,
			DurationMillis: a.DurationInMillis, ISRC: a.Isrc, ReleaseDate: a.ReleaseDate,
			Genre: firstGenre(a.GenreNames), ArtworkURL: a.Artwork.URL, Storefront: storefront,
			Composer: a.ComposerName, Explicit: a.ContentRating == "explicit",
		})
	}
	return out
}

// firstGenre is the most specific genre Apple lists, skipping the catch-all "Music".
func firstGenre(gs []string) string {
	for _, g := range gs {
		if g != "" && g != "Music" {
			return g
		}
	}
	return ""
}
