package names

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFoldKey(t *testing.T) {
	same := [][2]string{
		{"MASTER BOOT RECORD", "Master Boot Record"},
		{"meganeko", "Meganeko"},
		{"Café", "Café"}, // composed vs decomposed (macOS)
		{"Album.", "Album"},
		{"Ｔｅｓｔ", "test"}, // full-width
		{"Hello  World", "hello world"},
	}
	for _, c := range same {
		if FoldKey(c[0]) != FoldKey(c[1]) {
			t.Errorf("FoldKey(%q) != FoldKey(%q)", c[0], c[1])
		}
	}
	diff := [][2]string{{"Beyoncé", "Beyonce"}, {"Album 1", "Album 2"}, {"AC_DC", "ACDC"}}
	for _, c := range diff {
		if FoldKey(c[0]) == FoldKey(c[1]) {
			t.Errorf("FoldKey(%q) == FoldKey(%q), they are different spellings", c[0], c[1])
		}
	}
}

func TestEditionBase(t *testing.T) {
	for in, want := range map[string]string{
		"CHROMAKOPIA +":                         "CHROMAKOPIA",
		"CHROMAKOPIA":                           "CHROMAKOPIA",
		"Currents (Deluxe)":                     "Currents",
		"Currents (Deluxe Edition)":             "Currents",
		"Currents [Expanded Edition]":           "Currents",
		"Currents - Special Edition":            "Currents",
		"Rumours (Remastered 2004)":             "Rumours",
		"Rumours (2011 Remaster)":               "Rumours",
		"Rumours (Super Deluxe)":                "Rumours",
		"Rumours Deluxe Edition":                "Rumours",
		"Album (Deluxe) [Remastered]":           "Album",
		"Abbey Road (50th Anniversary Edition)": "Abbey Road",
		// not edition markers: different albums or plain parts of the name
		"Currents (Live)":                  "Currents (Live)",
		"Currents (Instrumentals)":         "Currents (Instrumentals)",
		"Currents (Acoustic)":              "Currents (Acoustic)",
		"Currents (2015)":                  "Currents (2015)",
		"Minecraft - Volume Beta":          "Minecraft - Volume Beta",
		"Vol. 2":                           "Vol. 2",
		"Deluxe":                           "Deluxe",
		"Deluxe Edition":                   "Deluxe Edition",
		"Best Of (Remixes)":                "Best Of (Remixes)",
		"Live at Budokan (Deluxe Edition)": "Live at Budokan",
	} {
		if got := EditionBase(in); got != want {
			t.Errorf("EditionBase(%q) = %q, want %q", in, got, want)
		}
	}
	if EditionKey("CHROMAKOPIA +") != EditionKey("chromakopia") {
		t.Error("deluxe and standard should share an edition key")
	}
	if EditionKey("Vol. 1") == EditionKey("Vol. 2") {
		t.Error("different volumes must not share a key")
	}
}

func TestDirsReusesExistingSpelling(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, age time.Duration) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		_ = os.Chtimes(p, mt, mt)
	}
	mk("Meganeko/Single/a.opus", time.Hour)
	mk("Tyler, The Creator/CHROMAKOPIA +/a.opus", time.Hour)
	want := filepath.Join(root, "Meganeko", "Single")
	if got := Dirs(root, "meganeko", "SINGLE"); got != want {
		t.Errorf("Dirs = %q, want %q", got, want)
	}
	want = filepath.Join(root, "Tyler, The Creator", "CHROMAKOPIA +")
	if got := Dirs(root, "Tyler, The Creator", "CHROMAKOPIA"); got != want {
		t.Errorf("Dirs = %q, want %q (the deluxe edition)", got, want)
	}
	// nothing exists: the name as given, and the first spelling seen sticks within the run
	first := Dirs(root, "New Artist", "")
	if first != filepath.Join(root, "New Artist") {
		t.Errorf("got %q", first)
	}
	if got := Dirs(root, "NEW ARTIST", ""); got != first {
		t.Errorf("a second spelling in one run must follow the first: %q vs %q", got, first)
	}
	if got := Dirs(root, "", ""); got != filepath.Join(root, "Unknown Artist") {
		t.Errorf("got %q", got)
	}
}

func TestDirsPicksOldestWhenSpellingsCoexist(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, age time.Duration) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		mt := time.Now().Add(-age)
		_ = os.Chtimes(p, mt, mt)
	}
	mk("ARTIST/x.opus", 10*time.Hour)
	mk("artist/y.opus", time.Hour)
	mk("artist/z.opus", time.Hour)
	// both exist: the one with the earliest file wins even though the other has more files
	// (if the filesystem is case-insensitive only one of them exists, which is fine)
	got := Dirs(root, "Artist", "")
	if _, err := os.Stat(filepath.Join(root, "artist", "y.opus")); err == nil {
		if _, err := os.Stat(filepath.Join(root, "ARTIST", "x.opus")); err == nil && got != filepath.Join(root, "ARTIST") {
			t.Errorf("want the oldest folder, got %q", got)
		}
	}
}

func TestSameArtist(t *testing.T) {
	yes := [][2]string{
		{"Tyler, The Creator", "Tyler The Creator"},
		{"Beyoncé", "BEYONCE"},
		{"Sigur Rós", "Sigur Ros"},
		{"Simon & Garfunkel", "Simon and Garfunkel"},
		{"Radiohead - Topic", "Radiohead"},
		{"RadioheadVEVO", "Radiohead"},
		{"The Weeknd", "Weeknd"},
		{"The Beatles", "Beatles"},
		{"Drake feat. Rihanna", "Drake"},
		{"Drake", "Drake, Rihanna"},
		{"Queen + Adam Lambert", "Queen"},
		{"P!nk", "Pink"},
		{"Ke$ha", "Kesha"},
		{"Run DMC", "RunDMC"},
		{"Daft Punk", "daft punk"},
		{"Mötley Crüe", "Motley Crue"},
		{"Earth, Wind & Fire", "Earth, Wind and Fire"},
		{"Kendrick Lamar & SZA", "SZA"},
	}
	for _, c := range yes {
		if !SameArtist(c[0], c[1]) {
			t.Errorf("SameArtist(%q, %q) = false", c[0], c[1])
		}
	}
	no := [][2]string{
		{"Pink", "Pink Floyd"},
		{"Eve", "Steve Miller"},
		{"Eve", "Eve 6"},
		{"Queen", "Queens of the Stone Age"},
		{"Pink Floyd", "Some Cover Band"},
		{"Drake", "Drake Bell"},
		{"", "Anyone"},
	}
	for _, c := range no {
		if SameArtist(c[0], c[1]) {
			t.Errorf("SameArtist(%q, %q) = true", c[0], c[1])
		}
	}
}
