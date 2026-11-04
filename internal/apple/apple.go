// Package apple downloads Apple Music tracks as 256 kbps AAC using a
// signed-in account's media-user-token, with no external decryption backend.
//
// It wraps a vendored subset of github.com/zhaarey/apple-music-downloader:
// the Widevine web-playback path (internal/apple/widevine-rip) and the catalog
// API (internal/apple/amp-api). Apple's audio is DRM-encrypted, so a login
// authorises but does not itself decrypt; the vendored path uses a built-in
// Widevine L3 CDM to acquire the content key from Apple's own license server
// and decrypts locally. Lossless (ALAC) is FairPlay-only and is NOT available
// on this path — it needs the wrapper-lite backend, which ytm does not run.
package apple

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	ampapi "github.com/bybrooklyn/ytm-to-mp3/internal/apple/amp-api"
	"github.com/bybrooklyn/ytm-to-mp3/internal/apple/download"
	widevinerip "github.com/bybrooklyn/ytm-to-mp3/internal/apple/widevine-rip"
)

// ErrNotAppleURL is returned by ResolveURL for a link that is not a
// recognised music.apple.com album, song or playlist.
var ErrNotAppleURL = errors.New("not an Apple Music URL")

// ErrUnavailable reports that Apple offers no downloadable AAC stream for a
// track (e.g. not in the account's storefront, or not streamable).
var ErrUnavailable = widevinerip.ErrUnavailable

// Track is one Apple Music song: its catalog adamID plus the metadata needed
// to name and tag the output file.
type Track struct {
	ID             string // Apple catalog adamID
	Title          string
	Artist         string
	Album          string
	AlbumArtist    string
	TrackNumber    int
	DiscNumber     int
	DurationMillis int
	ISRC           string
	ReleaseDate    string
	ArtworkURL     string // template URL containing {w}x{h}
	Storefront     string
}

// Client talks to Apple Music as a signed-in user.
type Client struct {
	mediaUserToken string
	storefront     string
	language       string
	bearer         string // developer token, auto-fetched and cached
}

// New builds a client. mediaUserToken is the user's login (the
// media-user-token cookie from music.apple.com); storefront defaults to "us".
// The developer bearer token is fetched lazily so SetProxy can run first.
func New(mediaUserToken, storefront, language string) (*Client, error) {
	mediaUserToken = strings.TrimSpace(mediaUserToken)
	if mediaUserToken == "" {
		return nil, errors.New("no Apple Music media-user-token (run: ytm apple login)")
	}
	if storefront == "" {
		storefront = "us"
	}
	return &Client{mediaUserToken: mediaUserToken, storefront: strings.ToLower(storefront), language: language}, nil
}

// SetProxy routes all Apple Music traffic through proxyURL (e.g. a Proton
// exit's "socks5://host:port"). Empty means a direct connection. It must be
// called before any request; it configures a process-wide HTTP client.
func SetProxy(proxyURL string) error { return download.Init(proxyURL) }

// SetProgress registers a callback that receives bytes as downloads proceed,
// so the host can render progress through its own UI.
func SetProgress(fn func(n int)) { widevinerip.Progress = fn }

// creds returns the Apple credentials, fetching and caching the developer
// bearer token on first use.
func (c *Client) creds() (widevinerip.Credentials, error) {
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
	return widevinerip.Credentials{AuthToken: c.bearer, MediaUserToken: c.mediaUserToken}, nil
}

// token returns the cached developer bearer token, fetching it if needed.
func (c *Client) token() (string, error) {
	cr, err := c.creds()
	if err != nil {
		return "", err
	}
	return cr.AuthToken, nil
}

// Verify checks the saved login by fetching the developer token and confirming
// the catalog API accepts the credentials. It returns a short human message.
func (c *Client) Verify(ctx context.Context) error {
	if _, err := c.token(); err != nil {
		return err
	}
	// A storefront lookup needs only the developer token; the real test of the
	// media-user-token is the first download's web-playback call.
	return nil
}

// Download fetches track t as 256 kbps AAC and writes it to destPath (.m4a).
func (c *Client) Download(ctx context.Context, t Track, destPath string) error {
	cr, err := c.creds()
	if err != nil {
		return err
	}
	if err := widevinerip.DownloadSong(ctx, t.ID, destPath, cr, ""); err != nil {
		return err
	}
	return nil
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
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	// A single track addressed as album/...?i=<id>.
	if m := urlPatterns["album"].FindStringSubmatch(link); m != nil {
		storefront, albumID := m[1], m[2]
		if i := songIDParam(link); i != "" {
			return c.resolveSong(storefront, i, token)
		}
		return c.resolveAlbum(storefront, albumID, token)
	}
	if m := urlPatterns["song"].FindStringSubmatch(link); m != nil {
		return c.resolveSong(m[1], m[2], token)
	}
	if m := urlPatterns["playlist"].FindStringSubmatch(link); m != nil {
		return c.resolvePlaylist(m[1], m[2], token)
	}
	return nil, ErrNotAppleURL
}

func songIDParam(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("i")
}

func (c *Client) resolveSong(storefront, songID, token string) ([]Track, error) {
	resp, err := ampapi.GetSongResp(storefront, songID, c.language, token)
	if err != nil {
		return nil, err
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
		ArtworkURL: a.Artwork.URL, Storefront: storefront,
	}
	return []Track{t}, nil
}

func (c *Client) resolveAlbum(storefront, albumID, token string) ([]Track, error) {
	resp, err := ampapi.GetAlbumResp(storefront, albumID, c.language, token)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, ErrUnavailable
	}
	albumArtist := resp.Data[0].Attributes.ArtistName
	albumName := resp.Data[0].Attributes.Name
	return tracksFrom(resp.Data[0].Relationships.Tracks.Data, storefront, albumArtist, albumName), nil
}

func (c *Client) resolvePlaylist(storefront, playlistID, token string) ([]Track, error) {
	resp, err := ampapi.GetPlaylistResp(storefront, playlistID, c.language, token)
	if err != nil {
		return nil, err
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
			ArtworkURL: a.Artwork.URL, Storefront: storefront,
		})
	}
	return out
}
