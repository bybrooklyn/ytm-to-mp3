package takeout

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

const songs = "\xef\xbb\xbfVideo ID,Song Title,Album Title,Artist Name 1,Artist Name 2\n" +
	"dQw4w9WgXcQ,Never Gonna Give You Up,Whenever You Need Somebody,Rick Astley,\n" +
	"jNQXAC9IVRw,,,,\n" +
	"dQw4w9WgXcQ,dupe,,,\n" +
	"short,bad id,,,\n"

const playlist = "Video ID,Playlist Video Creation Timestamp\naaaaaaaaaaa,2020\njNQXAC9IVRw,2020\n"

func writeZip(t *testing.T, files map[string]string) string {
	p := filepath.Join(t.TempDir(), "takeout.zip")
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

func TestZipPrefersSongsCSV(t *testing.T) {
	p := writeZip(t, map[string]string{
		"Takeout/YouTube and YouTube Music/music (library and uploads)/music library songs.csv": songs,
		"Takeout/YouTube and YouTube Music/playlists/Watch later.csv":                           playlist,
		"Takeout/YouTube and YouTube Music/history/watch-history.json":                          "{}",
	})
	tracks, err := Load(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2: %+v", len(tracks), tracks)
	}
	if tracks[0].Title != "Never Gonna Give You Up" || tracks[0].Artists[0] != "Rick Astley" || tracks[0].Album == "" {
		t.Errorf("bad first track: %+v", tracks[0])
	}
	all, _ := Load(p, true)
	if len(all) != 3 {
		t.Errorf("--all should add playlist-only IDs, got %d", len(all))
	}
}

func TestIDList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ids.txt")
	os.WriteFile(p, []byte("https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=x\nhttps://youtu.be/jNQXAC9IVRw\njNQXAC9IVRw\tArtist\tTitle\terr\nnonsense\n"), 0o644)
	tracks, err := Load(p, false)
	if err != nil || len(tracks) != 2 {
		t.Fatalf("got %v, %v", tracks, err)
	}
}

func TestNoTracks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(p, []byte("hello\n"), 0o644)
	if _, err := Load(p, false); err == nil {
		t.Error("expected error")
	}
}
