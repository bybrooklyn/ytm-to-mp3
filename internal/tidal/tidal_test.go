package tidal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/catalog"
)

// fake is a stand-in for Tidal's auth and API hosts.
type fake struct {
	srv      *httptest.Server
	answers  []func(w http.ResponseWriter) // token endpoint answers, in order, for device polling
	polls    atomic.Int32
	refreshN atomic.Int32
	api      http.HandlerFunc
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") == "" {
			http.Error(w, "no client", 400)
			return
		}
		_, _ = w.Write([]byte(`{"deviceCode":"dev1","userCode":"ABCDE","verificationUri":"link.tidal.com","verificationUriComplete":"link.tidal.com/ABCDE","expiresIn":300,"interval":2}`))
	})
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			n := f.refreshN.Add(1)
			if r.Form.Get("refresh_token") != "refresh-old" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"access-new` + string(rune('0'+n)) + `","refresh_token":"refresh-new","expires_in":3600}`))
			return
		}
		i := int(f.polls.Add(1)) - 1
		if i >= len(f.answers) {
			i = len(f.answers) - 1
		}
		f.answers[i](w)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		f.api(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	c := New(filepath.Join(t.TempDir(), "sub", "tidal.json"))
	c.AuthURL, c.APIURL, c.HTTP = f.srv.URL+"/auth", f.srv.URL+"/api", f.srv.Client()
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return f, c
}

func pending(w http.ResponseWriter) {
	w.WriteHeader(400)
	_, _ = w.Write([]byte(`{"status":400,"error":"authorization_pending","sub_status":1002}`))
}

func okToken(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-old","expires_in":3600,"user":{"userId":42,"countryCode":"DE"}}`))
}

func errAnswer(code string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
	}
}

func TestClientIDMatchesPublishedValue(t *testing.T) {
	if got := unpack(idParts); got != "fX2JxdmntZWK0ixT" {
		t.Errorf("client id decodes to %q", got)
	}
	if unpack(secretParts) == "" {
		t.Error("client secret did not decode")
	}
}

func TestDeviceFlowSuccess(t *testing.T) {
	f, c := newFake(t)
	f.answers = []func(http.ResponseWriter){pending, pending, okToken}
	dc, err := c.StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "ABCDE" || dc.URL() != "https://link.tidal.com/ABCDE" || dc.Interval != 2*time.Second {
		t.Errorf("device code = %+v url=%s", dc, dc.URL())
	}
	if err := c.WaitForLogin(context.Background(), dc); err != nil {
		t.Fatal(err)
	}
	if f.polls.Load() != 3 {
		t.Errorf("polled %d times", f.polls.Load())
	}
	if c.Session.UserID != 42 || c.Session.CountryCode != "DE" || c.Country() != "DE" {
		t.Errorf("session = %+v", c.Session)
	}
	// Reloading from disk gives the same login.
	if c2 := New(c.Path); !c2.SignedIn() || c2.Session.RefreshToken != "refresh-old" {
		t.Error("session was not saved")
	}
}

func TestDeviceFlowSlowDownAddsToInterval(t *testing.T) {
	f, c := newFake(t)
	f.answers = []func(http.ResponseWriter){errAnswer("slow_down"), okToken}
	var sleeps []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	dc, _ := c.StartDevice(context.Background())
	if err := c.WaitForLogin(context.Background(), dc); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 2 || sleeps[1] != sleeps[0]+5*time.Second {
		t.Errorf("sleeps = %v", sleeps)
	}
}

func TestDeviceFlowExpiredAndDenied(t *testing.T) {
	for code, want := range map[string]error{"expired_token": ErrExpired, "access_denied": ErrDenied} {
		f, c := newFake(t)
		f.answers = []func(http.ResponseWriter){pending, errAnswer(code)}
		dc, _ := c.StartDevice(context.Background())
		if err := c.WaitForLogin(context.Background(), dc); !errors.Is(err, want) {
			t.Errorf("%s: err = %v", code, err)
		}
		if c.SignedIn() {
			t.Errorf("%s: signed in anyway", code)
		}
	}
}

