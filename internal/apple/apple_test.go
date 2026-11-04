package apple

import (
	"testing"

	ampapi "github.com/bybrooklyn/ytm-to-mp3/internal/apple/amp-api"
)

func TestIsURL(t *testing.T) {
	cases := map[string]bool{
		"https://music.apple.com/us/album/currents/1440838039":                      true,
		"https://music.apple.com/us/album/the-less-i-know-the-better/1440838039?i=1440838041": true,
		"https://music.apple.com/gb/song/let-it-happen/1440838040":                  true,
		"https://music.apple.com/us/playlist/todays-hits/pl.f4d106fed2bd41149aaacabb233eb5eb": true,
		"https://music.apple.com/us/artist/tame-impala/511113657":                   false, // artist not supported yet
		"https://open.spotify.com/track/abc":                                        false,
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":                               false,
		"not a url":                                                                 false,
	}
	for link, want := range cases {
		if got := IsURL(link); got != want {
			t.Errorf("IsURL(%q) = %v, want %v", link, got, want)
		}
	}
}

func TestSongIDParam(t *testing.T) {
	got := songIDParam("https://music.apple.com/us/album/x/1440838039?i=1440838041")
	if got != "1440838041" {
		t.Errorf("songIDParam = %q, want 1440838041", got)
	}
	if got := songIDParam("https://music.apple.com/us/album/x/1440838039"); got != "" {
		t.Errorf("songIDParam without i = %q, want empty", got)
	}
}

func TestNorm(t *testing.T) {
	cases := map[string]string{
		"The Less I Know (The Better)":    "the less i know the better",
		"Song [Remastered 2011]":          "song remastered 2011",
		"Simon & Garfunkel":               "simon and garfunkel",
		"  Múltiple   Spaces  ":           "multiple spaces", // accents folded
	}
	for in, want := range cases {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScorePrefersExactMatch(t *testing.T) {
	mk := func(name, artist, album string, durMs int) ampapi.SongRespData {
		var d ampapi.SongRespData
		d.Attributes.Name = name
		d.Attributes.ArtistName = artist
		d.Attributes.AlbumName = album
		d.Attributes.DurationInMillis = durMs
		return d
	}
	wantTitle, wantArtist, wantAlbum := norm("Borderline"), norm("Tame Impala"), norm("Currents")
	exact := score(mk("Borderline", "Tame Impala", "Currents", 240000), wantTitle, wantArtist, wantAlbum, 240000)
	wrong := score(mk("Borderline", "Madonna", "Like a Virgin", 235000), wantTitle, wantArtist, wantAlbum, 240000)
	if exact <= wrong {
		t.Errorf("exact match (%.2f) should outscore wrong artist (%.2f)", exact, wrong)
	}
	if exact < 0.6 {
		t.Errorf("exact match score %.2f below acceptance threshold 0.6", exact)
	}
}
