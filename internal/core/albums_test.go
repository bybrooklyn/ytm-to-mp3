package core

import (
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/model"
)

func TestGroupAlbums(t *testing.T) {
	tr := func(id, title, album, artist string) model.Track {
		return model.Track{ID: id, Title: title, Album: album, Artists: []string{artist}}
	}
	tracks := []model.Track{
		tr("aaaaaaaaaaa", "Door", "Volume Alpha", "C418"),
		tr("bbbbbbbbbbb", "Wet Hands", "volume alpha", "c418"), // same album, other case
		tr("ccccccccccc", "Time", "Dark Side", "Pink Floyd"),
		tr("ddddddddddd", "No Album", "", "Someone"),
		{ID: "eeeeeeeeeee", Title: "No Artist", Album: "X"},
	}
	gs := groupAlbums(tracks, 1)
	if len(gs) != 2 || gs[0].title != "Volume Alpha" || len(gs[0].saved) != 2 {
		t.Fatalf("groups: %+v", gs)
	}
	if gs := groupAlbums(tracks, 2); len(gs) != 1 || gs[0].artist != "C418" {
		t.Fatalf("min 2: %+v", gs)
	}
}

func TestAlbumTracksSkipsWhatIHave(t *testing.T) {
	g := albumGroup{artist: "C418", title: "Volume Alpha", saved: []model.Track{
		{ID: "aaaaaaaaaaa", Title: "Door"},
		{ID: "mvmvmvmvmvm", Title: "Subwoofer Lullaby"}, // saved as a music video, other id
	}}
	a := &engine.Album{Tracks: []engine.AlbumTrack{
		{ID: "aaaaaaaaaaa", Title: "Door"},
		{ID: "sssssssssss", Title: "Subwoofer Lullaby"},
		{ID: "kkkkkkkkkkk", Title: "Key"},
	}}
	have := map[string]bool{"aaaaaaaaaaa": true}
	got := albumTracks(g, a, have)
	if len(got) != 1 || got[0].ID != "kkkkkkkkkkk" || got[0].Album != "Volume Alpha" || got[0].PrimaryArtist() != "C418" {
		t.Fatalf("got %+v", got)
	}
	if again := albumTracks(g, a, have); len(again) != 0 {
		t.Fatalf("a song must not be added twice: %+v", again)
	}
}
