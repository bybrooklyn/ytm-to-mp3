package ytmusic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
)

func row(id, title string, extra ...obj) obj {
	cols := []any{obj{"musicResponsiveListItemFlexColumnRenderer": obj{"text": obj{"runs": []any{obj{"text": title}}}}}}
	for _, c := range extra {
		cols = append(cols, obj{"musicResponsiveListItemFlexColumnRenderer": obj{"text": obj{"runs": c["runs"]}}})
	}
	return obj{"musicResponsiveListItemRenderer": obj{"playlistItemData": obj{"videoId": id}, "flexColumns": cols}}
}

func link(text, bid string) obj {
	return obj{"text": text, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": bid}}}
}

func TestSongRowArtistsAndAlbumByIDKind(t *testing.T) {
	r := row("aaaaaaaaaaa", "Song", obj{"runs": []any{link("A", "UC1"), obj{"text": " & "}, link("B", "UC2")}}, obj{"runs": []any{link("The Album", "MPREb_x")}})
	got, ok := song(asObj(r["musicResponsiveListItemRenderer"]))
	if !ok || got.Title != "Song" || strings.Join(got.Artists, "|") != "A|B" || got.Album != "The Album" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestSongSkipsRowsWithoutVideo(t *testing.T) {
	r := obj{"flexColumns": []any{}}
	if _, ok := song(r); ok {
		t.Fatal("a row with no video id is not a song")
	}
}

func TestSongsOnlyFromWantedShelf(t *testing.T) {
	resp := obj{"a": obj{
		"musicPlaylistShelfRenderer": obj{"contents": []any{row("aaaaaaaaaaa", "In")}},
		"musicShelfRenderer":         obj{"contents": []any{row("bbbbbbbbbbb", "Suggestion")}},
	}}
	got := songs(resp, "musicPlaylistShelfRenderer")
	if len(got) != 1 || got[0].ID != "aaaaaaaaaaa" {
		t.Fatalf("got %+v", got)
	}
}

func TestContinuationResponsesAreRead(t *testing.T) {
	resp := obj{"continuationContents": obj{"musicShelfContinuation": obj{"contents": []any{row("ccccccccccc", "Next")}}}}
	if got := songs(resp, "musicShelfRenderer"); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	appended := obj{"onResponseReceivedActions": []any{obj{"appendContinuationItemsAction": obj{"continuationItems": []any{row("ddddddddddd", "More")}}}}}
	if got := songs(appended, "musicShelfRenderer"); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestContinuationTokens(t *testing.T) {
	old := obj{"musicShelfRenderer": obj{"continuations": []any{obj{"nextContinuationData": obj{"continuation": "T1"}}}}}
	if continuation(old) != "T1" {
		t.Fatal("nextContinuationData")
	}
	neu := obj{"x": []any{obj{"continuationItemRenderer": obj{"continuationEndpoint": obj{"continuationCommand": obj{"token": "T2"}}}}}}
	if continuation(neu) != "T2" {
		t.Fatal("continuationItemRenderer")
	}
	if continuation(obj{}) != "" {
		t.Fatal("no token expected")
	}
}

func TestCardsAndHeader(t *testing.T) {
	resp := obj{"g": []any{
		obj{"musicTwoRowItemRenderer": obj{"title": obj{"runs": []any{obj{"text": "Alb"}}}, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": "MPREb_1"}}}},
		obj{"musicTwoRowItemRenderer": obj{"title": obj{"runs": []any{obj{"text": "PL"}}}, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": "VLPL1"}}}},
	}}
	if c := cards(resp, "MPREb"); len(c) != 1 || c[0].Title != "Alb" {
		t.Fatalf("albums %+v", c)
	}
	if c := cards(resp, "VL"); len(c) != 1 || c[0].ID != "VLPL1" {
		t.Fatalf("playlists %+v", c)
	}
	page := obj{"musicResponsiveHeaderRenderer": obj{"title": obj{"runs": []any{obj{"text": "Alb"}}}, "straplineTextOne": obj{"runs": []any{link("Artist", "UCx")}}}}
	if h := pageHeader(page); h.title != "Alb" || h.artist != "Artist" {
		t.Fatalf("header %+v", h)
	}
}

func TestAuthorizationHeader(t *testing.T) {
	c, err := New(nil, []browsercookies.Cookie{{Name: "SAPISID", Value: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	// sha1("1700000000 abc https://music.youtube.com")
	if got := c.authorization(); !strings.HasPrefix(got, "SAPISIDHASH 1700000000_") || len(got) != len("SAPISIDHASH 1700000000_")+40 {
		t.Fatalf("got %q", got)
	}
	if _, err := New(nil, nil); err == nil {
		t.Fatal("no cookie should be an error")
	}
}

func TestLibraryEndToEnd(t *testing.T) {
	album := obj{
		"musicResponsiveHeaderRenderer": obj{"title": obj{"runs": []any{obj{"text": "Big Album"}}}, "straplineTextOne": obj{"runs": []any{link("Band", "UCband")}}},
		"musicShelfRenderer":            obj{"contents": []any{row("eeeeeeeeeee", "Track One"), row("fffffffffff", "Track Two")}},
	}
	pages := map[string]any{
		"VLLM": obj{"musicPlaylistShelfRenderer": obj{"contents": []any{row("aaaaaaaaaaa", "Liked", obj{"runs": []any{link("Someone", "UC9")}})}}},
		"FEmusic_liked_videos": obj{
			"musicShelfRenderer": obj{"contents": []any{row("aaaaaaaaaaa", "Liked"), row("ggggggggggg", "Saved")},
				"continuations": []any{obj{"nextContinuationData": obj{"continuation": "NEXT"}}}},
		},
		"FEmusic_liked_albums": obj{"g": []any{obj{"musicTwoRowItemRenderer": obj{"title": obj{"runs": []any{obj{"text": "Big Album"}}}, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": "MPREb_big"}}}}}},
		"FEmusic_liked_playlists": obj{"g": []any{
			obj{"musicTwoRowItemRenderer": obj{"title": obj{"runs": []any{obj{"text": "Mix"}}}, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": "VLPLmix"}}}},
			obj{"musicTwoRowItemRenderer": obj{"title": obj{"runs": []any{obj{"text": "Liked"}}}, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": "VLLM"}}}},
			// an auto-generated mix: never requested (the fake server has no such page, so a
			// request would count as an unreadable playlist)
			obj{"musicTwoRowItemRenderer": obj{"title": obj{"runs": []any{obj{"text": "My Mix"}}}, "navigationEndpoint": obj{"browseEndpoint": obj{"browseId": "VLRDTMAK5uy_x"}}}},
		}},
		"MPREb_big": album,
		"VLPLmix":   obj{"musicPlaylistShelfRenderer": obj{"contents": []any{row("hhhhhhhhhhh", "In the mix"), row("eeeeeeeeeee", "Track One")}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "SAPISIDHASH ") {
			http.Error(w, "no", 401)
			return
		}
		if r.URL.Query().Get("ctoken") == "NEXT" {
			_ = json.NewEncoder(w).Encode(obj{"continuationContents": obj{"musicShelfContinuation": obj{"contents": []any{row("iiiiiiiiiii", "From page two")}}}})
			return
		}
		var body struct {
			BrowseID string `json:"browseId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p, ok := pages[body.BrowseID]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	}))
	defer srv.Close()

	c, _ := New(srv.Client(), []browsercookies.Cookie{{Name: "SAPISID", Value: "s"}})
	c.url = srv.URL + "/browse?x=1"
	lib, err := c.Library(context.Background(), Options{Albums: true, Playlists: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, tr := range lib.Tracks {
		ids[tr.ID] = tr.Title
	}
	if len(lib.Tracks) != 6 {
		t.Fatalf("want 6 distinct songs, got %d: %v", len(lib.Tracks), ids)
	}
	if lib.Liked != 1 || lib.Saved != 2 || lib.Albums != 1 || lib.Playlists != 1 || lib.Skipped != 0 {
		t.Fatalf("counts %+v", lib)
	}
	for _, tr := range lib.Tracks {
		if tr.ID == "fffffffffff" && (tr.Album != "Big Album" || tr.TrackNo != 2 || tr.TrackTotal != 2 || tr.PrimaryArtist() != "Band") {
			t.Fatalf("album song not filled in from its page: %+v", tr)
		}
	}
}

func TestSignedOutIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) }))
	defer srv.Close()
	c, _ := New(srv.Client(), []browsercookies.Cookie{{Name: "SAPISID", Value: "s"}})
	c.url = srv.URL + "/b?x=1"
	if _, err := c.Library(context.Background(), Options{}); err != ErrSignedOut {
		t.Fatalf("got %v", err)
	}
}

func searchRow(title, browse string) obj {
	return obj{"musicResponsiveListItemRenderer": obj{
		"navigationEndpoint": obj{"browseEndpoint": obj{"browseId": browse}},
		"flexColumns":        []any{obj{"musicResponsiveListItemFlexColumnRenderer": obj{"text": obj{"runs": []any{obj{"text": title}}}}}},
	}}
}

func fakeServer(t *testing.T, search, browse map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("anonymous client must not send account headers")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		var v any
		if strings.Contains(r.URL.Path, "/search") {
			v = search[str(body["params"])]
		} else {
			v = browse[str(body["browseId"])]
		}
		if v == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	c := NewAnonymous(srv.Client())
	c.url, c.search = srv.URL+"/youtubei/v1/browse?x=1", srv.URL+"/youtubei/v1/search?x=1"
	return c
}

func TestSearchSongsAndAlbums(t *testing.T) {
	c := fakeServer(t, map[string]any{
		filterSongs:  obj{"musicShelfRenderer": obj{"contents": []any{row("aaaaaaaaaaa", "One"), row("bbbbbbbbbbb", "Two"), row("ccccccccccc", "Three")}}},
		filterAlbums: obj{"musicShelfRenderer": obj{"contents": []any{searchRow("Alb", "MPREb_1"), searchRow("Alb (Deluxe)", "MPREb_2"), searchRow("Not an album", "UCxyz")}}},
	}, nil)
	hits, err := c.SearchSongs(context.Background(), "q", 2)
	if err != nil || len(hits) != 2 || hits[1].ID != "bbbbbbbbbbb" {
		t.Fatalf("songs %+v %v", hits, err)
	}
	albums, err := c.SearchAlbums(context.Background(), "q", 5)
	if err != nil || len(albums) != 2 || albums[0].BrowseID != "MPREb_1" || albums[1].Title != "Alb (Deluxe)" {
		t.Fatalf("albums %+v %v", albums, err)
	}
}

func TestAlbumPageYearThumbsTracks(t *testing.T) {
	page := obj{
		"musicResponsiveHeaderRenderer": obj{
			"title":    obj{"runs": []any{obj{"text": "Big Album"}}},
			"subtitle": obj{"runs": []any{obj{"text": "Album"}, obj{"text": " • "}, obj{"text": "2019"}}},
			"thumbnail": obj{"musicThumbnailRenderer": obj{"thumbnail": obj{"thumbnails": []any{
				obj{"url": "https://lh3.googleusercontent.com/x=w226-h226-l90-rj", "width": 226.0, "height": 226.0},
				obj{"url": "https://lh3.googleusercontent.com/x=w544-h544-l90-rj"},
			}}}},
		},
		"musicShelfRenderer": obj{"contents": []any{row("aaaaaaaaaaa", "T1"), row("bbbbbbbbbbb", "T2")}},
	}
	c := fakeServer(t, nil, map[string]any{"MPREb_big": page})
	a, err := c.Album(context.Background(), "MPREb_big")
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Big Album" || a.Year != 2019 || len(a.Tracks) != 2 || len(a.Thumbs) != 2 || a.Thumbs[1].W != 544 || a.Thumbs[1].H != 544 {
		t.Fatalf("%+v", a)
	}
	if _, err := c.Album(context.Background(), "../etc"); err == nil {
		t.Fatal("a non-album id must be refused")
	}
	if _, err := c.Album(context.Background(), "MPREb_missing"); err == nil {
		t.Fatal("a page that doesn't exist must be an error")
	}
}

func TestPlaylist(t *testing.T) {
	c := fakeServer(t, nil, map[string]any{"VLPL1": obj{"musicPlaylistShelfRenderer": obj{"contents": []any{row("aaaaaaaaaaa", "A"), row("aaaaaaaaaaa", "A again"), row("bbbbbbbbbbb", "B")}}}})
	ts, err := c.Playlist(context.Background(), "PL1")
	if err != nil || len(ts) != 2 {
		t.Fatalf("%+v %v", ts, err)
	}
	if ts2, err := c.Playlist(context.Background(), "VLPL1"); err != nil || len(ts2) != 2 {
		t.Fatal("a VL prefix must be accepted")
	}
}
