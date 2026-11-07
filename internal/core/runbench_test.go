package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/verify"
)

// fakeTidal serves N songs; every audio file takes cdnDelay to arrive.
func fakeTidal(t *testing.T, audio []byte, cdnDelay time.Duration) (*tidal.Client, *httptest.Server) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/playbackinfopostpaywall"):
			m, _ := json.Marshal(map[string]any{"mimeType": "audio/flac", "codecs": "flac", "encryptionType": "NONE",
				"urls": []string{srv.URL + "/cdn/a.flac"}})
			_ = json.NewEncoder(w).Encode(map[string]string{"audioQuality": "LOSSLESS", "manifestMimeType": "application/vnd.tidal.bts",
				"manifest": base64.StdEncoding.EncodeToString(m)})
		case strings.HasPrefix(r.URL.Path, "/cdn/"):
			time.Sleep(cdnDelay)
			http.ServeContent(w, r, "a.flac", time.Time{}, strings.NewReader(string(audio)))
		case strings.HasPrefix(r.URL.Path, "/api/albums/"):
			_, _ = w.Write([]byte(`{"id":9,"title":"Alb","releaseDate":"2020-02-02","numberOfTracks":10,"artist":{"name":"Main"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := tidal.New("")
	c.APIURL, c.AuthURL, c.HTTP = srv.URL+"/api", srv.URL+"/auth", srv.Client()
	c.Session = &tidal.Session{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), CountryCode: "US"}
	return c, srv
}

func benchItems(n int) []*item {
	var items []*item
	for i := 0; i < n; i++ {
		ct := catalog.Track{Title: fmt.Sprintf("Song %d", i), Artists: []string{"Main"}}
		items = append(items, &item{t: ct, key: ct.Key(), target: catalog.Tidal,
			tidalTrack: tidal.Track{ID: int64(1000 + i), Title: ct.Title, Artists: ct.Artists, Album: "Alb", AlbumID: 9, TrackNo: i + 1}})
	}
	return items
}

func makeAudio(t *testing.T) (ffmpeg, ffprobe string, audio []byte) {
	ffmpeg, ffprobe = workingFFmpeg()
	if ffmpeg == "" {
		t.Skip("no working ffmpeg/ffprobe")
	}
	src := filepath.Join(t.TempDir(), "src.flac")
	if err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=d=2", "-c:a", "flac", src).Run(); err != nil {
		t.Skipf("ffmpeg can't make a FLAC here: %v", err)
	}
	audio, _ = os.ReadFile(src)
	return
}

// The run harness: a batch of fake Tidal downloads end to end (stream lookup, fetch, tag
// pass, integrity check, filing, database), with the work split across 1 or 2 workers, and
// a per-phase timing of one song. Fakes only; run with -v to see the numbers.
func TestRunHarnessTidal(t *testing.T) {
	ffmpeg, ffprobe, audio := makeAudio(t)
	const n = 12
	const cdn = 150 * time.Millisecond // a modest CDN round trip per file
	run := func(workers int) time.Duration {
		c, _ := fakeTidal(t, audio, cdn)
		root := t.TempDir()
		st, err := store.Open(appname.StateDir(root))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		old := tidalWorkers
		tidalWorkers = workers
		defer func() { tidalWorkers = old }()
		led := &jobs.Ledger{}
		ex := &runExits{led: led, sink: led}
		began := time.Now()
		failed, err := runTidalItems(context.Background(), ui.New(), opts{out: root}, ex, st, ffmpeg, ffprobe, c, benchItems(n))
		d := time.Since(began)
		if err != nil || len(failed) != 0 {
			t.Fatalf("workers=%d: err=%v failed=%d", workers, err, len(failed))
		}
		if cnt := led.Counts(); cnt.Done != n {
			t.Fatalf("workers=%d: ledger %+v", workers, cnt)
		}
		if left, _ := filepath.Glob(filepath.Join(appname.StateDir(root), "partial", "*")); len(left) != 0 {
			t.Errorf("work files left behind: %v", left)
		}
		return d
	}
	d1, d2 := run(1), run(2)
	t.Logf("%d Tidal songs, %v CDN latency each: 1 worker %v, 2 workers %v (x%.2f)", n, cdn,
		d1.Round(time.Millisecond), d2.Round(time.Millisecond), d1.Seconds()/d2.Seconds())

	// Where one song's time goes outside the network.
	dir := t.TempDir()
	f := filepath.Join(dir, "a.flac")
	_ = os.WriteFile(f, audio, 0o644)
	ctx := context.Background()
	tm := func(name string, fn func()) {
		b := time.Now()
		for i := 0; i < 5; i++ {
			fn()
		}
		t.Logf("  %-24s %v per song", name, (time.Since(b) / 5).Round(time.Microsecond))
	}
	tm("tag.Write (ffmpeg+probe)", func() {
		_ = tag.Write(ctx, ffmpeg, ffprobe, f, map[string]string{"title": "x", "artist": "y"}, "")
	})
	tm("verify.File (ffprobe)", func() { _ = verify.File(ctx, ffprobe, f, verify.Want{Ext: ".flac", Duration: 2}) })
	tm("verify.File (header only)", func() { _ = verify.File(ctx, "", f, verify.Want{Ext: ".flac"}) })
	st, _ := store.Open(appname.StateDir(dir))
	defer st.Close()
	tm("LibraryPut", func() { _ = st.LibraryPut("k", "t", "a", "tidal", 5, f) })
}

// Several stages share one display and one ledger from different goroutines without
// stepping on each other (run with -race).
func TestRunStagesShareDisplayAndLedger(t *testing.T) {
	u := ui.New()
	led := &jobs.Ledger{}
	ex := &runExits{led: led, sink: jobs.Multi{led, jobs.Discard}}
	var wg sync.WaitGroup
	for _, lane := range []string{"Tidal", "Apple Music", "YouTube", "web"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lu := u.Lane(lane)
			for i := 0; i < 50; i++ {
				lu.Status("%s %d", lane, i)
				ex.emit(jobs.Event{Kind: jobs.Done, ID: fmt.Sprint(lane, i), Label: lane, Source: strings.ToLower(lane)})
			}
			lu.ClearStatus()
		}()
	}
	wg.Wait()
	if c := led.Counts(); c.Done != 200 {
		t.Errorf("done = %d, want 200", c.Done)
	}
}
