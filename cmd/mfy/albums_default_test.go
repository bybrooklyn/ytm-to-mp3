package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/store"
)

// sandboxConfig points the config folder at an empty temp dir on every OS.
func sandboxConfig(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	t.Setenv("HOME", d)
	t.Setenv("AppData", d)
}

func TestAlbumsOnByDefault(t *testing.T) {
	sandboxConfig(t)
	o, _, err := parse("ytm", []string{"https://youtu.be/dQw4w9WgXcQ"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.albums || o.song {
		t.Errorf("albums should be on and --song off by default: albums=%v song=%v", o.albums, o.song)
	}
	if o, _, _ = parse("ytm", []string{"--song", "x"}); !o.song || !o.albums {
		t.Errorf("--song is for this run only and leaves the setting alone: %+v", o.song)
	}
	if o, _, _ = parse("ytm", []string{"--albums=false", "x"}); o.albums {
		t.Error("--albums=false turns albums off")
	}

	cfg := config.Load()
	cfg.NoAlbums = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if o, _, _ = parse("ytm", []string{"x"}); o.albums {
		t.Error("the Complete albums setting turns it off")
	}
}

func TestIsSongLink(t *testing.T) {
	for in, want := range map[string]bool{
		"https://music.apple.com/us/album/name/1440857781?i=1440858081": true,
		"https://music.apple.com/us/album/name/1440857781":              false,
		"https://music.apple.com/us/song/name/1440858081":               true,
		"https://music.apple.com/us/playlist/x/pl.123":                  false,
		"https://open.spotify.com/track/0DiWol3AO6WpXZgp0goxAV":         true,
		"https://open.spotify.com/album/2noRn2Aes5aoNVsU6iWThc":         false,
		"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M":      false,
	} {
		if got := isSongLink(in); got != want {
			t.Errorf("isSongLink(%q) = %v", in, got)
		}
	}
}

func TestCatalogToModelCarriesMetadata(t *testing.T) {
	m := catalogToModel("sp_x", catalog.Track{Title: "T", Artists: []string{"A"}, Album: "L", AlbumArtist: "A",
		DiscNo: 2, ReleaseDate: "2020-02-02T00:00:00Z", Genre: "G", ISRC: "I",
		ArtworkURL: "https://is1-ssl.mzstatic.com/image/thumb/x/{w}x{h}bb.jpg"})
	if m.Date != "2020-02-02" || m.Disc != 2 || m.ISRC != "I" || m.Genre != "G" || m.AlbumArtist != "A" ||
		m.Cover != "https://is1-ssl.mzstatic.com/image/thumb/x/1200x1200bb.jpg" {
		t.Errorf("catalog metadata lost: %+v", m)
	}
}

func TestSummaries(t *testing.T) {
	s := ansi.Strip(runSummary(12, 1, 48<<20, 0, "YouTube"))
	if !strings.Contains(s, "YouTube") || !strings.Contains(s, "✓ 12 downloaded") || !strings.Contains(s, "✗ 1 failed") || !strings.Contains(s, "48") {
		t.Errorf("summary %q", s)
	}
	if s := ansi.Strip(runSummary(3, 0, 0, 0, "")); strings.Contains(s, "failed") || strings.Contains(s, "·") {
		t.Errorf("no failures and nothing measured are left out: %q", s)
	}

	fails := []store.Failure{
		{ID: "a", Title: "One", Artist: "X", Error: "unavailable"},
		{ID: "b", Title: "Two", Error: "network"},
		{ID: "c", Title: "Three", Error: "unavailable"},
		{ID: "d", Error: "unavailable\x1b[31m"},
	}
	lines := failureLines(fails, 3)
	joined := ansi.Strip(strings.Join(lines, "\n"))
	// d's error carries an escape code: stripped, it is the same reason as a and c
	if !strings.HasPrefix(joined, "  ✗ unavailable (3)") || !strings.Contains(joined, "      d\n") {
		t.Errorf("the most common reason comes first, escape codes stripped:\n%s", joined)
	}
	if !strings.Contains(joined, "X - One") || !strings.Contains(joined, "…and 1 more") {
		t.Errorf("songs are named up to the limit:\n%s", joined)
	}
}
