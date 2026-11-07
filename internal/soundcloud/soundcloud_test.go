package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const idA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const idB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

// fake is a stand-in for soundcloud.com, its web app script and api-v2.
type fake struct {
	t        *testing.T
	srv      *httptest.Server
	validID  atomic.Value // the client_id the API accepts, and the script shows
	siteHits atomic.Int32
	throttle atomic.Int32 // answer this many API calls with 429 first
	apiUA    atomic.Value
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{t: t, routes: map[string]func(http.ResponseWriter, *http.Request){}}
	f.validID.Store(idA)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	c := New(f.srv.Client())
	c.API = f.srv.URL
	c.Site = f.srv.URL + "/"
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return f, c
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		f.siteHits.Add(1)
		fmt.Fprintf(w, `<html><script crossorigin src="%s/other.js"></script><script crossorigin src="%s/app.js"></script>`, f.srv.URL, f.srv.URL)
		return
	case "/other.js":
		io.WriteString(w, `var x={client_id:"short"}`)
		return
	case "/app.js":
		fmt.Fprintf(w, `a(); e={env:1,client_id:"%s",x:2}`, f.validID.Load())
		return
	}
	f.apiUA.Store(r.UserAgent())
	if f.throttle.Add(-1) >= 0 {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if r.URL.Path != "/audio/seg" && r.URL.Path != "/audio/file" && !strings.HasPrefix(r.URL.Path, "/hls/") && r.URL.Query().Get("client_id") != f.validID.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	h := f.routes[r.URL.Path]
	if h == nil {
		f.t.Errorf("unexpected request %s", r.URL)
		w.WriteHeader(404)
		return
	}
	h(w, r)
}

func (f *fake) json(path string, v any) {
	f.routes[path] = func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(v) }
}

type m = map[string]any

func trackJSON(id int, title string, extra m) m {
	t := m{"id": id, "kind": "track", "title": title, "permalink_url": fmt.Sprintf("https://soundcloud.com/a/t%d", id),
		"duration": 213886, "genre": "Electronic", "sharing": "public", "created_at": "2007-09-22T14:45:46Z",
		"artwork_url": "https://i1.sndcdn.com/artworks-x-large.jpg", "downloadable": true, "policy": "MONETIZE",
		"user": m{"username": "Uploader"}, "track_authorization": "AUTH"}
	for k, v := range extra {
		t[k] = v
	}
	return t
}

func TestResolveTrackMapping(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", trackJSON(293, "Flickermood", m{
		"release_date":       "2003-06-02T00:00:00Z",
		"publisher_metadata": m{"artist": "Forss", "album_title": "Soulhack", "isrc": "dep960300042"},
	}))
	ts, err := c.Resolve(context.Background(), "https://soundcloud.com/forss/flickermood")
	if err != nil || len(ts) != 1 {
		t.Fatal(ts, err)
	}
	got := ts[0]
	want := Track{ID: 293, URL: "https://soundcloud.com/a/t293", Title: "Flickermood", Artist: "Forss", Album: "Soulhack",
		AlbumArtist: "Forss", Genre: "Electronic", Date: "2003-06-02", ISRC: "DEP960300042",
		Cover: "https://i1.sndcdn.com/artworks-x-t500x500.jpg", Duration: 213.886, Downloadable: true, Policy: "MONETIZE"}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if ua := f.apiUA.Load(); ua != "mfetchy" {
		t.Errorf("User-Agent %v, want mfetchy", ua)
	}
}

func TestMappingFallbacksAndPrivate(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", trackJSON(1, "Song", nil))
	ts, err := c.Resolve(context.Background(), "x")
	if err != nil || ts[0].Artist != "Uploader" || ts[0].Album != "" || ts[0].Date != "2007-09-22" {
		t.Fatal(ts, err)
	}
	f.json("/resolve", trackJSON(2, "Secret", m{"sharing": "private"}))
	if _, err := c.Resolve(context.Background(), "x"); err == nil {
		t.Error("a private track should not resolve")
	}
}

func TestPlaylistWithStubs(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", m{"kind": "playlist", "title": "Soulhack", "is_album": true, "user": m{"username": "Forss"},
		"tracks": []m{trackJSON(1, "One", nil), {"id": 2}, {"id": 3}}})
	var asked string
	f.routes["/tracks"] = func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("ids")
		json.NewEncoder(w).Encode([]m{trackJSON(3, "Three", nil), trackJSON(2, "Two", nil)}) // out of order
	}
	ts, err := c.Resolve(context.Background(), "x")
	if err != nil || len(ts) != 3 {
		t.Fatal(ts, err)
	}
	if asked != "2,3" {
		t.Errorf("batch ids %q", asked)
	}
	for i, w := range []string{"One", "Two", "Three"} {
		g := ts[i]
		if g.Title != w || g.Album != "Soulhack" || g.AlbumArtist != "Forss" || g.TrackNo != i+1 || g.TrackTotal != 3 {
			t.Errorf("track %d: %+v", i, g)
		}
	}
}

