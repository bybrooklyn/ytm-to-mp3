package bandcamp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func page(t *testing.T, v any) []byte {
	t.Helper()
	b, _ := json.Marshal(v)
	return []byte(`<html><div id="pagedata" data-blob="` + html.EscapeString(string(b)) + `"></div></html>`)
}

func TestParseFormatsBestFirst(t *testing.T) {
	p := page(t, map[string]any{"download_items": []any{map[string]any{"downloads": map[string]any{
		"mp3-v0":  map[string]any{"url": "https://x/v0"},
		"flac":    map[string]any{"url": "https://x/flac"},
		"mp3-320": map[string]any{"url": "https://x/320"},
		"weird":   map[string]any{"url": "https://x/w"},
	}}}})
	fs, err := ParseFormats(p)
	if err != nil {
		t.Fatal(err)
	}
	if fs[0].Name != "flac" || fs[1].Name != "mp3-320" || fs[2].Name != "mp3-v0" || fs[len(fs)-1].Name != "weird" {
		t.Fatalf("%+v", fs)
	}
	old := page(t, map[string]any{"digital_items": []any{map[string]any{"downloads": map[string]any{"mp3-320": map[string]any{"url": "https://x/320"}}}}})
	if fs, err := ParseFormats(old); err != nil || fs[0].Name != "mp3-320" {
		t.Fatalf("older page shape: %+v %v", fs, err)
	}
	if _, err := ParseFormats([]byte("<title>Client Challenge</title>")); err == nil {
		t.Fatal("a bot-check page has no download data")
	}
	if _, err := ParseFormats(page(t, map[string]any{"download_items": []any{}})); err == nil {
		t.Fatal("no formats is an error")
	}
}

func fakeBandcamp(t *testing.T, zipBytes []byte) (*Client, *int) {
	t.Helper()
	cookieChecks := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "identity=abc") {
			http.Error(w, "no", 401)
			return
		}
		cookieChecks++
		switch {
		case r.URL.Path == "/api/fan/2/collection_summary":
			fmt.Fprint(w, `{"fan_id":42,"collection_summary":{"username":"me","url":"https://bandcamp.com/me"}}`)
		case r.URL.Path == "/api/fancollection/1/collection_items":
			var body struct {
				Token string `json:"older_than_token"`
				Fan   int64  `json:"fan_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Fan != 42 {
				http.Error(w, "bad fan", 400)
				return
			}
			if strings.HasPrefix(body.Token, "second") {
				fmt.Fprint(w, `{"items":[{"item_title":"Single","band_name":"B","item_type":"track","item_url":"https://b.bandcamp.com/track/s","sale_item_type":"p","sale_item_id":2,"purchased":"2020"}],"more_available":false,"last_token":"","redownload_urls":{"p2":"`+srv.URL+`/dl/p2"}}`)
				return
			}
			fmt.Fprint(w, `{"items":[{"item_title":"Album","band_name":"A","item_type":"album","item_url":"https://a.bandcamp.com/album/al/","sale_item_type":"p","sale_item_id":1,"purchased":"2021"},{"item_title":"Unclaimed gift","band_name":"G","item_type":"album","item_url":"https://g.bandcamp.com/album/x","sale_item_type":"p","sale_item_id":9,"purchased":""}],"more_available":true,"last_token":"second:0:a::","redownload_urls":{"p1":"`+srv.URL+`/dl/p1","p9":"`+srv.URL+`/dl/p9"}}`)
		case strings.HasPrefix(r.URL.Path, "/statdownload/"):
			// the real endpoint wraps its answer in a JavaScript callback
			fmt.Fprint(w, `if ( window.Downloads ) { Downloads.statResult ( {"result":"ok","url":"x","download_url":"`+srv.URL+`/file`+strings.TrimPrefix(r.URL.Path, "/statdownload")+`"} ) };`)
		case r.URL.Path == "/file/album":
			_, _ = w.Write(zipBytes)
		case r.URL.Path == "/file/track":
			w.Header().Set("Content-Disposition", `attachment; filename="B - Single.flac"`)
			_, _ = w.Write([]byte("fLaC...."))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{hc: srv.Client(), cookie: "identity=abc", base: srv.URL, FanID: 42}
	return c, &cookieChecks
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestCollectionPagesAndSkipsUnclaimedGifts(t *testing.T) {
	c, _ := fakeBandcamp(t, nil)
	items, err := c.Collection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "Album" || items[1].Title != "Single" || items[1].Type != "track" {
		t.Fatalf("%+v", items)
	}
	if !strings.HasSuffix(items[0].DownloadPage, "/dl/p1") || !strings.HasSuffix(items[1].DownloadPage, "/dl/p2") {
		t.Fatalf("download pages matched by sale id: %+v", items)
	}
}

func TestFetchAlbumZipKeepsAudioOnlyAndStaysInsideDir(t *testing.T) {
	z := zipOf(t, map[string]string{
		"A - Album/01 A - One.flac": "one",
		"A - Album/02 A - Two.flac": "two",
		"A - Album/cover.jpg":       "img",
		"../../evil.flac":           "evil", // path traversal attempt
	})
	c, _ := fakeBandcamp(t, z)
	dir := t.TempDir()
	files, err := c.Fetch(context.Background(), Format{"flac", c.base + "/download/album?enc=flac&id=1"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 { // the two tracks and the (neutralised) evil one; the cover is skipped
		t.Fatalf("%v", files)
	}
	for _, f := range files {
		if filepath.Dir(f) != dir {
			t.Errorf("%s escaped %s", f, dir)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.flac")); err == nil {
		t.Fatal("zip-slip: a file was written outside the folder")
	}
}

func TestFetchSingleTrackKeepsItsName(t *testing.T) {
	c, _ := fakeBandcamp(t, nil)
	dir := t.TempDir()
	files, err := c.Fetch(context.Background(), Format{"flac", c.base + "/download/track?enc=flac&id=2"}, dir)
	if err != nil || len(files) != 1 || filepath.Base(files[0]) != "B - Single.flac" {
		t.Fatalf("%v %v", files, err)
	}
}

func TestSignInOnlyGoesToBandcamp(t *testing.T) {
	for host, want := range map[string]bool{
		"bandcamp.com": true, "artist.bandcamp.com": true, "popplers5.bandcamp.com": true,
		"evilbandcamp.com": false, "bandcamp.com.evil.example": false, "example.com": false,
	} {
		if sendsCookie(host) != want {
			t.Errorf("sendsCookie(%q) should be %v", host, want)
		}
	}
	c := &Client{hc: http.DefaultClient, cookie: "identity=abc", base: base}
	if _, err := c.do(context.Background(), "GET", "http://evil.example/x", nil); err == nil {
		t.Fatal("a plain-http foreign URL must be refused outright")
	}
}

func TestNormalize(t *testing.T) {
	if Normalize("https://A.bandcamp.com/album/Al/?from=x#y") != "a.bandcamp.com/album/Al" {
		t.Fatal(Normalize("https://A.bandcamp.com/album/Al/?from=x#y"))
	}
}
