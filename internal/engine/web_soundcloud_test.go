package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/soundcloud"
)

// scServer fakes soundcloud.com plus api-v2 for one public track; with broken set, the API
// answers 500 so the native path fails.
func scServer(t *testing.T, broken bool) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			fmt.Fprintf(w, `<script src="%s/app.js"></script>`, srv.URL)
		case r.URL.Path == "/app.js":
			fmt.Fprint(w, `client_id:"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"`)
		case broken:
			w.WriteHeader(500)
		case r.URL.Path == "/resolve":
			fmt.Fprintf(w, `{"kind":"track","id":7,"title":"Native","permalink_url":"https://soundcloud.com/u/native","user":{"username":"U"},
			 "duration":1000,"track_authorization":"A","media":{"transcodings":[{"url":"%s/media","preset":"mp3_0_1","format":{"protocol":"progressive","mime_type":"audio/mpeg"}}]}}`, srv.URL)
		case r.URL.Path == "/media":
			fmt.Fprintf(w, `{"url":"%s/audio"}`, srv.URL)
		case r.URL.Path == "/audio":
			fmt.Fprint(w, "native-audio")
		}
	}))
	t.Cleanup(srv.Close)
	scMu.Lock()
	scClients = map[string]*soundcloud.Client{}
	scMu.Unlock()
	old := scFactory
	scFactory = func(string) (*soundcloud.Client, error) {
		c := soundcloud.New(srv.Client())
		c.API, c.Site = srv.URL, srv.URL+"/"
		return c, nil
	}
	t.Cleanup(func() { scFactory = old; scClients = map[string]*soundcloud.Client{} })
	return srv
}

// scFakeYtDlp is a yt-dlp stand-in: -J prints one SoundCloud entry; a download writes a file
// and prints its path.
func scFakeYtDlp(t *testing.T) deps.Tools {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "yt-dlp")
	script := `#!/bin/sh
for a in "$@"; do if [ "$a" = "-J" ]; then echo '{"id":"9","title":"Fallback","uploader":"Y","webpage_url":"https://soundcloud.com/u/fallback"}'; exit 0; fi; done
echo yt-dlp-audio > "` + dir + `/got.m4a"
echo "` + dir + `/got.m4a"
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return deps.Tools{YtDlp: p}
}

func TestSoundCloudNativeFirst(t *testing.T) {
	scServer(t, false)
	tools := deps.Tools{YtDlp: "/nonexistent/yt-dlp"} // must not be needed
	ts, err := ListWeb(context.Background(), tools, "", "https://soundcloud.com/u/native")
	if err != nil || len(ts) != 1 || ts[0].Title != "Native" || ts[0].ID != "7" || ts[0].Site != "soundcloud" {
		t.Fatal(ts, err)
	}
	p, err := FetchWeb(context.Background(), tools, "", ts[0], t.TempDir(), nil)
	if err != nil || !strings.HasSuffix(p, ".mp3") {
		t.Fatal(p, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "native-audio" {
		t.Errorf("%q", b)
	}
}

func TestSoundCloudFallsBackToYtDlp(t *testing.T) {
	scServer(t, true)
	tools := scFakeYtDlp(t)
	ts, err := ListWeb(context.Background(), tools, "", "https://soundcloud.com/u/fallback")
	if err != nil || len(ts) != 1 || ts[0].Title != "Fallback" {
		t.Fatal(ts, err)
	}
	p, err := FetchWeb(context.Background(), tools, "", ts[0], t.TempDir(), nil)
	if err != nil || !strings.HasSuffix(p, "got.m4a") {
		t.Fatal(p, err)
	}
}

func TestSoundCloudEnvForcesYtDlpAndBandcampSkipsNative(t *testing.T) {
	scServer(t, false)
	tools := scFakeYtDlp(t)
	t.Setenv("YTM_SOUNDCLOUD", "yt-dlp")
	ts, err := ListWeb(context.Background(), tools, "", "https://soundcloud.com/u/native")
	if err != nil || ts[0].Title != "Fallback" {
		t.Fatal(ts, err)
	}
	t.Setenv("YTM_SOUNDCLOUD", "")
	ts, err = ListWeb(context.Background(), tools, "", "https://artist.bandcamp.com/track/x")
	if err != nil || ts[0].Title != "Fallback" {
		t.Fatalf("bandcamp must go to yt-dlp: %v %v", ts, err)
	}
}
