package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
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

func TestLyricsSettingsRows(t *testing.T) {
	m, last, _ := testModel(true)
	for r, want := range map[rowKind]string{rowLyricsAfter: "on", rowLyricsLRCLIB: "on", rowLyricsNetEase: "off"} {
		if v, _, _ := m.value(r); v != want {
			t.Errorf("%v starts %q, want %q", r, v, want)
		}
	}
	m.cur = int(rowLyricsLRCLIB)
	press(m, "right")
	if !last.NoLyricsLRCLIB || last.NoLyricsAfter || last.LyricsNetEase {
		t.Fatalf("wrong setting toggled: %+v", *last)
	}
	m.cur = int(rowLyricsNetEase)
	press(m, "right")
	if !last.LyricsNetEase {
		t.Fatal("NetEase should switch on")
	}
}

func TestAudioFilesSkipsYtmFolder(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"A/b.opus", "A/b.lrc", ".ytm/replaced/c.mp3", "d.flac", "A/.ytm-tmp.m4a"} {
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
