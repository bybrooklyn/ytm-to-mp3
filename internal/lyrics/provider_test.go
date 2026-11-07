package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const bloomLRC = `[ti:Bloom]
[ar:Radiohead]
[00:00.50]
[00:12.34]Open your mouth wide
[00:18.76]A bloom of light
[00:25.100]Like a flower in the sky
[00:31.5]Falling in
[00:38.00][01:20.00]Turning in somersaults
[00:44.20]Let it come
`

func lrclibServer(t *testing.T, calls *atomic.Int32, hits map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		// privacy: only these parameters are ever sent
		for k := range r.URL.Query() {
			switch k {
			case "track_name", "artist_name", "album_name", "duration":
			default:
				t.Errorf("unexpected query parameter %q", k)
			}
		}
		v, ok := hits[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
}

func TestLRCLIBGetThenSearch(t *testing.T) {
	var calls atomic.Int32
	rec := map[string]any{"trackName": "Bloom", "artistName": "Radiohead", "albumName": "The King of Limbs", "duration": 215.4,
		"instrumental": false, "plainLyrics": "Open your mouth wide", "syncedLyrics": bloomLRC}
	srv := lrclibServer(t, &calls, map[string]any{"/api/get": rec})
	defer srv.Close()
	p := &LRCLIB{Base: srv.URL}
	q := Query{Title: "Bloom (Remastered)", Artist: "Radiohead, Thom Yorke", Album: "The King of Limbs", Duration: 215 * time.Second}
	c, err := p.Fetch(context.Background(), q)
	if err != nil || len(c) != 1 || calls.Load() != 1 {
		t.Fatalf("%v %v calls=%d", c, err, calls.Load())
	}
	if len(c[0].Synced) != 8 || c[0].Duration != time.Duration(215.4*float64(time.Second)) || c[0].Provider != "LRCLIB" {
		t.Fatalf("%+v", c[0])
	}
	// no /get answer: falls to the search array
	calls.Store(0)
	srv2 := lrclibServer(t, &calls, map[string]any{"/api/search": []any{
		map[string]any{"trackName": "Bloom", "artistName": "Radiohead", "instrumental": true},
		map[string]any{"trackName": "Bloom", "artistName": "Radiohead", "syncedLyrics": nil, "plainLyrics": nil},
		rec,
	}})
	defer srv2.Close()
	c, err = (&LRCLIB{Base: srv2.URL}).Fetch(context.Background(), q)
	if err != nil || len(c) != 2 || !c[0].Instrumental || calls.Load() != 2 {
		t.Fatalf("%+v %v calls=%d", c, err, calls.Load())
	}
}

func TestLRCLIBOutageAndRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()
	c, err := (&LRCLIB{Base: srv.URL}).Fetch(context.Background(), Query{Title: "Bloom", Artist: "Radiohead"})
	if err != nil || len(c) != 0 || n.Load() != 2 {
		t.Fatalf("one 503 is retried: %v %v n=%d", c, err, n.Load())
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	_, err = (&LRCLIB{Base: down.URL}).Fetch(context.Background(), Query{Title: "Bloom", Artist: "Radiohead"})
	if !IsOutage(err) {
		t.Fatalf("a 500 is an outage, got %v", err)
	}
}

func TestNetEase(t *testing.T) {
	var sawText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search/get":
			sawText = r.URL.Query().Get("s")
			_, _ = w.Write([]byte(`{"result":{"songs":[
				{"id":11,"name":"Bloom","artists":[{"name":"Radiohead"}],"album":{"name":"The King of Limbs"},"duration":215000},
				{"id":12,"name":"Bloom (Live)","artists":[{"name":"Radiohead"}],"duration":230000}]},"code":200}`))
		case "/api/song/lyric":
			if r.URL.Query().Get("id") == "12" {
				_, _ = w.Write([]byte(`{"nolyric":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"lrc":{"lyric":"[00:00.00] 作词 : Thom\n[00:01.00] 作曲 : Thom\n[00:02.00]Open your mouth wide\n[00:05.00]A bloom of light\n[00:08.00]Like a flower\n[00:11.00]Falling in\n[00:13.00]曲 : keep me"},"tlyric":{"lyric":"[00:02.00]x"}}`))
		}
	}))
	defer srv.Close()
	got, err := (&NetEase{Base: srv.URL}).Fetch(context.Background(), Query{Title: "Bloom (Remastered)", Artist: "Radiohead", Album: "secret album", Duration: 215 * time.Second})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if sawText != "Bloom Radiohead" {
		t.Errorf("search text %q: only title and artist may be sent", sawText)
	}
	c := got[0]
	if c.Provider != NetEaseName || len(c.Synced) != 5 || c.Synced[0].Text != "Open your mouth wide" || c.Synced[4].Text != "曲 : keep me" {
		t.Fatalf("credits are dropped from the top only: %+v", c.Synced)
	}
	if !strings.Contains(NetEaseName, "unofficial") {
		t.Error("NetEase must be marked unofficial")
	}
}