func TestDeviceFlowTimesOutLocally(t *testing.T) {
	f, c := newFake(t)
	f.answers = []func(http.ResponseWriter){pending}
	dc, _ := c.StartDevice(context.Background())
	now := time.Now()
	c.Now = func() time.Time { now = now.Add(2 * time.Minute); return now }
	if err := c.WaitForLogin(context.Background(), dc); !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v", err)
	}
}

func signedIn(c *Client, expires time.Time) {
	c.Session = &Session{AccessToken: "access-old", RefreshToken: "refresh-old", ExpiresAt: expires, UserID: 42, CountryCode: "DE"}
}

func TestRefreshWhenExpiredRotatesAndSaves(t *testing.T) {
	f, c := newFake(t)
	signedIn(c, time.Now().Add(-time.Hour))
	var auth string
	f.api = func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Query().Get("countryCode") != "DE" {
			t.Errorf("countryCode = %q", r.URL.Query().Get("countryCode"))
		}
		_, _ = w.Write([]byte(`{"countryCode":"DE"}`))
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer access-new1" || c.Session.RefreshToken != "refresh-new" || c.Session.UserID != 42 {
		t.Errorf("auth=%q session=%+v", auth, c.Session)
	}
	b, _ := os.ReadFile(c.Path)
	if !strings.Contains(string(b), "refresh-new") {
		t.Error("rotated refresh token not saved")
	}
}

func TestRefreshOn401ThenSignedOut(t *testing.T) {
	f, c := newFake(t)
	signedIn(c, time.Now().Add(time.Hour))
	calls := 0
	f.api = func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer access-old" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}
	if err := c.Verify(context.Background()); err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	// A refresh token Tidal rejects means signed out.
	c.Session.RefreshToken, c.Session.ExpiresAt = "stale", time.Time{}
	if err := c.Verify(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Errorf("err = %v", err)
	}
}

func TestNotSignedIn(t *testing.T) {
	_, c := newFake(t)
	if err := c.Verify(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Errorf("err = %v", err)
	}
}

func TestSessionFilePermissionsAndSignOut(t *testing.T) {
	_, c := newFake(t)
	signedIn(c, time.Now().Add(time.Hour))
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{c.Path, filepath.Dir(c.Path)} {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0o600)
			if fi.IsDir() {
				want = 0o700
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s mode = %o, want %o", p, fi.Mode().Perm(), want)
			}
		}
		// A looser pre-existing file is tightened on the next save.
		_ = os.Chmod(c.Path, 0o644)
		_ = c.save()
		if fi, _ := os.Stat(c.Path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode after re-save = %o", fi.Mode().Perm())
		}
	}
	if err := c.SignOut(); err != nil || c.SignedIn() {
		t.Fatalf("signout: %v", err)
	}
	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Error("session file still there")
	}
}

const trackJSON = `{"id":111,"title":"Song","version":"Live","duration":200,"trackNumber":3,"volumeNumber":2,
 "isrc":"usabc1900001","explicit":true,"copyright":"(c) X","allowStreaming":true,
 "artist":{"name":"Main","type":"MAIN"},"artists":[{"name":"Main","type":"MAIN"},{"name":"Feat","type":"FEATURED"}],
 "audioModes":["STEREO"],
 "album":{"id":9,"title":"The Album","cover":"aa-bb-cc"}}`

const albumJSON = `{"id":9,"title":"The Album","cover":"aa-bb-cc","releaseDate":"2019-05-03","numberOfTracks":12,"numberOfVolumes":2,
 "artist":{"name":"Album Artist","type":"MAIN"},"artists":[{"name":"Album Artist","type":"MAIN"}]}`

