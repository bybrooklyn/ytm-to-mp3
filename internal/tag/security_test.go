package tag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

func TestInside(t *testing.T) {
	root := filepath.FromSlash("/lib/music")
	for p, want := range map[string]bool{
		"/lib/music/a/b.mp3":        true,
		"/lib/music":                true,
		"/lib/music/../etc/passwd":  false,
		"/lib/musicians/x.mp3":      false,
		"/etc/passwd":               false,
		"relative/x.mp3":            false,
		"":                          false,
		"/lib/music/a/../../../etc": false,
	} {
		if got := inside(root, filepath.FromSlash(p)); got != want {
			t.Errorf("inside(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestCoverBackupName(t *testing.T) {
	a := coverBackupName("b1", "/x/a.mp3")
	if a == coverBackupName("b1", "/x/b.mp3") {
		t.Error("distinct paths must differ")
	}
	if !strings.HasPrefix(a, "b1-") || len(a) != len("b1-")+16+len(".jpg") {
		t.Errorf("unexpected name %q", a)
	}
}

func TestUndoRefusesEscapingJournal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim.mp3")
	os.WriteFile(outside, []byte("data"), 0o644)
	cases := map[string]journalEntry{
		"path":     {Batch: "b", Path: outside, Old: map[string]string{}},
		"new_path": {Batch: "b", Path: filepath.Join(root, "a.mp3"), NewPath: outside, Old: map[string]string{}},
		"backup":   {Batch: "b", Path: filepath.Join(root, "a.mp3"), CoverBackup: outside, Old: map[string]string{}},
		"dotdot":   {Batch: "b", Path: filepath.Join(root, "..", "x.mp3"), Old: map[string]string{}},
		"relative": {Batch: "b", Path: "a.mp3", Old: map[string]string{}},
	}
	for name, e := range cases {
		if err := writeJournal(root, []journalEntry{e}); err != nil {
			t.Fatal(err)
		}
		err := undo(context.Background(), Options{Dir: root, FFmpeg: "/nonexistent", UI: ui.New()})
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("%s: undo should refuse, got %v", name, err)
		}
		if _, serr := os.Stat(outside); serr != nil {
			t.Fatalf("%s: outside file was touched", name)
		}
	}
}

func TestWriteJournalAtomicRoundTrip(t *testing.T) {
	root := t.TempDir()
	e1 := journalEntry{Batch: "b1", Path: filepath.Join(root, "a.mp3"), Old: map[string]string{"title": "x"}}
	if err := appendJournal(root, e1); err != nil {
		t.Fatal(err)
	}
	if err := writeJournal(root, []journalEntry{e1, e1}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(journalPath(root))
	if strings.Count(string(b), "\n") != 2 {
		t.Errorf("journal = %q", b)
	}
	ents, _ := os.ReadDir(filepath.Dir(journalPath(root)))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if fi, _ := os.Stat(journalPath(root)); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("journal perms %v", fi.Mode().Perm())
	}
}

func TestProbeArgsExplicitPath(t *testing.T) {
	a := probeArgs("-weird.mp3")
	j := strings.Join(a, " ")
	if !strings.Contains(j, "-protocol_whitelist file,pipe") || !strings.Contains(j, "-i file:/") {
		t.Errorf("probeArgs = %s", j)
	}
}

func TestRunMakesDirAbsolute(t *testing.T) {
	root := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(filepath.Dir(root))
	// No journal: undo must complain using the absolute root, proving Run normalised Dir.
	err := Run(context.Background(), Options{Dir: filepath.Base(root), Undo: true, UI: ui.New()})
	if err == nil || !strings.Contains(err.Error(), root) {
		t.Errorf("expected error naming absolute %s, got %v", root, err)
	}
}

// With a real ffmpeg, Write must replace the file atomically and keep its mode.
func TestWriteReplacesInPlace(t *testing.T) {
	ff, err := lookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	d := t.TempDir()
	p := filepath.Join(d, "t.mp3")
	if err := genSilence(ff, p); err != nil {
		t.Skip("cannot generate audio: ", err)
	}
	os.Chmod(p, 0o640)
	before, _ := os.Stat(p)
	if err := Write(context.Background(), ff, p, map[string]string{"title": "Hello\x1b[2J"}, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if after.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", after.Mode().Perm())
	}
	if os.SameFile(before, after) {
		t.Log("same inode (unexpected but harmless)")
	}
	ents, _ := os.ReadDir(d)
	if len(ents) != 1 {
		t.Errorf("leftover temp files: %v", ents)
	}
}
