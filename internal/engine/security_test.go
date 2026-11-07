package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

func TestClassifySanitizes(t *testing.T) {
	in := "ERROR: [youtube] abc: \x1b]0;pwn\x07boom \x1b[2J\x1b[31m via socks5h://bob:s3cret@1.2.3.4:1080 \u202Eend"
	e := classify(in)
	for _, bad := range []string{"\x1b", "\x07", "bob", "s3cret", "\u202E"} {
		if strings.Contains(e.Msg, bad) {
			t.Errorf("message still contains %q: %q", bad, e.Msg)
		}
	}
	if !strings.Contains(e.Msg, "boom") {
		t.Errorf("lost message: %q", e.Msg)
	}
	long := classify("ERROR: " + strings.Repeat("x", 5000))
	if utf8.RuneCountInString(long.Msg) > maxMsg+1 {
		t.Errorf("message not capped: %d", len(long.Msg))
	}
}

func TestOutputTemplateSafe(t *testing.T) {
	cases := []struct{ dir, id, want string }{
		{"/tmp/x", "dQw4w9WgXcQ", "/tmp/x/dl-dQw4w9WgXcQ.%(ext)s"},
		{"/tmp/100%/x", "aaaaaaaaaaa", "/tmp/100%%/x/dl-aaaaaaaaaaa.%(ext)s"},
		{"/tmp/x", "%(title)s", "/tmp/x/dl-%%(title)s.%(ext)s"}, // defensive: never reachable with a valid id
	}
	for _, c := range cases {
		got := filepath.ToSlash(outputTemplate(c.dir, c.id))
		if got != c.want {
			t.Errorf("outputTemplate(%q,%q) = %q, want %q", c.dir, c.id, got, c.want)
		}
	}
}

func TestYtdlpRejectsInvalidID(t *testing.T) {
	y := ytdlp{tools: deps.Tools{YtDlp: "/nonexistent/yt-dlp"}}
	if _, err := y.Resolve(context.Background(), "../../x"); err == nil || kindOf(err) != kindPermanent {
		t.Errorf("Resolve accepted a bad id: %v", err)
	}
	if _, err := y.Download(context.Background(), "a/b", t.TempDir(), nil); err == nil || kindOf(err) != kindPermanent {
		t.Errorf("Download accepted a bad id: %v", err)
	}
}

func TestBaseArgsDisablePlugins(t *testing.T) {
	args := strings.Join(ytdlp{}.baseArgs(), " ")
	if !strings.Contains(args, "--ignore-config --no-plugin-dirs") {
		t.Errorf("baseArgs = %s", args)
	}
}

func TestRunDropsInvalidIDs(t *testing.T) {
	out := t.TempDir()
	s, err := store.Open(appname.StateDir(out))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum := Run(context.Background(), Options{Out: out, Format: "mp3", Store: s, UI: ui.New()},
		[]model.Track{{ID: "../../../etc/x"}, {ID: "bad"}, {ID: "a b"}})
	if sum.Done != 0 || sum.Failed != 0 {
		t.Errorf("summary %+v", sum)
	}
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if e.Name() != appname.StateDirName {
			t.Errorf("unexpected file %q", e.Name())
		}
	}
}

func TestOutputPathByteLimit(t *testing.T) {
	tr := model.Track{ID: "dQw4w9WgXcQ", Title: strings.Repeat("日本語", 60), Artists: []string{strings.Repeat("あ", 200)}}
	p, _ := outputPath("/out", tr, &Resolved{}, "flac")
	for _, c := range strings.Split(filepath.ToSlash(p), "/") {
		if len(c) > 255 {
			t.Errorf("component of %d bytes", len(c))
		}
	}
	if !strings.HasSuffix(p, ".flac") {
		t.Errorf("extension lost: %q", p)
	}
	tr.Album, tr.TrackNo, tr.TrackTotal = strings.Repeat("Ü", 200), 7, 12
	p, _ = outputPath("/out", tr, &Resolved{}, "flac")
	for _, c := range strings.Split(filepath.ToSlash(p), "/") {
		if len(c) > 255 {
			t.Errorf("component of %d bytes", len(c))
		}
	}
	if !strings.HasPrefix(filepath.Base(p), "07 - ") {
		t.Errorf("track number lost: %q", p)
	}
}

func TestOutputPathNamesAndTrackTag(t *testing.T) {
	cases := []struct {
		tr    model.Track
		r     Resolved
		want  string
		track string
	}{
		{model.Track{ID: "aaaaaaaaaaa", Title: "Door", Album: "Volume Alpha", Artists: []string{"C418"}, TrackNo: 2, TrackTotal: 24},
			Resolved{}, "/m/C418/Volume Alpha/02 - Door.opus", "2/24"},
		{model.Track{ID: "aaaaaaaaaaa", Title: "Door", Album: "Big Box", Artists: []string{"X"}, TrackNo: 7, TrackTotal: 120},
			Resolved{}, "/m/X/Big Box/007 - Door.opus", "7/120"},
		{model.Track{ID: "aaaaaaaaaaa", Title: "Door", Album: "Volume Alpha", Artists: []string{"C418"}},
			Resolved{Track: 5}, "/m/C418/Volume Alpha/05 - Door.opus", "5"},
		{model.Track{ID: "aaaaaaaaaaa", Title: "Door", Artists: []string{"C418"}},
			Resolved{Track: 5}, "/m/C418/Door.opus", "5"}, // no album: no number in the name
		{model.Track{ID: "aaaaaaaaaaa", Title: "Door", Album: "Volume Alpha", Artists: []string{"C418"}},
			Resolved{}, "/m/C418/Volume Alpha/Door.opus", ""},
	}
	for _, c := range cases {
		p, meta := outputPath("/m", c.tr, &c.r, "opus")
		if filepath.ToSlash(p) != c.want || meta["track"] != c.track {
			t.Errorf("got %q track %q, want %q track %q", p, meta["track"], c.want, c.track)
		}
	}
}

func TestFetchSizeCapAndHost(t *testing.T) {
	huge := &Resolved{URL: "https://x.googlevideo.com/p?clen=99999999999999", Protocol: "https"}
	if canFetch(huge) {
		t.Error("absurd clen must not be fetched directly")
	}
	if err := fetch(context.Background(), nil, huge, filepath.Join(t.TempDir(), "f"), nil, nil); !errors.Is(err, errUnfetchable) {
		t.Errorf("fetch err = %v", err)
	}
	if !canFetch(&Resolved{URL: "https://x.googlevideo.com/p?clen=1000", Protocol: "https"}) {
		t.Error("normal clen should be fetchable")
	}
	for host, want := range map[string]bool{
		"googlevideo.com": true, "rr1.googlevideo.com": true, "RR1.GoogleVideo.com": true,
		"evilgooglevideo.com": false, "googlevideo.com.evil.io": false, "": false,
	} {
		if isGoogleVideo(host) != want {
			t.Errorf("isGoogleVideo(%q) != %v", host, want)
		}
	}
}

func TestAtomicReplace(t *testing.T) {
	d := t.TempDir()
	final, tmp := filepath.Join(d, "final.mp3"), filepath.Join(d, "tmp.mp3")
	os.WriteFile(final, []byte("old"), 0o644)
	os.WriteFile(tmp, []byte("new"), 0o644)
	if err := store.Replace(tmp, final); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(final); string(b) != "new" {
		t.Errorf("final = %q", b)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("tmp should be gone")
	}
}