func apiMux(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api") {
		case "/tracks/111":
			_, _ = w.Write([]byte(trackJSON))
		case "/albums/9":
			_, _ = w.Write([]byte(albumJSON))
		case "/albums/9/items":
			_, _ = w.Write([]byte(`{"totalNumberOfItems":2,"items":[{"type":"track","item":` + trackJSON + `},{"type":"video","item":{"id":5}}]}`))
		case "/playlists/uuid-1/items":
			if r.URL.Query().Get("offset") == "0" {
				_, _ = w.Write([]byte(`{"totalNumberOfItems":2,"items":[{"type":"track","item":` + trackJSON + `}]}`))
			} else {
				_, _ = w.Write([]byte(`{"totalNumberOfItems":2,"items":[{"type":"track","item":{"id":112,"title":"Two","artist":{"name":"B"},"album":{"id":9,"title":"The Album"}}}]}`))
			}
		case "/search/tracks":
			_, _ = w.Write([]byte(`{"items":[` + trackJSON + `,{"id":222,"title":"Other","artist":{"name":"Z"},"album":{"id":1}}]}`))
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}
}

func TestTrackMapping(t *testing.T) {
	f, c := newFake(t)
	signedIn(c, time.Now().Add(time.Hour))
	f.api = apiMux(t)
	tr, err := c.Track(context.Background(), 111)
	if err != nil {
		t.Fatal(err)
	}
	ct := tr.Catalog()
	if ct.Title != "Song (Live)" || ct.ISRC != "USABC1900001" || ct.TrackNo != 3 || ct.DiscNo != 2 || ct.DiscTotal != 2 ||
		ct.TrackTotal != 12 || ct.Album != "The Album" || ct.AlbumArtist != "Album Artist" || ct.ReleaseDate != "2019-05-03" ||
		ct.DurationMillis != 200000 || ct.Origin != catalog.Tidal || ct.OriginID != "111" {
		t.Errorf("catalog = %+v", ct)
	}
	if len(ct.Artists) != 1 || ct.Artists[0] != "Main" {
		t.Errorf("artists = %v (featured artists are not the main credit)", ct.Artists)
	}
	if !tr.Explicit || tr.Atmos {
		t.Errorf("explicit=%v atmos=%v", tr.Explicit, tr.Atmos)
	}
	if ct.ArtworkURL != "https://resources.tidal.com/images/aa/bb/cc/1280x1280.jpg" {
		t.Errorf("cover = %s", ct.ArtworkURL)
	}
}

func TestAlbumPlaylistSearchAndLinks(t *testing.T) {
	f, c := newFake(t)
	signedIn(c, time.Now().Add(time.Hour))
	f.api = apiMux(t)
	ctx := context.Background()
	al, err := c.ResolveURL(ctx, "https://tidal.com/browse/album/9", false)
	if err != nil || len(al) != 1 || al[0].ReleaseDate != "2019-05-03" {
		t.Fatalf("album: %v %+v", err, al)
	}
	one, err := c.ResolveURL(ctx, "https://tidal.com/album/9/track/111", false)
	if err != nil || len(one) != 1 || one[0].ID != 111 {
		t.Fatalf("album track: %v %+v", err, one)
	}
	pl, err := c.ResolveURL(ctx, "https://tidal.com/playlist/uuid-1", false)
	if err != nil || len(pl) != 2 || pl[1].Title != "Two" {
		t.Fatalf("playlist: %v %+v", err, pl)
	}
	whole, err := c.ResolveURL(ctx, "https://listen.tidal.com/track/111", true)
	if err != nil || len(whole) != 1 {
		t.Fatalf("whole album: %v %+v", err, whole)
	}
	m, ok, err := c.Match(ctx, catalog.Track{Title: "song live", Artists: []string{"MAIN"}, ISRC: "USABC1900001"})
	if err != nil || !ok || m.ID != 111 {
		t.Fatalf("match: %v %v %+v", err, ok, m)
	}
	if _, ok, _ := c.Match(ctx, catalog.Track{Title: "Nothing", Artists: []string{"Nobody"}}); ok {
		t.Error("matched a song that isn't there")
	}
}

