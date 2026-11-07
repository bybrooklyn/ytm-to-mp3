package apple

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLibrarySongsPagesAndSkipsUploads(t *testing.T) {
	var gotAuth, gotUser string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me/library/songs", func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUser = r.Header.Get("Authorization"), r.Header.Get("Music-User-Token")
		if r.URL.Query().Get("offset") == "100" {
			fmt.Fprint(w, `{"data":[{"attributes":{"name":"Uploaded"},"relationships":{"catalog":{"data":[]}}}]}`)
			return
		}
		fmt.Fprint(w, `{"next":"/v1/me/library/songs?limit=100&offset=100","data":[
		 {"attributes":{"name":"Lib"},"relationships":{"catalog":{"data":[{"id":"123","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","isrc":"US1","trackNumber":2,"genreNames":["Music","Pop"],"durationInMillis":1000}}]}}}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase, oldHTTP := libraryBase, libraryHTTP
	libraryBase, libraryHTTP = srv.URL, func() *http.Client { return srv.Client() }
	defer func() { libraryBase, libraryHTTP = oldBase, oldHTTP }()

	c, err := New("user-token", "", "us", "en-US")
	if err != nil {
		t.Fatal(err)
	}
	c.bearer = "dev-token" // no developer-token fetch in tests
	songs, skipped, err := c.LibrarySongs(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 1 || skipped != 1 {
		t.Fatalf("songs %d skipped %d", len(songs), skipped)
	}
	s := songs[0]
	if s.ID != "123" || s.Title != "Song" || s.ISRC != "US1" || s.Genre != "Pop" || s.TrackNumber != 2 {
		t.Errorf("%+v", s)
	}
	if gotAuth != "Bearer dev-token" || gotUser != "user-token" {
		t.Errorf("headers %q %q", gotAuth, gotUser)
	}
}

func TestLibrarySongsRejectedLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	oldBase, oldHTTP := libraryBase, libraryHTTP
	libraryBase, libraryHTTP = srv.URL, func() *http.Client { return srv.Client() }
	defer func() { libraryBase, libraryHTTP = oldBase, oldHTTP }()
	c, _ := New("t", "", "us", "")
	c.bearer = "x"
	if _, _, err := c.LibrarySongs(context.Background(), nil); err == nil {
		t.Fatal("want ErrLogin")
	}
}
