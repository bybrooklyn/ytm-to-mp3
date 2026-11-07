package store

import (
	"strings"
	"testing"
)

func TestMovePathFollowsEveryTable(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.AddPending("aaaaaaaaaaa", "Song A", "Artist")
	_ = s.MarkDone("aaaaaaaaaaa", "/m/MASTER/Song A.opus")
	_ = s.AppleDone("123", "Song B", "Artist", "/m/MASTER/Song B.m4a")
	_ = s.LibraryPut("name:song c|artist", "Song C", "Artist", "bandcamp-purchase", 4, "/m/MASTER/Song C.flac")
	for _, f := range []string{"Song A.opus", "Song B.m4a", "Song C.flac"} {
		if err := s.MovePath("/m/MASTER/"+f, "/m/Master/"+f); err != nil {
			t.Fatal(err)
		}
	}
	if _, p := s.TrackStatus("aaaaaaaaaaa"); p != "/m/Master/Song A.opus" {
		t.Errorf("tracks.path = %q", p)
	}
	if got := s.Library("name:song c|artist").Path; got != "/m/Master/Song C.flac" {
		t.Errorf("library_tracks.path = %q", got)
	}
	hs, _ := s.Held()
	for _, h := range hs {
		if !strings.HasPrefix(h.Path, "/m/Master/") {
			t.Errorf("held %q still at the old path", h.Path)
		}
	}
}

func TestProvenanceRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Provenance("k") != nil {
		t.Fatal("nothing recorded yet")
	}
	if err := s.SetProvenance("k", map[string]string{"isrc": "youtube", "title": "bandcamp-purchase"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Provenance("k"); got["isrc"] != "youtube" || got["title"] != "bandcamp-purchase" {
		t.Errorf("provenance = %v", got)
	}
}

func TestHeld(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.AddPending("aaaaaaaaaaa", "Song A", "Artist")
	_ = s.MarkDone("aaaaaaaaaaa", "/m/Artist/Song A.opus")
	_ = s.AddPending("bbbbbbbbbbb", "Song B", "Artist")
	_ = s.AddPending("ccccccccccc", "Song C", "Artist") // still pending: not held
	_ = s.AppleDone("123", "Song B", "Artist", "/m/Artist/Song B.m4a")
	_ = s.ReplaceWithApple("bbbbbbbbbbb", "Song B", "Artist", "/m/Artist/Song B.m4a")

	hs, err := s.Held()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range hs {
		got[h.Title] = h.Source
	}
	if len(hs) != 2 || got["Song A"] != "youtube" || got["Song B"] != "apple" {
		t.Fatalf("held = %+v", hs)
	}
}
