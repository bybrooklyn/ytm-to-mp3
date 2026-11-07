package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/bandcamp"
	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/ytmusic"
)

// browserNamed is the installed browser with this id (zero value when it is gone or none
// was remembered). Looking at which browsers are installed reads no cookies.
func browserNamed(browsers func() []browsercookies.Browser, id string) (browsercookies.Browser, bool) {
	if id == "" {
		return browsercookies.Browser{}, false
	}
	if browsers == nil {
		browsers = browsercookies.Detect
	}
	for _, b := range browsers() {
		if b.ID == id {
			return b, true
		}
	}
	return browsercookies.Browser{}, false
}

// --- YouTube Music ----------------------------------------------------------

// YouTube reads the YouTube Music library through the browser remembered in Connections.
type YouTube struct {
	// Config, Browsers and Fetch default to the real thing; tests replace them.
	Config   func() config.Config
	Browsers func() []browsercookies.Browser
	Fetch    func(ctx context.Context, b browsercookies.Browser, progress func(string)) (*ytmusic.Library, error)
}

// NewYouTube is the YouTube Music source.
func NewYouTube() *YouTube { return &YouTube{} }

func (*YouTube) Name() string             { return "YouTube Music" }
func (*YouTube) Supplies() catalog.Source { return catalog.YouTube }

func (y *YouTube) Detect() Status {
	cfg := config.Load
	if y.Config != nil {
		cfg = y.Config
	}
	if b, ok := browserNamed(y.Browsers, cfg().YouTubeBrowser); ok {
		return Status{SetUp: true, Detail: "reads " + b.Name}
	}
	return Status{Hint: "not set up (Connections > YouTube Music library)"}
}

func (y *YouTube) Read(ctx context.Context, progress func(string)) ([]Song, error) {
	cfg := config.Load
	if y.Config != nil {
		cfg = y.Config
	}
	b, ok := browserNamed(y.Browsers, cfg().YouTubeBrowser)
	if !ok {
		return nil, errors.New("no browser is set up for YouTube Music")
	}
	fetch := y.Fetch
	if fetch == nil {
		fetch = fetchYouTube
	}
	lib, err := fetch(ctx, b, progress)
	if err != nil {
		if errors.Is(err, browsercookies.ErrNotSignedIn) {
			return nil, fmt.Errorf("%s isn't signed in to YouTube: open music.youtube.com there and sign in", b.Name)
		}
		return nil, err
	}
	out := make([]Song, 0, len(lib.Tracks))
	for _, t := range lib.Tracks {
		out = append(out, Song{
			Track: catalog.Track{
				Title: t.Title, Artists: t.Artists, Album: t.Album, AlbumArtist: t.AlbumArtist,
				TrackNo: t.TrackNo, TrackTotal: t.TrackTotal, DiscNo: t.Disc, DiscTotal: t.DiscTotal,
				ISRC: t.ISRC, ArtworkURL: t.Cover, ReleaseDate: t.Date, Genre: t.Genre,
				Origin: catalog.YouTube, OriginID: t.ID,
			},
			From: y.Name(), Supply: catalog.YouTube,
		})
	}
	return out, nil
}

// fetchYouTube reads b's YouTube sign-in (kept in memory only) and the library behind it,
// from the user's own connection.
func fetchYouTube(ctx context.Context, b browsercookies.Browser, progress func(string)) (*ytmusic.Library, error) {
	cs, err := b.Import()
	if err != nil {
		return nil, err
	}
	c, err := ytmusic.New(&http.Client{Timeout: 45 * time.Second}, cs)
	if err != nil {
		return nil, err
	}
	return c.Library(ctx, ytmusic.Options{Albums: true, Playlists: true, Progress: progress})
}

// --- Bandcamp ---------------------------------------------------------------

// Bandcamp reads the purchases of the account the remembered browser is signed in to.
// A purchase of a track is a song; a purchase of an album is a Whole song (its tracks are
// not listed). Both come with the original files. Purchases that offer no download are left
// out: there is nothing better than a stream to get from them.
type Bandcamp struct {
	Config   func() config.Config
	Browsers func() []browsercookies.Browser
	// Fetch opens the account and lists its purchases.
	Fetch func(ctx context.Context, b browsercookies.Browser) (*bandcamp.Client, []bandcamp.Item, error)

	// Client is the signed-in client after a Read, for downloading the purchases.
	Client *bandcamp.Client
}

// NewBandcamp is the Bandcamp purchases source.
func NewBandcamp() *Bandcamp { return &Bandcamp{} }

func (*Bandcamp) Name() string             { return "Bandcamp" }
func (*Bandcamp) Supplies() catalog.Source { return catalog.BandcampPurchase }

