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

func TestFullerAlbumPrefersDeluxe(t *testing.T) {
	std := &Album{Title: "CHROMAKOPIA", Tracks: []AlbumTrack{{"aaaaaaaaaaa", "St. Chroma"}, {"bbbbbbbbbbb", "Noid"}}}
	dlx := &Album{Title: "CHROMAKOPIA +", Tracks: []AlbumTrack{{"aaaaaaaaaaa", "St. Chroma"}, {"bbbbbbbbbbb", "Noid"}, {"ccccccccccc", "Hey Jane"}}}
	saved := []model.Track{{ID: "aaaaaaaaaaa", Title: "St. Chroma"}}
	for _, order := range [][2]*Album{{std, dlx}, {dlx, std}} {
		if got := fullerAlbum(fullerAlbum(nil, order[0], saved), order[1], saved); got != dlx {
			t.Errorf("want the deluxe edition from order %s,%s", order[0].Title, order[1].Title)
		}
	}
	// the edition holding more of my songs wins over a longer one that lacks them
	other := &Album{Title: "CHROMAKOPIA (Instrumentals)", Tracks: []AlbumTrack{{"ddddddddddd", "x"}, {"eeeeeeeeeee", "y"}, {"fffffffffff", "z"}, {"ggggggggggg", "w"}}}
	if got := fullerAlbum(std, other, saved); got != std {
		t.Error("an album with none of my songs must not win on length")
	}
}
