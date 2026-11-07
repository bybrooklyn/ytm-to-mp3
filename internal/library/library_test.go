package library

import (
	"context"
	"errors"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/browsercookies"
	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
)

func yt(id, title, artist, album string) Song {
	return Song{
		Track: catalog.Track{Title: title, Artists: []string{artist}, Album: album, Origin: catalog.YouTube, OriginID: id},
		From:  "YouTube Music", Supply: catalog.YouTube,
	}
}

func ap(id, title, artist, album, isrc string) Song {
	return Song{
		Track: catalog.Track{Title: title, Artists: []string{artist}, Album: album, ISRC: isrc, TrackNo: 3, TrackTotal: 10,
			ReleaseDate: "2019-05-31", Origin: catalog.Apple, OriginID: id},
		From: "Apple Music", Supply: catalog.Apple,
	}
}

func bcAlbum(title, artist string) Song {
	return Song{
		Track: catalog.Track{Title: title, Artists: []string{artist}, Origin: catalog.BandcampPurchase, OriginID: "https://a.bandcamp.com/album/x"},
		From:  "Bandcamp", Supply: catalog.BandcampPurchase, Whole: true,
	}
}

func bcTrack(title, artist string) Song {
	return Song{
		Track: catalog.Track{Title: title, Artists: []string{artist}, Origin: catalog.BandcampPurchase, OriginID: "https://a.bandcamp.com/track/x"},
		From:  "Bandcamp", Supply: catalog.BandcampPurchase,
	}
}

func TestCombineSameSongDifferentSpellings(t *testing.T) {
	es := Combine([]Song{
		yt("aaaaaaaaaaa", "Night Drive (feat. Mia)", "The Band", "Night Drive - Single"),
		bcTrack("Night Drive", "Band, The"),
	})
	if len(es) != 1 {
		t.Fatalf("want 1 entry, got %d: %v", len(es), es)
	}
	e := es[0]
	if got := e.Accounts(); len(got) != 2 {
		t.Errorf("accounts = %v", got)
	}
	if e.Best().Supply != catalog.BandcampPurchase {
		t.Errorf("best = %v", e.Best().Supply)
	}
}

func TestCombineISRCBeatsNames(t *testing.T) {
	// Same ISRC, titles spelled differently: one song.
	es := Combine([]Song{
		ap("1", "Hello", "Adele", "25", "GBBKS1500214"),
		func() Song {
			s := yt("bbbbbbbbbbb", "Hello (Remastered)", "Adele", "25")
			s.ISRC = "gbbks1500214"
			return s
		}(),
	})
	if len(es) != 1 {
		t.Fatalf("want 1, got %d", len(es))
	}
	// Different ISRCs, identical names: two recordings.
	es = Combine([]Song{
		ap("1", "Hello", "Adele", "25", "AAA111111111"),
		func() Song { s := yt("bbbbbbbbbbb", "Hello", "Adele", "25"); s.ISRC = "BBB222222222"; return s }(),
	})
	if len(es) != 2 {
		t.Fatalf("want 2, got %d", len(es))
	}
}

func TestCombineISRCOnOneSideOnly(t *testing.T) {
	es := Combine([]Song{
		ap("1", "Sunset", "Foo", "Album", "USABC1900001"),
		yt("ccccccccccc", "Sunset", "Foo", "Album (Deluxe)"),
	})
	if len(es) != 1 {
		t.Fatalf("editions must unify: got %d", len(es))
	}
	if es[0].Track.ISRC != "USABC1900001" || es[0].Key != "isrc:USABC1900001" {
		t.Errorf("merged track lost the ISRC: %+v key %s", es[0].Track, es[0].Key)
	}
	if es[0].Track.TrackNo != 3 || es[0].Track.ReleaseDate != "2019-05-31" {
		t.Errorf("best metadata not taken from Apple: %+v", es[0].Track)
	}
}

func TestCombineDifferentAlbumsAreDifferentSongs(t *testing.T) {
	es := Combine([]Song{
		yt("aaaaaaaaaaa", "Intro", "Foo", "First"),
		yt("bbbbbbbbbbb", "Intro", "Foo", "Second"),
	})
	if len(es) != 2 {
		t.Fatalf("got %d", len(es))
	}
}