func (s *Bandcamp) Detect() Status {
	cfg := config.Load
	if s.Config != nil {
		cfg = s.Config
	}
	if b, ok := browserNamed(s.Browsers, cfg().BandcampBrowser); ok {
		return Status{SetUp: true, Detail: "reads " + b.Name}
	}
	return Status{Hint: "not set up (Connections > Bandcamp)"}
}

func (s *Bandcamp) Read(ctx context.Context, progress func(string)) ([]Song, error) {
	cfg := config.Load
	if s.Config != nil {
		cfg = s.Config
	}
	b, ok := browserNamed(s.Browsers, cfg().BandcampBrowser)
	if !ok {
		return nil, errors.New("no browser is set up for Bandcamp")
	}
	fetch := s.Fetch
	if fetch == nil {
		fetch = fetchBandcamp
	}
	if progress != nil {
		progress("purchases")
	}
	c, items, err := fetch(ctx, b)
	if err != nil {
		return nil, err
	}
	s.Client = c
	var out []Song
	for _, it := range items {
		if it.DownloadPage == "" {
			continue
		}
		t := catalog.Track{
			Title: it.Title, Artists: []string{it.Artist}, Origin: catalog.BandcampPurchase, OriginID: bandcamp.Normalize(it.URL),
		}
		song := Song{Track: t, From: s.Name(), Supply: catalog.BandcampPurchase, Ref: it}
		if it.Type == "album" {
			song.Whole = true
		}
		out = append(out, song)
	}
	return out, nil
}

func fetchBandcamp(ctx context.Context, b browsercookies.Browser) (*bandcamp.Client, []bandcamp.Item, error) {
	cs, err := b.BandcampCookies()
	if err != nil {
		return nil, nil, fmt.Errorf("%s isn't signed in to Bandcamp: %w", b.Name, err)
	}
	c, err := bandcamp.New(ctx, &http.Client{Timeout: 5 * time.Minute}, cs)
	if err != nil {
		return nil, nil, err
	}
	items, err := c.Collection(ctx)
	return c, items, err
}

// --- Apple Music ------------------------------------------------------------

// Apple reads the songs in the Apple Music library of the account mfetchy is signed in to
// (Connections > Apple Music), with the media-user-token already stored there.
type Apple struct {
	Token func() string // the stored login ("" = not signed in)
	// Fetch reads the library; skipped counts songs with no catalog entry (uploads).
	Fetch func(ctx context.Context, progress func(string)) (songs []apple.Track, skipped int, err error)
}

// NewApple is the Apple Music library source.
func NewApple() *Apple { return &Apple{} }

func (*Apple) Name() string             { return "Apple Music" }
func (*Apple) Supplies() catalog.Source { return catalog.Apple }

func (a *Apple) token() string {
	if a.Token != nil {
		return a.Token()
	}
	return config.AppleToken()
}

func (a *Apple) Detect() Status {
	if a.token() != "" {
		return Status{SetUp: true, Detail: "signed in"}
	}
	return Status{Hint: "not set up (Connections > Apple Music)"}
}

func (a *Apple) Read(ctx context.Context, progress func(string)) ([]Song, error) {
	if a.token() == "" {
		return nil, errors.New("not signed in to Apple Music")
	}
	fetch := a.Fetch
	if fetch == nil {
		fetch = fetchApple
	}
	ts, skipped, err := fetch(ctx, progress)
	if err != nil {
		return nil, err
	}
	if skipped > 0 && progress != nil {
		progress(fmt.Sprintf("%d uploaded songs have no Apple Music version and are left out", skipped))
	}
	out := make([]Song, 0, len(ts))
	for _, t := range ts {
		var artists []string
		if t.Artist != "" {
			artists = []string{t.Artist}
		}
		out = append(out, Song{
			Track: catalog.Track{
				Title: t.Title, Artists: artists, Album: t.Album, AlbumArtist: t.AlbumArtist,
				TrackNo: t.TrackNumber, TrackTotal: t.TrackCount, DiscNo: t.DiscNumber, DiscTotal: t.DiscCount,
				DurationMillis: t.DurationMillis, ISRC: t.ISRC, ArtworkURL: t.ArtworkURL,
				ReleaseDate: t.ReleaseDate, Genre: t.Genre, Origin: catalog.Apple, OriginID: t.ID,
			},
			From: a.Name(), Supply: catalog.Apple, Ref: t,
		})
	}
	return out, nil
}

func fetchApple(ctx context.Context, progress func(string)) ([]apple.Track, int, error) {
	cfg := config.Load()
	c, err := apple.New(config.AppleToken(), config.AppleCookies(), cfg.AppleStorefront, cfg.AppleLanguage)
	if err != nil {
		return nil, 0, err
	}
	return c.LibrarySongs(ctx, func(n int) {
		if progress != nil {
			progress(fmt.Sprintf("%d songs", n))
		}
	})
}
