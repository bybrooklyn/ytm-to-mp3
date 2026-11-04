package engine

import (
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
)

func TestAlbumTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Album - Minecraft - Volume Alpha": "Minecraft - Volume Alpha",
		"EP - Thing":                       "Thing",
		"Single - Wet Hands":               "Wet Hands",
		"Plain":                            "Plain",
	} {
		if got := albumTitle(in); got != want {
			t.Errorf("albumTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAlbumURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://music.youtube.com/browse/MPREb_2P1MWwMNdRk":     true,
		"https://music.youtube.com/browse/UCONGElgDdV3FXSX-x7gB": false, // an artist page
		"http://music.youtube.com/browse/MPREb_2P1MWwMNdRk":      false,
		"https://evil.com/browse/MPREb_2P1MWwMNdRk":              false,
		"https://music.youtube.com/browse/MPREb_x/../../etc":     false,
		"--exec=rm": false,
	} {
		if got := isAlbumURL(u); got != ok {
			t.Errorf("isAlbumURL(%q) = %v", u, got)
		}
	}
}

func TestAlbumMatches(t *testing.T) {
	a := &Album{Title: "Minecraft - Volume Alpha", Tracks: []AlbumTrack{
		{"-CKR-McWeYk", "Key"}, {"oF5MfzRJ8dg", "Door"}, {"lXoVugTertI", "Subwoofer Lullaby"},
	}}
	cases := []struct {
		name  string
		want  string
		saved []model.Track
		ok    bool
	}{
		{"saved by id", "Minecraft - Volume Alpha", []model.Track{{ID: "oF5MfzRJ8dg", Title: "Door"}}, true},
		{"saved music video, same title", "Minecraft - Volume Alpha", []model.Track{{ID: "zzzzzzzzzzz", Title: "Subwoofer Lullaby"}}, true},
		{"edition suffix ignored", "Minecraft - Volume Alpha (Deluxe)", []model.Track{{ID: "oF5MfzRJ8dg"}}, true},
		{"different album", "Minecraft - Volume Beta", []model.Track{{ID: "oF5MfzRJ8dg"}}, false},
		{"none of my songs on it", "Minecraft - Volume Alpha", []model.Track{{ID: "zzzzzzzzzzz", Title: "Sweden"}}, false},
	}
	for _, c := range cases {
		if got := albumMatches(c.want, a, c.saved); got != c.ok {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
