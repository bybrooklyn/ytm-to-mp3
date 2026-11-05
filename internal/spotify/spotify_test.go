package spotify

import (
	"context"
	"os"
	"testing"
)

func TestTracksFromList(t *testing.T) {
	list := []listItem{{URI: "spotify:track:a", Title: "One", Subtitle: "X"}, {URI: "spotify:track:b", Title: "Two", Subtitle: "X & Y"}}
	album := &entity{Type: "album", Name: "Record", TrackList: list}
	album.ReleaseDate.ISODate = "2015-07-17"
	got := tracksFromList(album)
	if got[1].TrackNo != 2 || got[1].TrackTotal != 2 || got[1].Album != "Record" || got[1].ReleaseDate != "2015-07-17" ||
		got[1].ID != "b" || len(got[1].Artists) != 2 {
		t.Errorf("album track = %+v", got[1])
	}
	// A playlist's order is not track numbers, and its songs come from many albums.
	for _, tr := range tracksFromList(&entity{Type: "playlist", Name: "Mix", TrackList: list}) {
		if tr.TrackNo != 0 || tr.Album != "" {
			t.Errorf("playlist track = %+v", tr)
		}
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		link, kind, id string
		ok             bool
	}{
		{"https://open.spotify.com/album/79dL7FLiJFOO0EoehUHQBv", "album", "79dL7FLiJFOO0EoehUHQBv", true},
		{"https://open.spotify.com/intl-de/album/79dL7FLiJFOO0EoehUHQBv?si=x", "album", "79dL7FLiJFOO0EoehUHQBv", true},
		{"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M", "playlist", "37i9dQZF1DXcBWIGoYBM5M", true},
		{"https://open.spotify.com/track/6K4t31amVTZDgR3sKmwUJJ?si=abc", "track", "6K4t31amVTZDgR3sKmwUJJ", true},
		{"spotify:track:6K4t31amVTZDgR3sKmwUJJ", "track", "6K4t31amVTZDgR3sKmwUJJ", true},
		{"https://music.apple.com/us/album/currents/79", "", "", false},
		{"https://open.spotify.com/artist/5INjqkS1o8h1imAzPqGZBb", "", "", false}, // artist not supported
		{"nope", "", "", false},
	}
	for _, c := range cases {
		kind, id, ok := Parse(c.link)
		if ok != c.ok || kind != c.kind || id != c.id {
			t.Errorf("Parse(%q) = (%q,%q,%v), want (%q,%q,%v)", c.link, kind, id, ok, c.kind, c.id, c.ok)
		}
	}
}

func TestSplitArtists(t *testing.T) {
	got := splitArtists("A, B & C")
	if len(got) != 3 || got[0] != "A" || got[2] != "C" {
		t.Errorf("splitArtists = %v", got)
	}
}

// TestLiveResolve hits real Spotify embed pages. Skipped unless YTM_SPOTIFY_LIVE=1.
func TestLiveResolve(t *testing.T) {
	if os.Getenv("YTM_SPOTIFY_LIVE") != "1" {
		t.Skip("set YTM_SPOTIFY_LIVE=1 to run the live Spotify test")
	}
	if err := SetProxy(os.Getenv("YTM_SPOTIFY_PROXY")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := map[string]int{ // link -> minimum tracks expected
		"https://open.spotify.com/track/6K4t31amVTZDgR3sKmwUJJ":    1,
		"https://open.spotify.com/album/79dL7FLiJFOO0EoehUHQBv":    13,
		"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M": 10,
	}
	for link, minN := range cases {
		ts, err := Resolve(ctx, link)
		if err != nil {
			t.Errorf("Resolve(%q): %v", link, err)
			continue
		}
		if len(ts) < minN {
			t.Errorf("Resolve(%q): %d tracks, want >= %d", link, len(ts), minN)
		}
		if len(ts) > 0 {
			t.Logf("%s -> %d tracks; first: %q by %v (%dms, id %s)", link, len(ts),
				ts[0].Title, ts[0].Artists, ts[0].DurationMillis, ts[0].ID)
		}
	}
}
