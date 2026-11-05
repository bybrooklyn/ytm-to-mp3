package takeout

import (
	"archive/zip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const ytSongsPath = "Takeout/YouTube and YouTube Music/music (library and uploads)/music library songs.csv"

func zipAt(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	f.Close()
	return p
}

func TestSplitPathsAndCleanPath(t *testing.T) {
	cases := map[string][]string{
		`/a/b.zip`:                   {"/a/b.zip"},
		`"/a/My Files/t.zip"`:        {"/a/My Files/t.zip"},
		`'/a/x.zip' /b/y.zip`:        {"/a/x.zip", "/b/y.zip"},
		`file:///a/My%20Files/t.zip`: {"/a/My Files/t.zip"},
	}
	if runtime.GOOS != "windows" { // there a backslash separates folders; it isn't an escape
		cases[`/a/My\ Files/t.zip`] = []string{"/a/My Files/t.zip"}
		cases[`  /a/one.zip   /a/two\ 2.zip `] = []string{"/a/one.zip", "/a/two 2.zip"}
	}
	for in, want := range cases {
		if got := SplitPaths(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitPaths(%q) = %q, want %q", in, got, want)
		}
	}
	home, _ := os.UserHomeDir()
	if got := CleanPath("~/x.zip"); got != filepath.Join(home, "x.zip") {
		t.Errorf("tilde: %q", got)
	}
}

func TestDiscoverFindsByContentGroupsPartsAndSkipsOthers(t *testing.T) {
	dir := t.TempDir()
	// a renamed export, found by what is inside it
	zipAt(t, dir, "my-music.zip", map[string]string{ytSongsPath: songs})
	// two numbered parts of one export are one source
	zipAt(t, dir, "takeout-20261103T100000Z-001.zip", map[string]string{ytSongsPath: songs})
	zipAt(t, dir, "takeout-20261103T100000Z-002.zip", map[string]string{
		"Takeout/YouTube and YouTube Music/playlists/p.csv": playlist})
	// unrelated zips are ignored
	zipAt(t, dir, "holiday-photos.zip", map[string]string{"a.jpg": "x"})
	// a download in progress is never offered
	os.WriteFile(filepath.Join(dir, "takeout-1.zip.crdownload"), []byte("partial"), 0o600)

	got := Discover([]string{dir})
	if len(got) != 2 {
		t.Fatalf("want 2 sources, got %d: %+v", len(got), got)
	}
	var multi *Source
	for i := range got {
		if len(got[i].Paths) == 2 {
			multi = &got[i]
		}
	}
	if multi == nil || multi.Tracks < 2 {
		t.Fatalf("the two parts should group and load together: %+v", got)
	}
}

func TestFolderImportTakesEveryYouTubeZipInside(t *testing.T) {
	dir := t.TempDir()
	zipAt(t, dir, "part1.zip", map[string]string{ytSongsPath: songs})
	zipAt(t, dir, "part2.zip", map[string]string{"Takeout/YouTube/playlists/p.csv": playlist})
	zipAt(t, dir, "other.zip", map[string]string{"readme.txt": "hi"})
	tr, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) < 3 {
		t.Fatalf("expected songs and playlist tracks merged, got %d", len(tr))
	}
}
