package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
)

func TestApplePathScheme(t *testing.T) {
	tr := apple.Track{ID: "1", Title: "Let It Happen", Artist: "Tame Impala", AlbumArtist: "Tame Impala",
		Album: "Currents", TrackNumber: 1, TrackCount: 13}
	if got := applePath("/m", tr); got != "/m/Tame Impala/Currents/01 - Let It Happen.m4a" {
		t.Errorf("got %q", got)
	}
	tr.Album, tr.AlbumArtist = "", ""
	if got := applePath("/m", tr); got != "/m/Tame Impala/Let It Happen.m4a" {
		t.Errorf("no album: got %q", got)
	}
	// nothing from Apple's metadata can climb out of the folder
	tr.Artist, tr.Title = "../..", "../../etc/passwd"
	got := applePath("/m", tr)
	if rel, err := filepath.Rel("/m", filepath.Clean(got)); err != nil || strings.HasPrefix(rel, "..") || strings.Count(rel, "/") != 1 {
		t.Errorf("escapes or adds folders: %q", got)
	}
}

func TestAppleLibraryJobPlacement(t *testing.T) {
	dir := t.TempDir()
	yt := filepath.Join(dir, "Artist", "Album", "03 - Song.opus")
	if err := os.MkdirAll(filepath.Dir(yt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := appleFound{yt: model.Track{ID: "aaaaaaaaaaa", Title: "Song"}, found: true,
		m: apple.Match{Track: apple.Track{ID: "9", Title: "Song", Artist: "Artist", Album: "Album (Deluxe)", TrackNumber: 5}}}

	// next to the YouTube copy, same name
	j := appleLibraryJob(dir, yt, r, false)
	if j.dest != filepath.Join(dir, "Artist", "Album", "03 - Song.m4a") || j.replace {
		t.Errorf("next to: %+v", j)
	}
	// a YouTube m4a: keep both under different names
	m4a := strings.TrimSuffix(yt, ".opus") + ".m4a"
	if err := os.Rename(yt, m4a); err != nil {
		t.Fatal(err)
	}
	if j := appleLibraryJob(dir, m4a, r, false); j.dest != strings.TrimSuffix(m4a, ".m4a")+" (Apple Music).m4a" {
		t.Errorf("same extension: %q", j.dest)
	}
	if j := appleLibraryJob(dir, m4a, r, true); j.dest != m4a || !j.replace || j.ytPath != m4a {
		t.Errorf("replace m4a: %+v", j)
	}
	// not downloaded from YouTube: Apple's own scheme
	if j := appleLibraryJob(dir, "", r, true); j.dest != filepath.Join(dir, "Artist", "Album (Deluxe)", "05 - Song.m4a") || j.ytPath != "" {
		t.Errorf("not downloaded: %+v", j)
	}
}

func TestAppleQueryCleansYouTubeNoise(t *testing.T) {
	q := appleQuery(model.Track{Title: "Tame Impala - Let It Happen (Official Audio)", Artists: []string{"Tame Impala - Topic"}, Album: "Currents"})
	if q.Title != "Let It Happen" || len(q.Artists) != 1 || q.Artists[0] != "Tame Impala" {
		t.Errorf("got %+v", q)
	}
}

func TestAppleRowsFit(t *testing.T) {
	l := &appleLive{label: strings.Repeat("Long Artist Name - ", 10)}
	l.total.Store(100)
	l.got.Store(50)
	rows := appleRows([]*appleLive{l}, 3, 10, 1, 2<<20, 80)
	if len(rows) != 2 || !strings.Contains(rows[0], "3/10") || !strings.Contains(rows[1], "50%") {
		t.Fatalf("rows: %q", rows)
	}
	if n := len([]rune(stripANSI(rows[1]))); n > 80 {
		t.Errorf("row is %d wide, terminal 80", n)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
