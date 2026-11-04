package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
)

func TestCheckCookies(t *testing.T) {
	d := t.TempDir()
	good := filepath.Join(d, "good.txt")
	os.WriteFile(good, []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tx\n"), 0o600)
	bad := filepath.Join(d, "bad.txt")
	os.WriteFile(bad, []byte("# Netscape HTTP Cookie File\n.example.com\tTRUE\t/\tTRUE\t0\tSID\tx\n"), 0o600)
	if err := checkCookies(good); err != nil {
		t.Errorf("good file refused: %v", err)
	}
	if checkCookies(bad) == nil || checkCookies(filepath.Join(d, "missing")) == nil {
		t.Error("non-YouTube or missing cookies accepted")
	}
}

func TestPremiumFormatsNeverFallBack(t *testing.T) {
	for f, want := range map[string]string{"opus": "774", "m4a": "141", "mp3": "774/141", "flac": "774/141"} {
		if got := engine.PremiumFormats(f); got != want {
			t.Errorf("%s: %q, want %q", f, got, want)
		}
	}
}
