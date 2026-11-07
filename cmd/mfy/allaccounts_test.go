package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/bandcamp"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/library"
	"github.com/bybrooklyn/mfetchy/internal/store"
)

func accountSongs() []library.Song {
	ytSong := func(id, title, album string) library.Song {
		return library.Song{
			Track: catalog.Track{Title: title, Artists: []string{"Artist"}, Album: album, Origin: catalog.YouTube, OriginID: id},
			From:  "YouTube Music", Supply: catalog.YouTube,
		}
	}
	it := bandcamp.Item{Title: "Great Album", Artist: "Artist", Type: "album", URL: "https://a.bandcamp.com/album/great", DownloadPage: "x"}
	return []library.Song{
		ytSong("aaaaaaaaaaa", "One", "Great Album"),
		ytSong("bbbbbbbbbbb", "Loose", ""),
		{
			Track: catalog.Track{Title: "Great Album", Artists: []string{"Artist"}, Origin: catalog.BandcampPurchase, OriginID: bandcamp.Normalize(it.URL)},
			From:  "Bandcamp", Supply: catalog.BandcampPurchase, Whole: true, Ref: it,
		},
	}
}

// With no database (a dry run in a fresh folder) nothing is held and everything is new.
func TestAllAccountsDryRunNoStore(t *testing.T) {
	p := library.MakePlan(library.Combine(accountSongs()), heldForPlan(nil))
	if c := p.Counts(); c.New != 2 || c.Held != 0 {
		t.Fatalf("%+v", c)
	}
}

// After a download, a second plan has nothing new; before it, a YouTube copy of a purchased
// album is an upgrade.
func TestAllAccountsPlanAgainstStore(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(appname.StateDir(root))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	file := filepath.Join(root, "one.opus")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = st.AddPending("aaaaaaaaaaa", "One", "Artist")
	_ = st.MarkDone("aaaaaaaaaaa", file)

	plan := func() library.Counts {
		return library.MakePlan(library.Combine(accountSongs()), heldForPlan(st)).Counts()
	}
	if c := plan(); c.Upgrades != 1 || c.New != 1 || c.Held != 0 {
		t.Fatalf("before: %+v", c)
	}
	// The purchase is downloaded (marker) and the loose song fetched from YouTube.
	_ = st.LibraryPut(albumMarkKey(bandcamp.Normalize("https://a.bandcamp.com/album/great")), "Great Album", "Artist",
		albumMarkSource, catalog.Rank(catalog.BandcampPurchase), root)
	loose := filepath.Join(root, "loose.opus")
	_ = os.WriteFile(loose, []byte("x"), 0o644)
	_ = st.AddPending("bbbbbbbbbbb", "Loose", "Artist")
	_ = st.MarkDone("bbbbbbbbbbb", loose)
	if c := plan(); c.New != 0 || c.Upgrades != 0 || c.Held != 2 {
		t.Fatalf("after: %+v", c)
	}
	// The album marker is not a song.
	hs, _ := st.Held()
	for _, h := range hs {
		if h.Source == albumMarkSource {
			t.Errorf("marker listed as a held song: %+v", h)
		}
	}
}
