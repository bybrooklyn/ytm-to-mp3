package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/model"
)

// A new song goes into the artist and album folders that are already there, whatever the
// capitalisation or edition marker, so no case-variant folder is created.
func TestOutputPathReusesExistingFolders(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Meganeko", "CHROMAKOPIA +")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01 - a.opus"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "dQw4w9WgXcQ", Title: "Song", Artists: []string{"meganeko"}, Album: "CHROMAKOPIA", TrackNo: 2}
	p, _ := outputPath(root, tr, &Resolved{}, "opus")
	if want := filepath.Join(dir, "02 - Song.opus"); p != want {
		t.Errorf("outputPath = %q, want %q", p, want)
	}
}
