package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/config"
)

func TestLyricsProvidersFollowSettings(t *testing.T) {
	names := func(c config.Config) []string {
		var out []string
		for _, p := range lyricsProviders(c) {
			out = append(out, p.Name())
		}
		return out
	}
	if got := names(config.Config{}); len(got) != 1 || got[0] != "LRCLIB" {
		t.Errorf("default: LRCLIB only, got %v", got)
	}
	if got := names(config.Config{LyricsNetEase: true}); len(got) != 2 || got[0] != "LRCLIB" {
		t.Errorf("LRCLIB first, then NetEase: %v", got)
	}
	if got := names(config.Config{NoLyricsLRCLIB: true}); len(got) != 0 {
		t.Errorf("all off: %v", got)
	}
}

func TestAudioFilesSkipsYtmFolder(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"A/b.opus", "A/b.lrc", ".mfetchy/replaced/c.mp3", "d.flac", "A/.mfy-tmp.m4a"} {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := audioFiles(dir)
	if len(got) != 2 || filepath.Base(got[0]) != "b.opus" || filepath.Base(got[1]) != "d.flac" {
		t.Fatalf("%v", got)
	}
}