func TestParseURL(t *testing.T) {
	good := map[string]Link{
		"https://tidal.com/track/111":                      {Kind: "track", ID: "111"},
		"https://tidal.com/browse/track/111":               {Kind: "track", ID: "111"},
		"https://listen.tidal.com/album/9/track/111":       {Kind: "album", ID: "9", Track: 111},
		"https://www.tidal.com/playlist/ab-12-cd":          {Kind: "playlist", ID: "ab-12-cd"},
		" https://tidal.com/browse/album/9/ ":              {Kind: "album", ID: "9"},
		"https://tidal.com/browse/playlist/ab-12-cd?u=1#x": {Kind: "playlist", ID: "ab-12-cd"},
	}
	for in, want := range good {
		if got, ok := ParseURL(in); !ok || got != want {
			t.Errorf("%q = %+v %v", in, got, ok)
		}
	}
	for _, in := range []string{"https://tidal.com/artist/5", "https://tidal.com/track/abc", "https://example.com/track/1", "https://tidal.com.evil.test/track/1", "tidal", ""} {
		if IsURL(in) {
			t.Errorf("%q should not be a Tidal link", in)
		}
	}
}

func TestPickPrefersStereoExplicitAndISRC(t *testing.T) {
	cands := []Track{
		{ID: 1, Title: "S", Artists: []string{"A"}, Atmos: true},
		{ID: 2, Title: "S", Artists: []string{"A"}},
		{ID: 3, Title: "S", Artists: []string{"A"}, Explicit: true},
		{ID: 4, Title: "Different", Artists: []string{"A"}, ISRC: "X1"},
	}
	if got, ok := Pick(catalog.Track{Title: "S", Artists: []string{"A"}}, cands); !ok || got.ID != 3 {
		t.Errorf("picked %+v", got)
	}
	if got, ok := Pick(catalog.Track{Title: "whatever", ISRC: "x1"}, cands); !ok || got.ID != 4 {
		t.Errorf("isrc pick %+v", got)
	}
	// Same name but the length is off by a minute: a different recording.
	if _, ok := Pick(catalog.Track{Title: "S", Artists: []string{"A"}, DurationMillis: 300000}, []Track{{ID: 9, Title: "S", Artists: []string{"A"}, DurationSec: 240}}); ok {
		t.Error("matched a different-length song")
	}
}

func btsFor(m map[string]any) string {
	b, _ := json.Marshal(m)
	return base64.StdEncoding.EncodeToString(b)
}

func TestParseManifest(t *testing.T) {
	const bts = "application/vnd.tidal.bts"
	s, err := ParseManifest(bts, btsFor(map[string]any{"mimeType": "audio/flac", "codecs": "flac", "encryptionType": "NONE", "urls": []string{"https://cdn.test/a.flac"}}))
	if err != nil || s.URL != "https://cdn.test/a.flac" {
		t.Fatalf("plain: %v %+v", err, s)
	}
	_, err = ParseManifest(bts, btsFor(map[string]any{"mimeType": "audio/flac", "codecs": "flac", "encryptionType": "OLD_AES", "keyId": "k", "urls": []string{"https://cdn.test/a"}}))
	if !errors.Is(err, ErrEncrypted) {
		t.Errorf("encrypted: %v", err)
	}
	_, err = ParseManifest(bts, btsFor(map[string]any{"mimeType": "audio/mp4", "codecs": "mp4a.40.2", "encryptionType": "NONE", "urls": []string{"https://cdn.test/a.m4a"}}))
	if !errors.Is(err, ErrLossy) {
		t.Errorf("lossy: %v", err)
	}
	if _, err = ParseManifest("application/dash+xml", base64.StdEncoding.EncodeToString([]byte("<MPD/>"))); !errors.Is(err, ErrDASH) {
		t.Errorf("dash: %v", err)
	}
	if _, err = ParseManifest(bts, "!!!"); err == nil {
		t.Error("garbage accepted")
	}
	if _, err = ParseManifest(bts, btsFor(map[string]any{"codecs": "flac", "urls": []string{}})); err == nil {
		t.Error("manifest with no url accepted")
	}
}

