package catalog

import "testing"

func TestKey(t *testing.T) {
	// ISRC wins when present and is case-normalized.
	a := Track{Title: "Song", Artists: []string{"Artist"}, ISRC: "usum71500303"}
	if got := a.Key(); got != "isrc:USUM71500303" {
		t.Errorf("ISRC key = %q", got)
	}
	// Without ISRC, same song by name/artist/album folds to one key.
	b := Track{Title: "The Less I Know (The Better)", Artists: []string{"Tame Impala"}, Album: "Currents"}
	c := Track{Title: "the less i know the better", Artists: []string{"Tame Impala"}, Album: "CURRENTS"}
	if b.Key() != c.Key() {
		t.Errorf("name keys differ:\n %q\n %q", b.Key(), c.Key())
	}
	// Different songs get different keys.
	d := Track{Title: "Borderline", Artists: []string{"Tame Impala"}, Album: "Currents"}
	if b.Key() == d.Key() {
		t.Error("different titles share a key")
	}
}

func TestRank(t *testing.T) {
	if !(Rank(Apple) > Rank(YouTube) && Rank(YouTube) > Rank(Spotify)) {
		t.Errorf("ranks out of order: apple=%d youtube=%d spotify=%d", Rank(Apple), Rank(YouTube), Rank(Spotify))
	}
}

func TestRankOrderWithTidal(t *testing.T) {
	order := []Source{Spotify, SoundCloud, YouTube, Apple, Tidal, BandcampPurchase}
	for i := 1; i < len(order); i++ {
		if SoundCloud == order[i] && Rank(Bandcamp) != Rank(SoundCloud) {
			t.Error("stream sources should tie")
		}
		if Rank(order[i]) <= Rank(order[i-1]) {
			t.Errorf("%s (%d) should outrank %s (%d)", order[i], Rank(order[i]), order[i-1], Rank(order[i-1]))
		}
	}
}

func TestNameKey(t *testing.T) {
	if NameKey("Beyoncé (Live)", "Destiny's Child") != NameKey("beyonce live", "destinys child") {
		t.Error("name keys should fold case, accents and punctuation")
	}
	if NameKey("Song", "A") == NameKey("Song", "B") {
		t.Error("different artists share a name key")
	}
}
