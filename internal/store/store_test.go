package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/model"
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
	// unavailable but never searched for on YouTube Music: one more go, straight to the search
	p, _ = s.Plan(tr, false)
	if p.Done != 1 || p.Skipped != 0 || len(p.Todo) != 2 || !p.MusicFirst["bbbbbbbbbbb"] || p.MusicFirst["ccccccccccc"] {
		t.Fatalf("unexpected plan %+v", p)
	}
	// searched and still unavailable: skipped from now on
	if err := s.MarkMusicTried("bbbbbbbbbbb", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Plan(tr, false)
	if p.Done != 1 || p.Skipped != 1 || len(p.Todo) != 1 || p.Todo[0].ID != "ccccccccccc" {
		t.Fatalf("unexpected plan %+v", p)
	}
	p, _ = s.Plan(tr, true)
	if len(p.Todo) != 2 || len(p.MusicFirst) != 0 {
		t.Fatalf("retryAll todo=%d musicFirst=%v", len(p.Todo), p.MusicFirst)
	}
}

func TestAlbumCache(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Album("c418\x00volume alpha"); ok {
		t.Fatal("nothing cached yet")
	}
	s.SetAlbum("c418\x00volume alpha", `{"playlist":"OLAK5uy_x"}`)
	if d, ok := s.Album("c418\x00volume alpha"); !ok || d != `{"playlist":"OLAK5uy_x"}` {
		t.Fatalf("got %q %v", d, ok)
	}
	s.SetAlbum("x\x00not there", "")
	if d, ok := s.Album("x\x00not there"); !ok || d != "" {
		t.Fatal("a fresh 'not found' is believed")
	}
	s.db.Exec(`UPDATE albums SET checked=0 WHERE key=?`, "x\x00not there")
	if _, ok := s.Album("x\x00not there"); ok {
		t.Fatal("an old 'not found' should be looked up again")
	}
}

func TestMigrateAddsMusicTried(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir) // second open: the column already exists
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.MarkMusicTried("aaaaaaaaaaa", ""); err != nil {
		t.Fatal(err)
	}
}

// A YouTube Music match found by a run that was then interrupted is reused, not searched
// for again; once the track is done it no longer matters; a match that failed is forgotten.
func TestReplacementRemembered(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := []model.Track{{ID: "aaaaaaaaaaa", Title: "Song"}}
	s.Plan(tr, false)
	s.MarkFailed("aaaaaaaaaaa", "Video unavailable", true)
	s.MarkMusicTried("aaaaaaaaaaa", "bbbbbbbbbbb")
	p, _ := s.Plan(tr, false)
	if p.Replacements["aaaaaaaaaaa"] != "bbbbbbbbbbb" || !p.MusicFirst["aaaaaaaaaaa"] || len(p.Todo) != 1 {
		t.Fatalf("replacement not reused: %+v", p)
	}
	s.MarkMusicTried("aaaaaaaaaaa", "") // the match failed
	if p, _ = s.Plan(tr, false); len(p.Todo) != 0 || p.Skipped != 1 {
		t.Fatalf("a failed match should leave the track skipped: %+v", p)
	}
	s.MarkMusicTried("aaaaaaaaaaa", "../../etc")
	if p, _ = s.Plan(tr, false); len(p.Replacements) != 0 {
		t.Fatalf("an invalid id must never be stored: %+v", p)
	}
}

func TestFailuresFlagOtherVersions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := []model.Track{
		{ID: "aaaaaaaaaaa", Title: "Verbatim", Artists: []string{"Mother Mother"}},
		{ID: "bbbbbbbbbbb", Title: "verbatim", Artists: []string{"Mother Mother"}},
		{ID: "ccccccccccc", Title: "Ward", Artists: []string{"C418"}},
	}
	s.Plan(tr, false)
	s.MarkDone("aaaaaaaaaaa", "x")
	s.MarkFailed("bbbbbbbbbbb", "Video unavailable", true)
	s.MarkFailed("ccccccccccc", "Video unavailable", true)
	fs, err := s.Failures()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range fs {
		got[f.ID] = f.OtherVersion
	}
	if !got["bbbbbbbbbbb"] || got["ccccccccccc"] {
		t.Fatalf("other-version flags wrong: %v", got)
	}
}

func TestImportDisk(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Artist"), 0o755)
	os.WriteFile(filepath.Join(dir, "Artist", "Song [dQw4w9WgXcQ].mp3"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "Artist", "Other.mp3"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "Artist", "01 - Tagged.opus"), nil, 0o644)
	s, _ := Open(appname.StateDir(dir))
	defer s.Close()
	// a fresh database: names first, then tags for files without an id in the name
	probed := map[string]bool{}
	idOf := func(p string) string {
		probed[filepath.Base(p)] = true
		if filepath.Base(p) == "01 - Tagged.opus" {
			return "kJQP7kiw5Fk"
		}
		return ""
	}
	n, err := s.ImportDisk(dir, idOf)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if probed["Song [dQw4w9WgXcQ].mp3"] {
		t.Fatal("a file with the id in its name must not be probed")
	}
	p, _ := s.Plan([]model.Track{{ID: "dQw4w9WgXcQ"}, {ID: "kJQP7kiw5Fk"}}, false)
	if p.Done != 2 {
		t.Fatalf("expected done, got %+v", p)
	}
	if s.Owner(filepath.Join(dir, "Artist", "01 - Tagged.opus")) != "kJQP7kiw5Fk" {
		t.Fatal("path not recorded")
	}
	// the database now knows files: tags are not read again
	probed = map[string]bool{}
	if _, err := s.ImportDisk(dir, idOf); err != nil || len(probed) != 0 {
		t.Fatalf("probed again: %v %v", probed, err)
	}
}

func TestPremiumTodo(t *testing.T) {
	s, _ := Open(t.TempDir())
	defer s.Close()
	s.Plan([]model.Track{{ID: "aaaaaaaaaaa", Title: "A"}, {ID: "bbbbbbbbbbb", Title: "B"}, {ID: "ccccccccccc", Title: "C"}}, false)
	s.MarkDone("aaaaaaaaaaa", "/m/a.opus")
	s.MarkDone("bbbbbbbbbbb", "/m/b.opus")
	// c never downloaded: nothing to upgrade
	todo, err := s.PremiumTodo(10)
	if err != nil || len(todo) != 2 {
		t.Fatalf("todo=%v err=%v", todo, err)
	}
	s.SetPremium("aaaaaaaaaaa", 1)
	s.SetPremium("bbbbbbbbbbb", -1)
	if todo, _ := s.PremiumTodo(10); len(todo) != 0 {
		t.Fatalf("upgraded / unavailable must not come back: %v", todo)
	}
}

func TestPathClaimsAndMoves(t *testing.T) {
	s, _ := Open(t.TempDir())
	defer s.Close()
	s.Plan([]model.Track{{ID: "aaaaaaaaaaa"}}, false)
	s.SetPath("aaaaaaaaaaa", "/m/A/01 - Intro.opus")
	if s.Owner("/m/A/01 - Intro.opus") != "aaaaaaaaaaa" || s.Owner("/m/A/other.opus") != "" {
		t.Fatal("owner lookup")
	}
	s.MovePath("/m/A/01 - Intro.opus", "/m/A/02 - Intro.opus")
	if s.Owner("/m/A/02 - Intro.opus") != "aaaaaaaaaaa" {
		t.Fatal("move not recorded")
	}
}

func TestPlanSkipsInvalidIDs(t *testing.T) {
	s, err := Open(appname.StateDir(t.TempDir()))
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
	dir := appname.StateDir(root)
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
