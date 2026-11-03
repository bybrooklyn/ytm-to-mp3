package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeJPEG(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.cover")
	if err := os.WriteFile(p, []byte("\xff\xd8\xff\xe0JFIF....."), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEncodeArgs(t *testing.T) {
	meta := map[string]string{"title": "T", "artist": "A", "album": ""}
	cover := writeJPEG(t)
	a := encodeArgs("in.webm", cover, "out.mp3", "mp3", "opus", meta)
	j := strings.Join(a, " ")
	for _, want := range []string{"libmp3lame", "-map 1:v:0", "attached_pic", "title=T", "artist=A", "-map_metadata -1",
		"-protocol_whitelist file,pipe", "-f image2", "-c:v mjpeg -i file:" + cover} {
		if !strings.Contains(j, want) {
			t.Errorf("mp3 args missing %q: %s", want, j)
		}
	}
	if strings.Count(j, "-protocol_whitelist file,pipe") != 2 || !strings.Contains(j, "-nostdin") {
		t.Errorf("both inputs need the whitelist: %s", j)
	}
	if strings.Contains(j, "album=") {
		t.Error("empty tags must be omitted")
	}
	if slices.Contains(a, "-vn:0") {
		t.Error("stray -vn")
	}
	if last := a[len(a)-1]; !strings.HasPrefix(last, "file:") || !strings.HasSuffix(last, "out.mp3") {
		t.Errorf("dst must be last and explicit: %q", last)
	}

	if j := strings.Join(encodeArgs("in.m4a", "", "o.m4a", "m4a", "mp4a.40.2", nil), " "); !strings.Contains(j, "-c:a copy") {
		t.Errorf("aac source should be remuxed: %s", j)
	}
	if j := strings.Join(encodeArgs("in.webm", cover, "o.opus", "opus", "opus", nil), " "); !strings.Contains(j, "-c:a copy") || strings.Contains(j, "image2") {
		t.Errorf("opus should copy and skip cover: %s", j)
	}
}

func TestEncodeArgsPathsNeverOptions(t *testing.T) {
	a := encodeArgs("-evil.webm", "", "-out.mp3", "mp3", "opus", nil)
	for i, s := range a {
		if i > 0 && a[i-1] == "-i" && !strings.HasPrefix(s, "file:/") {
			t.Errorf("input %q is not an explicit absolute file: path", s)
		}
	}
	if last := a[len(a)-1]; !strings.HasPrefix(last, "file:/") {
		t.Errorf("output %q is not explicit", last)
	}
}

func TestEncodeArgsRejectsNonImageCover(t *testing.T) {
	for name, content := range map[string]string{
		"playlist": "#EXTM3U\nfile:///etc/passwd\n",
		"gif":      "GIF89a......",
		"svg":      "<svg xmlns='http://www.w3.org/2000/svg'/>",
	} {
		p := filepath.Join(t.TempDir(), "c")
		os.WriteFile(p, []byte(content), 0o600)
		j := strings.Join(encodeArgs("in.webm", p, "o.mp3", "mp3", "opus", nil), " ")
		if strings.Contains(j, "image2") || strings.Contains(j, "-map 1:v:0") {
			t.Errorf("%s: cover must be skipped: %s", name, j)
		}
	}
}

func TestCodecFromExt(t *testing.T) {
	for p, want := range map[string]string{
		"/t/dl-x.webm": "opus", "/t/dl-x.opus": "opus", "/t/dl-x.m4a": "mp4a",
		"/t/dl-x.MP4": "mp4a", "/t/dl-x.mp3": "", "/t/dl-x": "",
	} {
		if got := codecFromExt(p); got != want {
			t.Errorf("codecFromExt(%q) = %q, want %q", p, got, want)
		}
	}
}
