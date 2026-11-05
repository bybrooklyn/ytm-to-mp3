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
