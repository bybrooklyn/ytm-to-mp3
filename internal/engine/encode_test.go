package engine

import (
	"slices"
	"strings"
	"testing"
)

func TestEncodeArgs(t *testing.T) {
	meta := map[string]string{"title": "T", "artist": "A", "album": ""}
	a := encodeArgs("in.webm", "c.jpg", "out.mp3", "mp3", "opus", meta)
	j := strings.Join(a, " ")
	for _, want := range []string{"libmp3lame", "-map 1:v:0", "attached_pic", "title=T", "artist=A", "-map_metadata -1"} {
		if !strings.Contains(j, want) {
			t.Errorf("mp3 args missing %q: %s", want, j)
		}
	}
	if strings.Contains(j, "album=") {
		t.Error("empty tags must be omitted")
	}
	if slices.Contains(a, "-vn:0") {
		t.Error("stray -vn")
	}
	if a[len(a)-1] != "out.mp3" {
		t.Error("dst must be last")
	}

	if j := strings.Join(encodeArgs("in.m4a", "", "o.m4a", "m4a", "mp4a.40.2", nil), " "); !strings.Contains(j, "-c:a copy") {
		t.Errorf("aac source should be remuxed: %s", j)
	}
	if j := strings.Join(encodeArgs("in.webm", "c.jpg", "o.opus", "opus", "opus", nil), " "); !strings.Contains(j, "-c:a copy") || strings.Contains(j, "-i c.jpg") {
		t.Errorf("opus should copy and skip cover: %s", j)
	}
}
