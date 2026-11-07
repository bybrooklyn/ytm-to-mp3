package main

import (
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/config"
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
	o, _, err := parse("mfy", []string{"https://youtu.be/dQw4w9WgXcQ"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.albums || o.song {
		t.Errorf("albums should be on and --song off by default: albums=%v song=%v", o.albums, o.song)
	}
	if o, _, _ = parse("mfy", []string{"--song", "x"}); !o.song || !o.albums {
		t.Errorf("--song is for this run only and leaves the setting alone: %+v", o.song)
	}
	if o, _, _ = parse("mfy", []string{"--albums=false", "x"}); o.albums {
		t.Error("--albums=false turns albums off")
	}

	cfg := config.Load()
	cfg.NoAlbums = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if o, _, _ = parse("mfy", []string{"x"}); o.albums {
		t.Error("the Complete albums setting turns it off")
	}
}
