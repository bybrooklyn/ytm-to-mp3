package store

import "testing"

func TestAppleState(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if st := s.Apple("1"); st.Done || st.Permanent {
		t.Fatalf("unknown song: %+v", st)
	}
	if err := s.AppleFailed("1", "Song", "Artist", "Unavailable", true); err != nil {
		t.Fatal(err)
	}
	if st := s.Apple("1"); !st.Permanent || st.Done {
		t.Fatalf("after permanent failure: %+v", st)
	}
	if err := s.AppleDone("1", "Song", "Artist", "/m/a.m4a"); err != nil {
		t.Fatal(err)
	}
	if st := s.Apple("1"); !st.Done || st.Path != "/m/a.m4a" || st.Attempts != 2 {
		t.Fatalf("after done: %+v", st)
	}
	// a rename by `mfy tag` follows the file
	if err := s.MovePath("/m/a.m4a", "/m/b.m4a"); err != nil {
		t.Fatal(err)
	}
	if st := s.Apple("1"); st.Path != "/m/b.m4a" {
		t.Fatalf("after move: %+v", st)
	}
}

func TestAppleMatchCache(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, ok := s.AppleMatch("aaaaaaaaaaa"); ok {
		t.Fatal("cached before lookup")
	}
	if err := s.SetAppleMatch("aaaaaaaaaaa", "", 0); err != nil {
		t.Fatal(err)
	}
	if data, _, ok := s.AppleMatch("aaaaaaaaaaa"); !ok || data != "" {
		t.Fatalf("not-found answer: %q %v", data, ok)
	}
	if err := s.SetAppleMatch("aaaaaaaaaaa", `{"id":"5"}`, 0.93); err != nil {
		t.Fatal(err)
	}
	if data, sc, ok := s.AppleMatch("aaaaaaaaaaa"); !ok || data != `{"id":"5"}` || sc != 0.93 {
		t.Fatalf("match: %q %v %v", data, sc, ok)
	}
}

func TestReplaceWithApple(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ReplaceWithApple("aaaaaaaaaaa", "Song", "Artist", "/m/Song.m4a"); err != nil {
		t.Fatal(err)
	}
	if p := s.Path("aaaaaaaaaaa"); p != "/m/Song.m4a" {
		t.Fatalf("path = %q", p)
	}
	todo, err := s.PremiumTodo(10)
	if err != nil || len(todo) != 0 {
		t.Fatalf("Premium pass would touch an Apple file: %v %v", todo, err)
	}
}
