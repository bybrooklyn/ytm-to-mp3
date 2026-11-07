package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tidal"
)

func TestTidalLinkRouting(t *testing.T) {
	for link, song := range map[string]bool{
		"https://tidal.com/browse/track/111":         true,
		"https://listen.tidal.com/album/9/track/111": true,
		"https://tidal.com/album/9":                  false,
		"https://tidal.com/browse/playlist/abc-def":  false,
	} {
		if !tidal.IsURL(link) {
			t.Errorf("%s not recognised as Tidal", link)
		}
		if isSongLink(link) != song {
			t.Errorf("isSongLink(%s) = %v, want %v", link, !song, song)
		}
	}
	if tidal.IsURL("https://music.apple.com/us/album/x/1") || tidal.IsURL("https://open.spotify.com/track/1") {
		t.Error("another service's link was taken for Tidal")
	}
}

func TestSourceSummaryMentionsTidal(t *testing.T) {
	if got := sourceSummary(2, 1, 3, 4, 1); got != "2 from Tidal, 1 from Apple Music, 3 from YouTube, 4 already held (1 to upgrade)" {
		t.Errorf("summary = %q", got)
	}
}

// The rank of a held song comes from its source's name, not from the number stored with it:
// databases written before Tidal existed hold numbers from the old scale.
func TestPlanUsesSourceNameNotStoredNumber(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, ".ytm"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	file := func(n string) string {
		p := filepath.Join(root, n)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		return p
	}
	mk := func(title string) item {
		ct := catalog.Track{Title: title, Artists: []string{"A"}}
		return item{t: ct, key: ct.Key(), target: catalog.Tidal}
	}
	apple, bc := mk("From Apple"), mk("From Bandcamp")
	// Stale numbers: Apple stored as 99, a Bandcamp purchase as the old 4.
	_ = st.LibraryPut(apple.key, "From Apple", "A", string(catalog.Apple), 99, file("a.m4a"))
	_ = st.LibraryPut(bc.key, "From Bandcamp", "A", string(catalog.BandcampPurchase), 4, file("b.flac"))
	appleItems, ytItems, held, upgrades := planUnified(st, []item{apple, bc}, true)
	if held != 1 || upgrades != 1 || len(appleItems) != 0 || len(ytItems) != 1 || ytItems[0].t.Title != "From Apple" {
		t.Fatalf("held=%d upgrades=%d apple=%d other=%d", held, upgrades, len(appleItems), len(ytItems))
	}
	if ytItems[0].heldRank != catalog.Rank(catalog.Apple) {
		t.Errorf("heldRank = %d", ytItems[0].heldRank)
	}
}

func TestTidalPath(t *testing.T) {
	got := tidalPath("/m", tidal.Track{Title: "Song", Artists: []string{"Main", "Feat"}, Album: "Alb", AlbumArtist: "Main", TrackNo: 3, TrackTotal: 12})
	if want := filepath.Join("/m", "Main", "Alb", "03 - Song.flac"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func workingFFmpeg() (ffmpeg, ffprobe string) {
	for _, dir := range []string{"/usr/bin", ""} {
		f, p := "ffmpeg", "ffprobe"
		if dir != "" {
			f, p = filepath.Join(dir, f), filepath.Join(dir, p)
		}
		fp, err1 := exec.LookPath(f)
		pp, err2 := exec.LookPath(p)
		if err1 == nil && err2 == nil && exec.Command(fp, "-version").Run() == nil && exec.Command(pp, "-version").Run() == nil {
			return fp, pp
		}
	}
	return "", ""
}

// A whole save against fake Tidal and CDN servers: stream resolved, FLAC fetched, tagged
// through ffmpeg, filed as Artist/Album/NN - Title.flac and recorded at Tidal's rank.
func TestTidalSaveEndToEnd(t *testing.T) {
	ffmpeg, ffprobe := workingFFmpeg()
	if ffmpeg == "" {
		t.Skip("no working ffmpeg/ffprobe")
	}
	work := t.TempDir()
	src := filepath.Join(work, "src.flac")
	if err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=d=1", "-c:a", "flac", src).Run(); err != nil {
		t.Skipf("ffmpeg can't make a FLAC here: %v", err)
	}
	audio, _ := os.ReadFile(src)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/playbackinfopostpaywall"):
			m, _ := json.Marshal(map[string]any{"mimeType": "audio/flac", "codecs": "flac", "encryptionType": "NONE", "urls": []string{srv.URL + "/cdn/a.flac"}})
			_ = json.NewEncoder(w).Encode(map[string]string{"audioQuality": "LOSSLESS", "manifestMimeType": "application/vnd.tidal.bts", "manifest": base64.StdEncoding.EncodeToString(m)})
		case r.URL.Path == "/cdn/a.flac":
			_, _ = w.Write(audio)
		case r.URL.Path == "/api/albums/9":
			_, _ = w.Write([]byte(`{"id":9,"title":"Alb","releaseDate":"2020-02-02","numberOfTracks":10,"artist":{"name":"Main"}}`))
		default:
			http.NotFound(w, r) // the cover art: the song is tagged without it
		}
	}))
	defer srv.Close()
	c := tidal.New("")
	c.APIURL, c.AuthURL, c.HTTP = srv.URL+"/api", srv.URL+"/auth", srv.Client()
	c.Session = &tidal.Session{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), CountryCode: "US"}

	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, ".ytm"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tt := tidal.Track{ID: 111, Title: "Song", Artists: []string{"Main"}, Album: "Alb", AlbumID: 9, TrackNo: 3, ISRC: "USABC1900001"}
	ct := catalog.Track{Title: "Song", Artists: []string{"Main"}}
	it := &item{t: ct, key: ct.Key(), target: catalog.Tidal, tidalTrack: tt}

	label, err := tidalSave(context.Background(), c, st, ffmpeg, ffprobe, root, it, tidal.Lossless, nil)
	if err != nil {
		t.Fatal(err)
	}
	if label != "FLAC 16-bit/44.1 kHz" {
		t.Errorf("label = %q", label)
	}
	dest := filepath.Join(root, "Main", "Alb", "03 - Song.flac")
	if !fileExists(dest) {
		t.Fatalf("%s missing", dest)
	}
	if lib := st.Library(it.key); !lib.Done || lib.Source != "tidal" || lib.Path != dest || lib.Quality != catalog.Rank(catalog.Tidal) {
		t.Errorf("library row = %+v", lib)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "Main", "Alb", ".ytm-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
	// The tags made it in.
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format_tags", "-of", "json", dest).Output()
	if err != nil || !strings.Contains(strings.ToLower(string(out)), "usabc1900001") || !strings.Contains(string(out), "2020-02-02") {
		t.Errorf("tags = %s (%v)", out, err)
	}
	// A second run finds it already there.
	if _, err := tidalSave(context.Background(), c, st, ffmpeg, ffprobe, root, it, tidal.Lossless, nil); err != errTidalExists {
		t.Errorf("second save: %v", err)
	}
}