func TestCombineDurationSeparatesVersions(t *testing.T) {
	a := yt("aaaaaaaaaaa", "Song", "Foo", "")
	a.DurationMillis = 180000
	b := yt("bbbbbbbbbbb", "Song", "Foo", "")
	b.DurationMillis = 240000
	if len(Combine([]Song{a, b})) != 2 {
		t.Fatal("a 4-minute and a 3-minute song are not the same")
	}
}

func TestCombineSingleAccountSongStaysAlone(t *testing.T) {
	es := Combine([]Song{yt("aaaaaaaaaaa", "Only Here", "Foo", ""), ap("1", "Other", "Bar", "X", "")})
	if len(es) != 2 || len(es[0].Accounts()) != 1 {
		t.Fatalf("%v", es)
	}
}

func TestCombineAlbumPurchaseCoversOtherAccountsSongs(t *testing.T) {
	es := Combine([]Song{
		yt("aaaaaaaaaaa", "One", "Artist", "Great Album (Deluxe Edition)"),
		yt("bbbbbbbbbbb", "Two", "Artist", "Great Album"),
		yt("ccccccccccc", "Elsewhere", "Artist", "Other Album"),
		bcAlbum("Great Album", "Artist"),
	})
	if len(es) != 3 {
		t.Fatalf("want 3 entries (purchase covers two), got %d: %v", len(es), es)
	}
	for _, e := range es[:2] {
		if e.Best().Supply != catalog.BandcampPurchase || !e.Best().Whole {
			t.Errorf("%v not covered by the purchase", e)
		}
	}
	if es[2].Best().Supply != catalog.YouTube {
		t.Errorf("unrelated song picked up a purchase")
	}
}

func TestCombineUncoveredAlbumPurchaseIsItsOwnEntry(t *testing.T) {
	es := Combine([]Song{yt("aaaaaaaaaaa", "One", "Artist", "Else"), bcAlbum("Great Album", "Artist")})
	if len(es) != 2 || !es[1].Whole || es[1].Track.Album != "Great Album" {
		t.Fatalf("%v", es)
	}
}

func TestMergeKeepsFeaturedArtists(t *testing.T) {
	a := yt("aaaaaaaaaaa", "Song", "Foo", "")
	b := ap("1", "Song", "Foo", "", "")
	b.Artists = []string{"Foo & Bar"}
	es := Combine([]Song{a, b})
	if len(es) != 1 || es[0].Track.Artists[0] != "Foo & Bar" {
		t.Fatalf("%+v", es)
	}
}

// holdings is a fake music folder.
type holdings map[string]HeldCopy

func (h holdings) fn(e Entry, _ Song) (HeldCopy, bool) { c, ok := h[e.Key]; return c, ok }

func TestPlanStatesAndCounts(t *testing.T) {
	songs := []Song{
		yt("aaaaaaaaaaa", "New One", "Foo", ""),
		yt("bbbbbbbbbbb", "Held YT", "Foo", ""),
		yt("ccccccccccc", "Upgradable", "Foo", "Rec"),
		bcAlbum("Rec", "Foo"),
		ap("1", "Apple Only", "Bar", "B", "ISRC00000001"),
	}
	songs[0].DurationMillis = 200000
	es := Combine(songs)
	var held = holdings{}
	for _, e := range es {
		switch e.Track.Title {
		case "Held YT":
			held[e.Key] = HeldCopy{Source: catalog.YouTube, Rank: catalog.Rank(catalog.YouTube)}
		case "Upgradable":
			held[e.Key] = HeldCopy{Source: catalog.YouTube, Rank: catalog.Rank(catalog.YouTube)}
		case "Apple Only":
			held[e.Key] = HeldCopy{Source: catalog.BandcampPurchase, Rank: 4} // better than needed
		}
	}
	p := MakePlan(es, held.fn)
	c := p.Counts()
	if c.Total != 4 || c.New != 1 || c.Upgrades != 1 || c.Held != 2 {
		t.Fatalf("counts %+v", c)
	}
	if c.Download[catalog.YouTube] != 1 || c.Download[catalog.BandcampPurchase] != 1 {
		t.Errorf("download %v", c.Download)
	}
	if c.PerAccount["YouTube Music"] != 3 || c.OnlyOn["Apple Music"] != 1 {
		t.Errorf("per account %v only %v", c.PerAccount, c.OnlyOn)
	}
	if c.EstimateOf != 1 || c.EstimateBytes <= 0 {
		t.Errorf("estimate %d of %d", c.EstimateBytes, c.EstimateOf)
	}
	if todo := p.Todo(); len(todo) != 2 || todo[0].State != New || todo[1].State != Upgrade {
		t.Errorf("todo %v", todo)
	}
}

