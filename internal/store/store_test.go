package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
)

func TestPlanLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := []model.Track{{ID: "aaaaaaaaaaa"}, {ID: "bbbbbbbbbbb"}, {ID: "ccccccccccc"}}
	p, _ := s.Plan(tr, false)
	if len(p.Todo) != 3 {
		t.Fatalf("todo=%d", len(p.Todo))
	}
	s.MarkDone("aaaaaaaaaaa", "x")
	s.MarkFailed("bbbbbbbbbbb", "Private video", true)
	s.MarkFailed("ccccccccccc", "timeout", false)
	p, _ = s.Plan(tr, false)
	if p.Done != 1 || p.Skipped != 1 || len(p.Todo) != 1 || p.Todo[0].ID != "ccccccccccc" {
		t.Fatalf("unexpected plan %+v", p)
	}
	p, _ = s.Plan(tr, true)
	if len(p.Todo) != 2 {
		t.Fatalf("retryAll todo=%d", len(p.Todo))
	}
}

func TestImportDisk(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Artist"), 0o755)
	os.WriteFile(filepath.Join(dir, "Artist", "Song [dQw4w9WgXcQ].mp3"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "Artist", "Other.mp3"), nil, 0o644)
	s, _ := Open(filepath.Join(dir, ".ytm"))
	defer s.Close()
	n, err := s.ImportDisk(dir)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	p, _ := s.Plan([]model.Track{{ID: "dQw4w9WgXcQ"}}, false)
	if p.Done != 1 {
		t.Fatalf("expected done, got %+v", p)
	}
}
