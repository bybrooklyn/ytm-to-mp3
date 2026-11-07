package tag

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// workingTool returns a runnable ffmpeg and ffprobe, or skips: a broken binary can shadow
// a good one on PATH, so well-known locations are tried too.
func workingTools(t *testing.T) (string, string) {
	t.Helper()
	find := func(name string) string {
		for _, c := range []string{"/usr/bin/" + name, name} {
			p, err := exec.LookPath(c)
			if err == nil && exec.Command(p, "-version").Run() == nil {
				return p
			}
		}
		t.Skip("no working " + name)
		return ""
	}
	return find("ffmpeg"), find("ffprobe")
}

func makeMP3(t *testing.T, ffmpeg, dst string, tags map[string]string, cover string) {
	t.Helper()
	args := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "0.5",
		"-c:a", "libmp3lame", "-id3v2_version", "3"}
	for k, v := range tags {
		args = append(args, "-metadata", k+"="+v)
	}
	args = append(args, "-y", dst)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("cannot make an mp3: %v %s", err, out)
	}
	if cover != "" { // the package's own writer attaches the picture
		ffprobe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
		if err := Write(context.Background(), ffmpeg, ffprobe, dst, nil, cover); err != nil {
			t.Skipf("cannot attach a cover: %v", err)
		}
	}
}

func makeJPEG(t *testing.T, ffmpeg, dst string, px int) {
	t.Helper()
	size := strconv.Itoa(px)
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=red:s="+size+"x"+size,
		"-frames:v", "1", "-y", dst).CombinedOutput(); err != nil {
		t.Skipf("cannot make a jpeg: %v %s", err, out)
	}
}

// A YouTube copy that carries a MusicBrainz id, an ISRC, a genre and lyrics is replaced by
// a purchased file with good tags and bigger art: the id, genre and lyrics survive, the
// purchase's own tags and art win.
func TestCarryTagsKeepsWhatTheReplacementLacks(t *testing.T) {
	ff, fp := workingTools(t)
	dir := t.TempDir()
	small, big := filepath.Join(dir, "small.jpg"), filepath.Join(dir, "big.jpg")
	makeJPEG(t, ff, small, 200)
	makeJPEG(t, ff, big, 800)

	old := filepath.Join(dir, "old.mp3")
	makeMP3(t, ff, old, map[string]string{
		"title": "Pronoun Palace (Official Audio)", "artist": "Composer - Topic", "album": "Pronoun Palace",
		"genre": "Soundtrack", "TSRC": "USRC12100001", "date": "2021-03-02",
		"MusicBrainz Track Id": "5d7c1c36-8b0e-4a4f-9a1f-2f1f3f9a1e11", "lyrics": "la la la",
	}, small)
	nw := filepath.Join(dir, "new.mp3")
	makeMP3(t, ff, nw, map[string]string{
		"title": "Pronoun Palace", "artist": "Composer", "album": "Pronoun Palace", "album_artist": "Composer", "track": "3/12",
	}, big)

	from, err := CarryTags(context.Background(), ff, fp, nw, "bandcamp-purchase", old, "youtube")
	if err != nil {
		t.Fatal(err)
	}
	info, err := Probe(context.Background(), fp, nw)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"title": "Pronoun Palace", "artist": "Composer", "genre": "Soundtrack", "isrc": "USRC12100001",
		"date": "2021-03-02", "musicbrainz_trackid": "5d7c1c36-8b0e-4a4f-9a1f-2f1f3f9a1e11", "track": "3/12",
	} {
		if got := info.Tags[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if info.CoverW != 800 {
		t.Errorf("the purchase's larger cover should stay, got %dpx", info.CoverW)
	}
	if from["title"] != "bandcamp-purchase" || from["genre"] != "youtube" {
		t.Errorf("provenance: %v", from)
	}

	// and the other way: a poor new cover is replaced by the old file's bigger one
	nw2 := filepath.Join(dir, "new2.mp3")
	makeMP3(t, ff, nw2, map[string]string{"title": "Pronoun Palace", "artist": "Composer"}, small)
	old2 := filepath.Join(dir, "old2.mp3")
	makeMP3(t, ff, old2, map[string]string{"title": "x"}, big)
	if _, err := CarryTags(context.Background(), ff, fp, nw2, "apple", old2, "youtube"); err != nil {
		t.Fatal(err)
	}
	if info, _ := Probe(context.Background(), fp, nw2); info.CoverW != 800 {
		t.Errorf("the bigger old cover should be carried, got %dpx", info.CoverW)
	}
	_ = os.Remove(old)
}
