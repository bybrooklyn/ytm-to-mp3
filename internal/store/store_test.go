package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestPlanSkipsInvalidIDs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), ".ytm"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bad := []string{"../../etc/pass", "short", "aaaaaaaaaa%", "aaaaaaaaaa/", "", "aaaaaaaaaaaa", "aaaaaaaaaa\n"}
	var tr []model.Track
	for _, b := range bad {
		tr = append(tr, model.Track{ID: b})
	}
	tr = append(tr, model.Track{ID: "dQw4w9WgXcQ"})
	p, err := s.Plan(tr, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Todo) != 1 || p.Todo[0].ID != "dQw4w9WgXcQ" || p.Invalid != len(bad) {
		t.Fatalf("plan=%+v", p)
	}
}

func TestPermsAndRedaction(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".ytm")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Errorf("dir perm %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Join(dir, "state.db")); fi.Mode().Perm() != 0o600 {
			t.Errorf("db perm %v", fi.Mode().Perm())
		}
	}
	s.Plan([]model.Track{{ID: "aaaaaaaaaaa"}}, false)
	s.MarkFailed("aaaaaaaaaaa", "ERROR \x1b]0;x\x07 via socks5h://u:p@10.0.0.1:1080 \u202Eboom", false)
	f, _ := s.Failures()
	if len(f) != 1 || strings.Contains(f[0].Error, "u:p") || strings.ContainsAny(f[0].Error, "\x1b\x07\u202E") {
		t.Fatalf("unsafe stored error: %q", f[0].Error)
	}
}

func TestNewTempDirUniqueAndPrivate(t *testing.T) {
	root := t.TempDir()
	a, err := NewTempDir(root)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewTempDir(root)
	if a == b {
		t.Fatal("temp dirs must be unique")
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatal("second call must not delete a live dir")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(a); fi.Mode().Perm() != 0o700 {
			t.Errorf("tmp perm %v", fi.Mode().Perm())
		}
	}
}

func TestCreateExclAndReplace(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "f")
	f, err := CreateExcl(p, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := CreateExcl(p, 0o600); err == nil {
		t.Error("second create must fail")
	}
	if runtime.GOOS != "windows" {
		l := filepath.Join(d, "link")
		os.Symlink(filepath.Join(d, "nowhere"), l)
		if _, err := CreateExcl(l, 0o600); err == nil {
			t.Error("must not create through a symlink")
		}
	}
	src := filepath.Join(d, "new")
	os.WriteFile(src, []byte("new"), 0o600)
	if err := Replace(src, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Errorf("not replaced: %q", b)
	}
}
