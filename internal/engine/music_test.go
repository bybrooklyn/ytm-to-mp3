package engine

import "testing"

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