func TestPlaylistNotAnAlbum(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", m{"kind": "playlist", "title": "My favourites", "tracks": []m{trackJSON(1, "One", nil)}})
	ts, err := c.Resolve(context.Background(), "x")
	if err != nil || len(ts) != 1 || ts[0].Album != "" || ts[0].TrackNo != 0 {
		t.Fatal(ts, err)
	}
}

func TestUserPagination(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", m{"kind": "user", "id": 183})
	f.routes["/users/183/tracks"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "" {
			json.NewEncoder(w).Encode(m{"collection": []m{trackJSON(1, "A", nil), trackJSON(2, "B", nil)},
				"next_href": f.srv.URL + "/users/183/tracks?offset=2&limit=50"}) // no client_id in it
			return
		}
		json.NewEncoder(w).Encode(m{"collection": []m{trackJSON(3, "C", nil)}, "next_href": nil})
	}
	ts, err := c.Resolve(context.Background(), "x")
	if err != nil || len(ts) != 3 || ts[2].Title != "C" {
		t.Fatal(ts, err)
	}
}

func TestClientIDDiscoveryAndRefresh(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", trackJSON(1, "A", nil))
	for i := 0; i < 3; i++ {
		if _, err := c.Resolve(context.Background(), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.siteHits.Load(); n != 1 {
		t.Errorf("client_id discovered %d times for 3 calls, want once (cached)", n)
	}
	// The id rotates: the next call gets 401, discovers the new one, and succeeds.
	f.validID.Store(idB)
	if _, err := c.Resolve(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if n := f.siteHits.Load(); n != 2 {
		t.Errorf("site hits %d, want 2 after rotation", n)
	}
	if c.clientID != idB {
		t.Errorf("client_id %q", c.clientID)
	}
}

func TestRefusedAfterRefreshIsAnError(t *testing.T) {
	f, c := newFake(t)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".js") {
			f.serve(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden) // e.g. a bot check
	})
	if _, err := c.Resolve(context.Background(), "x"); err == nil {
		t.Fatal("want error")
	}
}

func TestRateLimitRetry(t *testing.T) {
	f, c := newFake(t)
	f.json("/resolve", trackJSON(1, "A", nil))
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	f.throttle.Store(2)
	if _, err := c.Resolve(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != time.Second {
		t.Errorf("slept %v (Retry-After: 1)", slept)
	}
	// Forever throttled: gives up with an error rather than hanging.
	f.throttle.Store(1000)
	if _, err := c.Resolve(context.Background(), "x"); err == nil {
		t.Error("want an error when always throttled")
	}
}

func TestPickTranscoding(t *testing.T) {
	mk := func(preset, proto, mime string) transcoding {
		tc := transcoding{URL: "u/" + preset, Preset: preset}
		tc.Format.Protocol, tc.Format.MimeType = proto, mime
		return tc
	}
	prog := mk("mp3_0_1", "progressive", "audio/mpeg")
	hlsMP3 := mk("mp3_0_1", "hls", "audio/mpeg")
	aac160 := mk("aac_160k", "hls", `audio/mp4; codecs="mp4a.40.2"`)
	aac96 := mk("aac_96k", "hls", `audio/mp4; codecs="mp4a.40.2"`)
	opus := mk("opus_0_0", "hls", `audio/ogg; codecs="opus"`)
	abr := mk("abr_sq", "hls", "audio/mpegurl")

	if c, err := pick([]transcoding{aac160, hlsMP3, prog, opus}); err != nil || c.t.Preset != "mp3_0_1" || c.t.Format.Protocol != "progressive" || c.ext != ".mp3" {
		t.Errorf("progressive not preferred: %+v %v", c, err)
	}
	if c, err := pick([]transcoding{abr, opus, aac96, aac160, hlsMP3}); err != nil || c.ext != ".mp3" || c.t.Format.Protocol != "hls" {
		t.Errorf("hls mp3 should beat aac: %+v %v", c, err)
	}
	if c, err := pick([]transcoding{abr, aac96, aac160}); err != nil || c.t.Preset != "aac_160k" || c.ext != ".m4a" {
		t.Errorf("want aac_160k: %+v %v", c, err)
	}
	if _, err := pick([]transcoding{abr, opus}); !errors.Is(err, ErrNoStream) {
		t.Errorf("nothing usable: %v", err)
	}
	if _, err := pick(nil); !errors.Is(err, ErrNoStream) {
		t.Errorf("empty: %v", err)
	}
}

// audioFake wires /resolve, the transcoding lookup and the audio itself.
func (f *fake) audioTrack(transcodings []m) {
	tr := trackJSON(9, "Song", m{"media": m{"transcodings": transcodings}})
	f.json("/resolve", tr)
}

func tc(f *fake, name, proto, mime, preset string) m {
	return m{"url": f.srv.URL + "/media/" + name, "preset": preset, "format": m{"protocol": proto, "mime_type": mime}}
}

func TestFetchProgressive(t *testing.T) {
	f, c := newFake(t)
	f.audioTrack([]m{tc(f, "p", "progressive", "audio/mpeg", "mp3_0_1")})
	f.routes["/media/p"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("track_authorization") != "AUTH" {
			t.Error("track_authorization not sent")
		}
		json.NewEncoder(w).Encode(m{"url": f.srv.URL + "/audio/file"})
	}
	f.routes["/audio/file"] = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ID3-audio-bytes") }
	var last, total int64
	path, err := c.Fetch(context.Background(), Track{URL: "https://soundcloud.com/a/t9"}, t.TempDir()+"/x", func(g, tt int64) { last, total = g, tt })
	if err != nil || !strings.HasSuffix(path, "x.mp3") {
		t.Fatal(path, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "ID3-audio-bytes" || last != int64(len(b)) || total != last {
		t.Errorf("file %q progress %d/%d", b, last, total)
	}
}

func TestFetchHLSConcatenates(t *testing.T) {
	f, c := newFake(t)
	f.audioTrack([]m{tc(f, "h", "hls", "audio/mpeg", "mp3_0_1")})
	f.routes["/media/h"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(m{"url": f.srv.URL + "/hls/list.m3u8"})
	}
	f.routes["/hls/list.m3u8"] = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:10,\nseg0\n#EXTINF:10,\n%s/hls/seg1\n#EXTINF:5,\nseg2\n#EXT-X-ENDLIST\n", f.srv.URL)
	}
	for i, s := range []string{"AAA", "BBB", "CC"} {
		s := s
		f.routes[fmt.Sprintf("/hls/seg%d", i)] = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, s) }
	}
	path, err := c.Fetch(context.Background(), Track{URL: "u"}, t.TempDir()+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "AAABBBCC" || !strings.HasSuffix(path, ".mp3") {
		t.Errorf("%s: %q", path, b)
	}
}

func TestFetchHLSFragmentedMP4InitFirst(t *testing.T) {
	f, c := newFake(t)
	f.audioTrack([]m{tc(f, "h", "hls", `audio/mp4; codecs="mp4a.40.2"`, "aac_160k")})
	f.routes["/media/h"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(m{"url": f.srv.URL + "/hls/list.m3u8"})
	}
	f.routes["/hls/list.m3u8"] = func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:10\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:10,\nd0.m4s\n#EXTINF:10,\nd1.m4s\n#EXT-X-ENDLIST\n")
	}
	f.routes["/hls/init.mp4"] = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "INIT") }
	f.routes["/hls/d0.m4s"] = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "-0") }
	f.routes["/hls/d1.m4s"] = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "-1") }
	path, err := c.Fetch(context.Background(), Track{URL: "u"}, t.TempDir()+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "INIT-0-1" || !strings.HasSuffix(path, ".m4a") {
		t.Errorf("%s: %q", path, b)
	}
}

func TestFetchNothingUsableAndEncrypted(t *testing.T) {
	f, c := newFake(t)
	f.audioTrack([]m{tc(f, "o", "hls", `audio/ogg; codecs="opus"`, "opus_0_0")})
	dir := t.TempDir()
	if _, err := c.Fetch(context.Background(), Track{URL: "u"}, dir+"/x", nil); !errors.Is(err, ErrNoStream) {
		t.Errorf("opus hls: %v", err)
	}
	f.audioTrack([]m{tc(f, "h", "hls", "audio/mpeg", "mp3_0_1")})
	f.routes["/media/h"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(m{"url": f.srv.URL + "/hls/list.m3u8"})
	}
	f.routes["/hls/list.m3u8"] = func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:10,\ns0\n#EXT-X-ENDLIST\n")
	}
	if _, err := c.Fetch(context.Background(), Track{URL: "u"}, dir+"/y", nil); !errors.Is(err, ErrNoStream) {
		t.Errorf("encrypted: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("leftover files: %v", ents)
	}
}