func TestPlanIsIdempotent(t *testing.T) {
	songs := []Song{
		yt("aaaaaaaaaaa", "A", "Foo", "Rec"), yt("bbbbbbbbbbb", "B", "Foo", ""),
		ap("1", "C", "Bar", "", ""), bcAlbum("Rec", "Foo"),
	}
	held := holdings{}
	first := MakePlan(Combine(songs), held.fn)
	if len(first.Todo()) != 3 {
		t.Fatalf("todo %d", len(first.Todo()))
	}
	for _, it := range first.Todo() { // "download" everything from its best source
		held[it.Entry.Key] = HeldCopy{Source: it.Best.Supply, Rank: catalog.Rank(it.Best.Supply)}
	}
	// A second read of the same accounts plans nothing.
	second := MakePlan(Combine(songs), held.fn)
	if c := second.Counts(); c.New != 0 || c.Upgrades != 0 || len(second.Todo()) != 0 {
		t.Fatalf("second plan not empty: %+v", c)
	}
}

func TestPlanWithoutHeldLookupIsAllNew(t *testing.T) {
	p := MakePlan(Combine([]Song{yt("aaaaaaaaaaa", "A", "Foo", "")}), nil)
	if p.Counts().New != 1 {
		t.Fatal("dry run with no music folder: everything is new")
	}
}

// --- sources ---------------------------------------------------------------

type fakeSource struct {
	name  string
	setUp bool
	songs []Song
	err   error
	reads int
}

func (f *fakeSource) Name() string             { return f.name }
func (f *fakeSource) Supplies() catalog.Source { return catalog.YouTube }
func (f *fakeSource) Detect() Status           { return Status{SetUp: f.setUp, Hint: "not set up"} }
func (f *fakeSource) Read(context.Context, func(string)) ([]Song, error) {
	f.reads++
	return f.songs, f.err
}

func TestReadAllSkipsUnsetAndSurvivesFailures(t *testing.T) {
	a := &fakeSource{name: "A", setUp: true, songs: []Song{yt("aaaaaaaaaaa", "X", "Foo", "")}}
	b := &fakeSource{name: "B", setUp: false, songs: []Song{yt("bbbbbbbbbbb", "Y", "Foo", "")}}
	c := &fakeSource{name: "C", setUp: true, err: errors.New("boom")}
	songs, res := ReadAll(context.Background(), []LibrarySource{a, b, c}, nil)
	if len(songs) != 1 || b.reads != 0 {
		t.Fatalf("songs %d, unset source read %d times", len(songs), b.reads)
	}
	if !res[1].Skipped || res[2].Err == nil || res[0].Songs != 1 {
		t.Errorf("%+v", res)
	}
}

func TestRegisteredSourcesAndExtensibility(t *testing.T) {
	n := len(Sources())
	if n < 3 {
		t.Fatalf("built-in sources missing: %d", n)
	}
	Register(&fakeSource{name: "Later"})
	if len(Sources()) != n+1 {
		t.Fatal("Register did not add the source")
	}
	regMu.Lock()
	registry = registry[:n]
	regMu.Unlock()
}

func TestDetectWithNoConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	none := func() []browsercookies.Browser { return nil }
	for _, s := range []LibrarySource{
		&YouTube{Browsers: none}, &Bandcamp{Browsers: none}, &Apple{Token: func() string { return "" }},
	} {
		st := s.Detect()
		if st.SetUp || st.Hint == "" {
			t.Errorf("%s: %+v", s.Name(), st)
		}
	}
}

func TestDetectSetUp(t *testing.T) {
	ff := func() []browsercookies.Browser { return []browsercookies.Browser{{ID: "firefox", Name: "Firefox"}} }
	cfg := func() config.Config { return config.Config{YouTubeBrowser: "firefox", BandcampBrowser: "gone"} }
	if st := (&YouTube{Config: cfg, Browsers: ff}).Detect(); !st.SetUp || st.Detail != "reads Firefox" {
		t.Errorf("%+v", st)
	}
	if st := (&Bandcamp{Config: cfg, Browsers: ff}).Detect(); st.SetUp {
		t.Errorf("a browser that is gone must not count as set up: %+v", st)
	}
	if st := (&Apple{Token: func() string { return "tok" }}).Detect(); !st.SetUp || st.Detail != "signed in" {
		t.Errorf("%+v", st)
	}
}