func TestStreamTiersAndFallback(t *testing.T) {
	f, c := newFake(t)
	signedIn(c, time.Now().Add(time.Hour))
	bts := func(enc string) string {
		return btsFor(map[string]any{"mimeType": "audio/flac", "codecs": "flac", "encryptionType": enc, "urls": []string{"https://cdn.test/x.flac"}})
	}
	var asked []string
	var hiRes func(w http.ResponseWriter)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("audioquality")
		asked = append(asked, q)
		if q == "HI_RES_LOSSLESS" {
			hiRes(w)
			return
		}
		_, _ = w.Write([]byte(`{"audioQuality":"LOSSLESS","manifestMimeType":"application/vnd.tidal.bts","manifest":"` + bts("NONE") + `"}`))
	}
	hiRes = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"audioQuality":"HI_RES_LOSSLESS","manifestMimeType":"application/dash+xml","manifest":"PE1QRC8+"}`))
	}
	ctx := context.Background()
	// Best: the hi-res tier is DASH, so it is skipped for plain lossless.
	s, err := c.Stream(ctx, 111, Best)
	if err != nil || s.Quality != "LOSSLESS" || s.Label() != "FLAC 16-bit/44.1 kHz" || strings.Join(asked, ",") != "HI_RES_LOSSLESS,LOSSLESS" {
		t.Fatalf("best: %v %+v %v", err, s, asked)
	}
	// Best, with hi-res served as plain FLAC: taken.
	hiRes = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"audioQuality":"HI_RES_LOSSLESS","manifestMimeType":"application/vnd.tidal.bts","manifest":"` + bts("NONE") + `"}`))
	}
	if s, err = c.Stream(ctx, 111, Best); err != nil || s.Label() != "FLAC hi-res" {
		t.Fatalf("hi-res: %v %+v", err, s)
	}
	// Lossless only asks for LOSSLESS.
	asked = nil
	if _, err = c.Stream(ctx, 111, Lossless); err != nil || strings.Join(asked, ",") != "LOSSLESS" {
		t.Fatalf("lossless: %v %v", err, asked)
	}
	// Everything encrypted: refused with the reason, nothing fetched.
	f.api = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"audioQuality":"LOSSLESS","manifestMimeType":"application/vnd.tidal.bts","manifest":"` + bts("AES") + `"}`))
	}
	_, err = c.Stream(ctx, 111, Lossless)
	var ns *NoStreamError
	if !errors.As(err, &ns) || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("encrypted tier: %v", err)
	}
	// A subscription without lossless serves HIGH.
	f.api = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"audioQuality":"HIGH","manifestMimeType":"application/vnd.tidal.bts","manifest":"` + bts("NONE") + `"}`))
	}
	if _, err = c.Stream(ctx, 111, Lossless); !errors.As(err, &ns) || !strings.Contains(err.Error(), "HIGH") {
		t.Errorf("lower tier: %v", err)
	}
}

func TestDownload(t *testing.T) {
	body := append([]byte("fLaC"), make([]byte, 300<<10)...)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the CDN request carried credentials")
		}
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write(body)
		case "/html":
			_, _ = w.Write([]byte("<html>nope"))
		case "/short":
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write(append([]byte("fLaC"), 1, 2, 3))
		default:
			w.WriteHeader(403)
		}
	}))
	defer cdn.Close()
	fetchBackoff = time.Millisecond
	_, c := newFake(t)
	signedIn(c, time.Now().Add(time.Hour))
	dir := t.TempDir()
	var last int64
	dst := filepath.Join(dir, "a.flac")
	if err := c.Download(context.Background(), Stream{URL: cdn.URL + "/ok"}, dst, func(d, _ int64) { last = d }); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dst); fi == nil || fi.Size() != int64(len(body)) || last != int64(len(body)) {
		t.Errorf("size/progress wrong: %v %d", fi, last)
	}
	for _, p := range []string{"/html", "/short", "/forbidden"} {
		d := filepath.Join(dir, "bad"+strings.ReplaceAll(p, "/", "_"))
		if err := c.Download(context.Background(), Stream{URL: cdn.URL + p}, d, nil); err == nil {
			t.Errorf("%s: no error", p)
		}
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s: partial file left behind", p)
		}
	}
	// Cancelled midway: no file.
	ctx, cancel := context.WithCancel(context.Background())
	d := filepath.Join(dir, "cancel.flac")
	err := c.Download(ctx, Stream{URL: cdn.URL + "/ok"}, d, func(int64, int64) { cancel() })
	if err == nil {
		t.Error("cancelled download reported success")
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Error("cancelled download left a file at the destination")
	}
}
