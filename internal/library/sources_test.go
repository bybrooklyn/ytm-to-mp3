package library

import (
	"context"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/bandcamp"
	"github.com/bybrooklyn/ytm-to-mp3/internal/browsercookies"
	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ytmusic"
)

var ffBrowser = func() []browsercookies.Browser { return []browsercookies.Browser{{ID: "firefox", Name: "Firefox"}} }

func TestYouTubeReadMapsTracks(t *testing.T) {
	y := &YouTube{
		Config:   func() config.Config { return config.Config{YouTubeBrowser: "firefox"} },
		Browsers: ffBrowser,
		Fetch: func(context.Context, browsercookies.Browser, func(string)) (*ytmusic.Library, error) {
			return &ytmusic.Library{Tracks: []model.Track{{ID: "aaaaaaaaaaa", Title: "T", Artists: []string{"A"}, Album: "Al"}}}, nil
		},
	}
	got, err := y.Read(context.Background(), nil)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if got[0].Origin != catalog.YouTube || got[0].OriginID != "aaaaaaaaaaa" || got[0].Supply != catalog.YouTube || got[0].From != "YouTube Music" {
		t.Errorf("%+v", got[0])
	}
}

func TestBandcampReadAlbumsTracksAndNoDownload(t *testing.T) {
	b := &Bandcamp{
		Config:   func() config.Config { return config.Config{BandcampBrowser: "firefox"} },
		Browsers: ffBrowser,
		Fetch: func(context.Context, browsercookies.Browser) (*bandcamp.Client, []bandcamp.Item, error) {
			return &bandcamp.Client{}, []bandcamp.Item{
				{Title: "Album", Artist: "A", Type: "album", URL: "https://a.bandcamp.com/album/x", DownloadPage: "d"},
				{Title: "Single", Artist: "A", Type: "track", URL: "https://a.bandcamp.com/track/y", DownloadPage: "d"},
				{Title: "Nothing", Artist: "A", Type: "album", URL: "https://a.bandcamp.com/album/z"},
			}, nil
		},
	}
	got, err := b.Read(context.Background(), nil)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if !got[0].Whole || got[1].Whole || got[0].Supply != catalog.BandcampPurchase {
		t.Errorf("%+v", got)
	}
	if _, ok := got[0].Ref.(bandcamp.Item); !ok || b.Client == nil {
		t.Error("the purchase and the client must be kept for the downloader")
	}
}

func TestAppleReadMapsSongs(t *testing.T) {
	a := &Apple{
		Token: func() string { return "tok" },
		Fetch: func(context.Context, func(string)) ([]apple.Track, int, error) {
			return []apple.Track{{ID: "9", Title: "T", Artist: "A", Album: "Al", ISRC: "US1", TrackNumber: 2}}, 1, nil
		},
	}
	var said []string
	got, err := a.Read(context.Background(), func(s string) { said = append(said, s) })
	if err != nil || len(got) != 1 || got[0].ISRC != "US1" || got[0].Supply != catalog.Apple || got[0].OriginID != "9" {
		t.Fatal(got, err)
	}
	if len(said) != 1 {
		t.Errorf("skipped uploads not reported: %v", said)
	}
	if _, err := (&Apple{Token: func() string { return "" }}).Read(context.Background(), nil); err == nil {
		t.Error("reading without a login must fail before any request")
	}
}
