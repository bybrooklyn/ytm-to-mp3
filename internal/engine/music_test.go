package engine

import (
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/model"
)

func TestTitleMatches(t *testing.T) {
	for _, c := range []struct {
		want, got string
		ok        bool
	}{
		{"Wish You Were Here", "Wish You Were Here", true},
		{"Time (2023 Remaster)", "Time", true},
		{"Us And Them (2023 Remaster)", "Us and Them - 2011 Remaster", true},
		{"Until the Plug Comes Back Around", "Until The Plug Comes Back Around", true},
		{"Wish You Were Here", "The Great Gig in the Sky", false},
		{"Verbatim", "Oh Ana (Mother Version)", false},
		{"=ටᆼට=", "=ටᆼට=", true},
		{"", "Anything", false},
	} {
		if got := titleMatches(c.want, c.got); got != c.ok {
			t.Errorf("titleMatches(%q, %q) = %v, want %v", c.want, c.got, got, c.ok)
		}
	}
}

func TestOrderHitsPrefersExactVersion(t *testing.T) {
	hits := []musicHit{
		{"aaaaaaaaaaa", "Time"},
		{"bbbbbbbbbbb", "Money"},
		{"ccccccccccc", "Time (2023 Remaster)"},
		{"ddddddddddd", "Time (2023 Remaster)"}, // the dead id itself
	}
	got := orderHits("Time (2023 Remaster)", "ddddddddddd", hits)
	if len(got) != 2 || got[0].ID != "ccccccccccc" || got[1].ID != "aaaaaaaaaaa" {
		t.Fatalf("want exact remaster first, then the plain version: %v", got)
	}
}

func TestArtistMatches(t *testing.T) {
	if !artistMatches([]string{"Tyler, The Creator"}, []string{"Tyler The Creator"}) {
		t.Error("punctuation should not matter")
	}
	if !artistMatches([]string{"Pink Floyd"}, []string{"David Gilmour", "Pink Floyd"}) {
		t.Error("any listed artist should do")
	}
	if artistMatches([]string{"Pink Floyd"}, []string{"Some Cover Band"}) {
		t.Error("a different artist must not match")
	}
	if artistMatches([]string{"Pink Floyd"}, nil) {
		t.Error("a result with no artist must not match a track that has one")
	}
}

// Realistic reasons a re-uploaded song was not found: the same artist or title spelled
// differently by the export and by YouTube Music.
func TestArtistMatchesRealCredits(t *testing.T) {
	for _, c := range []struct {
		name      string
		want, got []string
		ok        bool
	}{
		{"diacritics", []string{"Sigur Ros"}, []string{"Sigur Rós"}, true},
		{"diacritics other way", []string{"Beyoncé"}, []string{"BEYONCE"}, true},
		{"ampersand", []string{"Simon & Garfunkel"}, []string{"Simon and Garfunkel"}, true},
		{"topic channel", []string{"Radiohead"}, []string{"Radiohead - Topic"}, true},
		{"vevo channel", []string{"Adele"}, []string{"AdeleVEVO"}, true},
		{"feat in one string", []string{"Drake"}, []string{"Drake feat. Rihanna"}, true},
		{"joined credit wanted", []string{"Kendrick Lamar & SZA"}, []string{"SZA"}, true},
		{"The prefix", []string{"The Weeknd"}, []string{"Weeknd"}, true},
		{"stylised", []string{"P!nk"}, []string{"Pink"}, true},
		{"stylised dollar", []string{"Ke$ha"}, []string{"Kesha"}, true},
		{"umlauts", []string{"Mötley Crüe"}, []string{"Motley Crue"}, true},
		{"plus collab", []string{"Queen + Adam Lambert"}, []string{"Queen"}, true},
		{"japanese stays itself", []string{"ずっと真夜中でいいのに。"}, []string{"ずっと真夜中でいいのに。"}, true},
		// A different artist with a similar name must not be accepted.
		{"one word is not enough", []string{"Pink"}, []string{"Pink Floyd"}, false},
		{"substring of another act", []string{"Eve"}, []string{"Steve Miller Band"}, false},
		{"cover band", []string{"Queen"}, []string{"Queens of the Stone Age"}, false},
		{"different kana", []string{"か"}, []string{"が"}, false},
	} {
		if got := artistMatches(c.want, c.got); got != c.ok {
			t.Errorf("%s: artistMatches(%q, %q) = %v, want %v", c.name, c.want, c.got, got, c.ok)
		}
	}
}

func TestTitleMatchesRealTitles(t *testing.T) {
	for _, c := range []struct {
		want, got string
		ok        bool
	}{
		{"Déjà Vu", "Deja Vu", true},
		{"Rock & Roll", "Rock and Roll", true},
		{"Get Lucky ft. Pharrell Williams", "Get Lucky", true},
		{"Get Lucky (feat. Pharrell Williams)", "Get Lucky", true},
		{"Señorita", "Senorita", true},
		{"Bohemian Rhapsody", "Bohemian Rhapsody - Remastered 2011", true},
		{"Song", "Song 2", false},
		{"Rock & Roll", "Rock Roll Part 2", false},
	} {
		if got := titleMatches(c.want, c.got); got != c.ok {
			t.Errorf("titleMatches(%q, %q) = %v, want %v", c.want, c.got, got, c.ok)
		}
	}
}

func TestMusicQueryDropsChannelNoise(t *testing.T) {
	got := musicQuery(model.Track{Title: "Creep", Artists: []string{"Radiohead - Topic"}})
	if got != "Radiohead Creep" {
		t.Errorf("query = %q", got)
	}
	if got := musicQuery(model.Track{Title: "Hello", Artists: []string{"AdeleVEVO"}}); got != "Adele Hello" {
		t.Errorf("query = %q", got)
	}
}

func TestParseProgress(t *testing.T) {
	got, total, ok := parseProgress("ytmprog 1048576 4194304")
	if !ok || got != 1048576 || total != 4194304 {
		t.Fatalf("got %d/%d ok=%v", got, total, ok)
	}
	if got, total, ok = parseProgress("ytmprog 512 NA"); !ok || got != 512 || total != 0 {
		t.Fatalf("unknown total: got %d/%d ok=%v", got, total, ok)
	}
	if got, total, ok = parseProgress("ytmprog 2048.0 4096.5"); !ok || got != 2048 || total != 4096 {
		t.Fatalf("estimates come as floats: got %d/%d ok=%v", got, total, ok)
	}
	if _, _, ok = parseProgress("/tmp/dl-abc.webm"); ok {
		t.Fatal("a file path is not a progress line")
	}
}
