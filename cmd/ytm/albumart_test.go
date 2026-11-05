package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
)

// A library song whose album is in the lookup cache gets the album's square art, year and
// place; a song with the same album name that isn't on it gets nothing from it.
func TestNumberFromAlbumsAddsArtAndYear(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), ".ytm"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := engine.Album{Title: "Volume Alpha", Cover: "https://i9.ytimg.com/s_p/x/maxresdefault.jpg", Year: 2011,
		Tracks: []engine.AlbumTrack{{ID: "aaaaaaaaaaa", Title: "Key"}, {ID: "bbbbbbbbbbb", Title: "Door"}}}
	b, _ := json.Marshal(a)
	if err := st.SetAlbum(albumKey("C418", "Volume Alpha"), string(b)); err != nil {
		t.Fatal(err)
	}
	ts := []model.Track{
		{ID: "bbbbbbbbbbb", Title: "Door", Album: "Volume Alpha", Artists: []string{"C418"}},
		{ID: "zzzzzzzzzzz", Title: "Other", Album: "Volume Alpha", Artists: []string{"C418"}},
	}
	numberFromAlbums(st, ts)
	if d := ts[0]; d.TrackNo != 2 || d.TrackTotal != 2 || d.Cover != a.Cover || d.Date != "2011" {
		t.Errorf("Door: %+v", d)
	}
	if o := ts[1]; o.Cover != "" || o.Date != "" || o.TrackNo != 0 {
		t.Errorf("a song not on the album must not get its art: %+v", o)
	}
	if !engine.AlbumCoverURL(a.Cover) || engine.AlbumCoverURL("https://ytimg.com.evil.example/x.jpg") {
		t.Error("album art host check")
	}
}
