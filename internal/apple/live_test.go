package apple

import (
	"context"
	"testing"

	ampapi "github.com/bybrooklyn/mfetchy/internal/apple/amp-api"
	"github.com/bybrooklyn/mfetchy/internal/appname"
)

// TestLiveCatalog hits real Apple Music servers to validate the account-free
// half of the pipeline (developer-token fetch, URL resolution, library match).
// It is skipped unless YTM_APPLE_LIVE=1. Set YTM_APPLE_PROXY to a socks5 URL to
// route through a VPN exit.
func TestLiveCatalog(t *testing.T) {
	if appname.Getenv("APPLE_LIVE") != "1" {
		t.Skip("set YTM_APPLE_LIVE=1 to run the live Apple catalog test")
	}
	if err := SetProxy(appname.Getenv("APPLE_PROXY")); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	ctx := context.Background()

	tok, err := ampapi.GetToken()
	if err != nil || tok == "" {
		t.Fatalf("GetToken: %q err=%v", tok, err)
	}
	t.Logf("developer token: %d chars", len(tok))

	// media-user-token is not needed for catalog/resolve/match, only download.
	c, err := New("dummy-not-used-for-catalog", "", "us", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A well-known album: Tame Impala - Currents.
	const albumURL = "https://music.apple.com/us/album/currents/1440838039"
	tracks, err := c.ResolveURL(ctx, albumURL)
	if err != nil {
		t.Fatalf("ResolveURL: %v", err)
	}
	if len(tracks) == 0 {
		t.Fatal("ResolveURL returned no tracks")
	}
	t.Logf("album resolved: %d tracks, first = %q by %q (id %s)", len(tracks), tracks[0].Title, tracks[0].Artist, tracks[0].ID)
	for _, tr := range tracks {
		if tr.ID == "" || tr.Title == "" {
			t.Errorf("track missing id/title: %+v", tr)
		}
	}

	// Library-style match.
	m, ok, err := c.Match(ctx, Query{Title: "The Less I Know The Better", Artists: []string{"Tame Impala"}, Album: "Currents"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !ok {
		t.Fatal("Match found nothing for a known song")
	}
	t.Logf("match: %q by %q (id %s, isrc %s, score %.2f, sure %v)", m.Track.Title, m.Track.Artist, m.Track.ID, m.Track.ISRC, m.Score, m.Sure)
}
