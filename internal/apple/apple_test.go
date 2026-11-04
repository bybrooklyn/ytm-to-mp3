package apple

import (
	"testing"

	ampapi "github.com/bybrooklyn/ytm-to-mp3/internal/apple/amp-api"
)

func TestIsURL(t *testing.T) {
	cases := map[string]bool{
		"https://music.apple.com/us/album/currents/1440838039":                                true,
		"https://music.apple.com/us/album/the-less-i-know-the-better/1440838039?i=1440838041": true,
		"https://music.apple.com/gb/song/let-it-happen/1440838040":                            true,
		"https://music.apple.com/us/playlist/todays-hits/pl.f4d106fed2bd41149aaacabb233eb5eb": true,
		"https://music.apple.com/us/artist/tame-impala/511113657":                             false, // artist not supported yet
		"https://open.spotify.com/track/abc":                                                  false,
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":                                         false,
		"not a url": false,
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
		"The Less I Know (The Better)": "the less i know the better",
		"Song [Remastered 2011]":       "song remastered 2011",
		"Simon & Garfunkel":            "simon and garfunkel",
		"  Múltiple   Spaces  ":        "multiple spaces", // accents folded
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
	q := Query{Title: "Borderline", Artists: []string{"Tame Impala"}, Album: "Currents", DurationMillis: 240000}
	exact, sure := score(mk("Borderline", "Tame Impala", "Currents", 240000), q)
	wrong, _ := score(mk("Borderline", "Madonna", "Like a Virgin", 235000), q)
	if exact <= wrong {
		t.Errorf("exact match (%.2f) should outscore wrong artist (%.2f)", exact, wrong)
	}
	if !sure || exact < sureScore {
		t.Errorf("exact match score %.2f (sure %v), want sure", exact, sure)
	}
	if wrong >= minScore {
		t.Errorf("wrong artist %.2f would be accepted (min %.2f)", wrong, minScore)
	}
}

func TestScoreVersions(t *testing.T) {
	mk := func(name, artist string) ampapi.SongRespData {
		var d ampapi.SongRespData
		d.Attributes.Name, d.Attributes.ArtistName = name, artist
		return d
	}
	cases := []struct {
		want, wantArtist, have, haveArtist string
		accept, sure                       bool
	}{
		{"Let It Happen", "Tame Impala", "Let It Happen", "Tame Impala", true, true},
		{"Let It Happen", "Tame Impala", "Let It Happen (Live)", "Tame Impala", false, false},
		{"Let It Happen", "Tame Impala", "Let It Happen (Soulwax Remix)", "Tame Impala", false, false},
		{"Something (2019 Mix)", "The Beatles", "Something (2019 Mix)", "The Beatles", true, true},
		{"Bad Habits (feat. Bring Me The Horizon)", "Ed Sheeran", "Bad Habits (feat. Bring Me the Horizon)", "Ed Sheeran", true, true},
		{"Under Pressure", "Queen", "Under Pressure", "Queen & David Bowie", true, true},
		{"Like Him", "Tyler, The Creator", "Like Him (feat. Lola Young)", "Tyler, The Creator", true, true},
		{"Under Pressure", "Queen", "Under Pressure", "Queen Latifah", true, false},
		{"Under Pressure - Remastered 2011", "Queen", "Under Pressure", "Queen & David Bowie", true, true},
		{"Song", "Artist", "Song Part 2", "Artist", false, false},
		{"Beyoncé", "Someone", "Beyonce", "Someone", true, true},
		{"Lemon", "米津玄師", "Lemon", "米津玄師", true, true},
		{"Lemon", "米津玄師", "Lemon", "Kenshi Yonezu", false, false},
	}
	for _, c := range cases {
		s, sure := score(mk(c.have, c.haveArtist), Query{Title: c.want, Artists: []string{c.wantArtist}})
		if (s >= minScore) != c.accept || sure != c.sure {
			t.Errorf("%q by %q vs %q by %q: score %.2f sure %v, want accept %v sure %v",
				c.want, c.wantArtist, c.have, c.haveArtist, s, sure, c.accept, c.sure)
		}
	}
}

func TestCoreTitle(t *testing.T) {
	cases := map[string]string{
		"Song (feat. X) [Live]":            "Song",
		"Under Pressure - Remastered 2011": "Under Pressure",
		"Song feat. Someone":               "Song",
		"(Intro)":                          "(Intro)",
		"Hello - World":                    "Hello - World", // a dash that is part of the name
	}
	for in, want := range cases {
		if got := coreTitle(in); got != want {
			t.Errorf("coreTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStorefrontHeader(t *testing.T) {
	if got := storefrontHeader("gb"); got != "143444-1,25" {
		t.Errorf("gb = %q", got)
	}
	if got := storefrontHeader("zz"); got != "" {
		t.Errorf("unknown = %q, want empty (US default)", got)
	}
}
