package store

import "testing"

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
