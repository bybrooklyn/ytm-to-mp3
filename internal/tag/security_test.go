package tag

import (
	"context"
	"os"
	"os/exec"
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

// Ogg Opus keeps its cover as a comment: retagging must keep the cover and every other tag
// (a plain stream copy fails on it, and mapping audio only drops it).
func TestWriteOggKeepsCoverAndTags(t *testing.T) {
	ff, err1 := lookPath("ffmpeg")
	fp, err2 := lookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("no ffmpeg/ffprobe")
	}
	d := t.TempDir()
	p := filepath.Join(d, "t.opus")
	if err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "0.3",
		"-c:a", "libopus", "-metadata", "artist=Keep Me", "-y", p).Run(); err != nil {
		t.Skip("cannot make opus: ", err)
	}
	cover := filepath.Join(d, "c.jpg")
	if err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "color=red:s=800x500", "-frames:v", "1", "-y", cover).Run(); err != nil {
		t.Skip("cannot make jpeg: ", err)
	}
	ctx := context.Background()
	if err := Write(ctx, ff, fp, p, map[string]string{"title": "First"}, cover); err != nil {
		t.Fatal(err)
	}
	if err := Write(ctx, ff, fp, p, map[string]string{"album": "Second"}, ""); err != nil {
		t.Fatal(err)
	}
	info, err := Probe(ctx, fp, p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCover {
		t.Error("cover lost on retag")
	}
	for k, want := range map[string]string{"title": "First", "album": "Second", "artist": "Keep Me"} {
		if info.Tags[k] != want {
			t.Errorf("%s = %q, want %q (tags %v)", k, info.Tags[k], want, info.Tags)
		}
	}
	if ents, _ := os.ReadDir(d); len(ents) != 2 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

// The Premium upgrade swaps a file's audio and must keep its tags and cover.
func TestReplaceAudioKeepsTagsAndCover(t *testing.T) {
	ff, err1 := lookPath("ffmpeg")
	fp, err2 := lookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("no ffmpeg/ffprobe")
	}
	d := t.TempDir()
	p, src, cover := filepath.Join(d, "song.opus"), filepath.Join(d, "new.webm"), filepath.Join(d, "c.jpg")
	mk := func(dst string, extra ...string) error {
		a := append([]string{"-v", "error", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "0.3", "-c:a", "libopus"}, extra...)
		return exec.Command(ff, append(a, "-y", dst)...).Run()
	}
	if mk(p, "-metadata", "title=Song", "-metadata", "artist=A") != nil || mk(src, "-b:a", "96k") != nil ||
		exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "color=blue:s=300x300", "-frames:v", "1", "-y", cover).Run() != nil {
		t.Skip("cannot make test media")
	}
	ctx := context.Background()
	if err := Write(ctx, ff, fp, p, map[string]string{"album": "Alb"}, cover); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceAudio(ctx, ff, fp, src, p); err != nil {
		t.Fatal(err)
	}
	info, err := Probe(ctx, fp, p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCover || info.Tags["title"] != "Song" || info.Tags["artist"] != "A" || info.Tags["album"] != "Alb" {
		t.Errorf("lost something: cover=%v tags=%v", info.HasCover, info.Tags)
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
	if err := Write(context.Background(), ff, "", p, map[string]string{"title": "Hello\x1b[2J"}, ""); err != nil {
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
