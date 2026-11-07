package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/model"
)

// A music folder from before the rename has .ytm/state.db: Open(.mfetchy) renames the
// folder, keeps every row, and repoints stored paths that lived inside it.
func TestOpenMigratesOldStateFolder(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, appname.OldStateDirName)
	s, err := Open(old) // not the new name: no migration, builds an old-style folder
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(old, "replaced", "A", "x.mp3")
	outside := filepath.Join(root, "A", "y.mp3")
	if _, err := s.Plan([]model.Track{{ID: "aaaaaaaaaaa"}, {ID: "bbbbbbbbbbb"}}, false); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkDone("aaaaaaaaaaa", inside)
	_ = s.MarkDone("bbbbbbbbbbb", outside)
	if err := s.LibraryPut("k1", "T", "A", "youtube", 1, inside); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := os.WriteFile(filepath.Join(old, "apple-match.txt"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // the second open must be a no-op
		ns, err := Open(filepath.Join(root, appname.StateDirName))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(old); err == nil {
			t.Fatal("old folder still exists")
		}
		wantInside := filepath.Join(root, appname.StateDirName, "replaced", "A", "x.mp3")
		if done, p := ns.TrackStatus("aaaaaaaaaaa"); !done || p != wantInside {
			t.Errorf("run %d: inside path = %q (done %v), want %q", i, p, done, wantInside)
		}
		if done, p := ns.TrackStatus("bbbbbbbbbbb"); !done || p != outside {
			t.Errorf("run %d: outside path changed to %q", i, p)
		}
		if l := ns.Library("k1"); l.Path != wantInside {
			t.Errorf("run %d: library path = %q", i, l.Path)
		}
		_ = ns.Close()
	}
	if b, err := os.ReadFile(filepath.Join(root, appname.StateDirName, "apple-match.txt")); err != nil || string(b) != "report" {
		t.Errorf("sidecar file lost: %v", err)
	}
}

// With both folders present the new one is used and the old one is left alone.
func TestOpenBothFoldersNewWins(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, appname.OldStateDirName)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "marker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, appname.StateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(root, appname.StateDirName))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := os.Stat(filepath.Join(old, "marker")); err != nil {
		t.Error("old folder was touched")
	}
	if _, err := os.Stat(filepath.Join(root, appname.StateDirName, "state.db")); err != nil {
		t.Error("new folder has no database")
	}
}
