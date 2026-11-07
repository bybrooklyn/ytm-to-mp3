package inputs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"https://youtu.be/dQw4w9WgXcQ?si=abc&t=3":                                         "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"youtu.be/dQw4w9WgXcQ":                                                            "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=share&pp=x":                  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":                                       "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ&si=zz":                             "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://open.spotify.com/intl-de/track/4uLU6hMCjMI75M1A2tKUQC?si=1234&context=x": "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		"spotify:track:4uLU6hMCjMI75M1A2tKUQC":                                            "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		"https://music.apple.com/us/album/x/123?i=456&l=en-US&uo=4":                       "https://music.apple.com/us/album/x/123?i=456",
		"HTTPS://SoundCloud.com/Artist/Track/?utm_source=clipboard&utm_medium=text#frag":  "https://soundcloud.com/Artist/Track",
	}
	for in, want := range cases {
		got, ok := Normalize(in)
		if !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := Normalize("hello"); ok {
		t.Error("a word is not a link")
	}
}

func TestParseMixedBlock(t *testing.T) {
	block := `
# my list
https://youtu.be/dQw4w9WgXcQ?si=x  https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC?si=1
(https://www.youtube.com/watch?v=dQw4w9WgXcQ).
<https://soundcloud.com/a/b>, https://a.bandcamp.com/track/c
https://example.com/nope
notafile
https://music.apple.com/us/album/x/123?i=456
https://music.apple.com/us/album/x/123?i=456&l=en
`
	r := Parse([]string{block})
	var kinds []Kind
	for _, it := range r.Items {
		kinds = append(kinds, it.Kind)
	}
	want := []Kind{YouTube, Spotify, Web, Web, Apple}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v (%+v)", kinds, want, r.Items)
	}
	if r.Dups != 2 {
		t.Errorf("dups = %d, want 2", r.Dups)
	}
	if len(r.Bad) != 2 {
		t.Fatalf("bad = %v", r.Bad)
	}
	if r.Bad[0].Line != 6 || !strings.Contains(r.Bad[0].Reason, "not a site") {
		t.Errorf("bad[0] = %+v", r.Bad[0])
	}
	if r.Bad[1].Line != 7 {
		t.Errorf("bad[1] = %+v", r.Bad[1])
	}
}

func TestParseFilesAndLists(t *testing.T) {
	dir := t.TempDir()
	zip := filepath.Join(dir, "My Takeout.zip")
	os.WriteFile(zip, []byte("x"), 0o644)
	list := filepath.Join(dir, "links.txt")
	os.WriteFile(list, []byte("https://youtu.be/dQw4w9WgXcQ\n\n# c\nhttps://youtu.be/9bZkp7q19f0\n"), 0o644)

	r := Parse([]string{zip, list, "https://www.youtube.com/watch?v=9bZkp7q19f0"})
	if len(r.Items) != 3 || r.Items[0].Kind != File || r.Items[1].Kind != YouTube {
		t.Fatalf("items = %+v bad=%v", r.Items, r.Bad)
	}
	if r.Dups != 1 {
		t.Errorf("dups = %d", r.Dups)
	}
	// a path with spaces on a line of its own, and quoted
	r = Parse([]string{`"` + zip + `"`})
	if len(r.Items) != 1 || r.Items[0].Value != zip {
		t.Errorf("quoted path: %+v %v", r.Items, r.Bad)
	}
}

func TestBareIDAndPlaylist(t *testing.T) {
	r := Parse([]string{"dQw4w9WgXcQ https://www.youtube.com/playlist?list=PLabc123&si=1 https://music.youtube.com/playlist?list=PLabc123"})
	if len(r.Items) != 2 || r.Dups != 1 {
		t.Fatalf("%+v dups=%d bad=%v", r.Items, r.Dups, r.Bad)
	}
}