type fakeProvider struct {
	name  string
	cands []Candidate
	err   error
	calls int
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Fetch(ctx context.Context, q Query) ([]Candidate, error) {
	f.calls++
	return f.cands, f.err
}

func syncedCand(name string) Candidate {
	c := cand(name, 0, "Bloom", "Radiohead", 215*time.Second)
	c.Synced, _ = ParseLRC(bloomLRC)
	return c
}

func TestLookupCachesAndStopsAtFirstSynced(t *testing.T) {
	dir := t.TempDir()
	first := &fakeProvider{name: "A", cands: []Candidate{syncedCand("A")}}
	second := &fakeProvider{name: "B", cands: []Candidate{syncedCand("B")}}
	f := &Fetcher{Providers: []Provider{first, second}, Cache: &Cache{Dir: dir}}
	q := Query{Title: "Bloom", Artist: "Radiohead", Duration: 215 * time.Second}
	r, err := f.Lookup(context.Background(), q)
	if err != nil || r.Kind != KindSynced || r.Source != "A" || r.Cached || second.calls != 0 {
		t.Fatalf("%+v %v second=%d", r, err, second.calls)
	}
	// the same song again, even from a renamed/upgraded file with a slightly different length: no request
	q2 := Query{Title: "Bloom (Remastered)", Artist: "Radiohead", Duration: 216 * time.Second}
	r, err = f.Lookup(context.Background(), q2)
	if err != nil || !r.Cached || r.Kind != KindSynced || len(r.Synced) != 8 || first.calls != 1 {
		t.Fatalf("%+v %v calls=%d", r, err, first.calls)
	}
	// but a much longer file of the same name is another cut: asked again
	q3 := q
	q3.Duration = 300 * time.Second
	if _, err = f.Lookup(context.Background(), q3); err != nil || first.calls != 2 {
		t.Fatalf("calls=%d %v", first.calls, err)
	}
}

func TestLookupFallsBackToLaterProviderForPlain(t *testing.T) {
	a := &fakeProvider{name: "A"}
	pl := cand("B", 0, "Bloom", "Radiohead", 215*time.Second)
	pl.Plain = plain(12)
	b := &fakeProvider{name: "B", cands: []Candidate{pl}}
	f := &Fetcher{Providers: []Provider{a, b}}
	r, err := f.Lookup(context.Background(), Query{Title: "Bloom", Artist: "Radiohead", Duration: 215 * time.Second})
	if err != nil || r.Kind != KindPlain || r.Source != "B" || a.calls != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestNegativeCacheAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cache := &Cache{Dir: t.TempDir(), Now: func() time.Time { return now }}
	p := &fakeProvider{name: "A"}
	f := &Fetcher{Providers: []Provider{p}, Cache: cache}
	q := Query{Title: "Nobody Sings This", Artist: "Obscure Band", Duration: 100 * time.Second}
	for i := 0; i < 3; i++ {
		r, err := f.Lookup(context.Background(), q)
		if err != nil || r.Found() {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("a song nobody has is asked once, got %d", p.calls)
	}
	now = now.Add(NoneRetry - time.Hour)
	_, _ = f.Lookup(context.Background(), q)
	if p.calls != 1 {
		t.Fatal("still within the retry-after")
	}
	now = now.Add(2 * time.Hour)
	p.cands = []Candidate{syncedCand("A")}
	q2 := Query{Title: "Bloom", Artist: "Radiohead"}
	if _, err := f.Lookup(context.Background(), q2); err != nil {
		t.Fatal(err)
	}
	p.calls = 0
	_, _ = f.Lookup(context.Background(), q)
	if p.calls != 1 {
		t.Fatal("after the retry-after the song is asked again")
	}
}

func TestOutageIsNotCachedAsNoLyrics(t *testing.T) {
	cache := &Cache{Dir: t.TempDir()}
	p := &fakeProvider{name: "A", err: ErrUnavailable}
	f := &Fetcher{Providers: []Provider{p}, Cache: cache}
	q := Query{Title: "Bloom", Artist: "Radiohead"}
	if _, err := f.Lookup(context.Background(), q); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	p.err, p.cands = nil, []Candidate{syncedCand("A")}
	r, err := f.Lookup(context.Background(), q)
	if err != nil || r.Kind != KindSynced || p.calls != 2 {
		t.Fatalf("%+v %v calls=%d", r, err, p.calls)
	}
}

func TestInstrumentalIsCached(t *testing.T) {
	c := cand("A", 0, "Cavern", "Brian Eno", 200*time.Second)
	c.Instrumental = true
	p := &fakeProvider{name: "A", cands: []Candidate{c}}
	f := &Fetcher{Providers: []Provider{p}, Cache: &Cache{Dir: t.TempDir()}}
	q := Query{Title: "Cavern", Artist: "Brian Eno", Duration: 200 * time.Second}
	for i := 0; i < 2; i++ {
		r, err := f.Lookup(context.Background(), q)
		if err != nil || r.Kind != KindInstrumental || r.Found() {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
}

func TestCacheKeyIgnoresDecorationButNotVersions(t *testing.T) {
	a := Key(Query{Title: "Bloom (Remastered 2011)", Artist: "Radiohead - Topic"})
	b := Key(Query{Title: "Bloom", Artist: "Radiohead, Thom Yorke"})
	if a != b {
		t.Error("remaster and extra credited artists share a key")
	}
	if Key(Query{Title: "Bloom (Live)", Artist: "Radiohead"}) == b {
		t.Error("live is another recording")
	}
}
