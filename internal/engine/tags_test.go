package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

func TestOutputPathCatalogMetadata(t *testing.T) {
	tr := model.Track{ID: "sp_abc", Title: "Song (feat. Guest & Other)", Album: "LP", Artists: []string{"Main"},
		TrackNo: 3, TrackTotal: 10, AlbumArtist: "Main", Disc: 2, Date: "2019-05-31", Genre: "Pop", ISRC: "USABC1900001"}
	r := &Resolved{Date: "2023-01-01", Genre: "Music", Artists: []string{"Uploader - Topic"}}
	p, meta := outputPath("/m", tr, r, "opus")
	if filepath.ToSlash(p) != "/m/Main/LP/03 - Song (feat. Guest & Other).opus" {
		t.Errorf("path %q", p)
	}
	for k, want := range map[string]string{"date": "2019-05-31", "genre": "Pop", "isrc": "USABC1900001",
		"disc": "2", "track": "3/10", "album_artist": "Main", "artist": "Main; Guest; Other"} {
		if meta[k] != want {
			t.Errorf("%s = %q, want %q (catalog values beat YouTube's)", k, meta[k], want)
		}
	}
}

func TestCleanArtistsAndFeatured(t *testing.T) {
	if got := cleanArtists([]string{"Adele - Topic", "TaylorSwiftVEVO", "adele", " "}); !slices.Equal(got, []string{"Adele", "TaylorSwift"}) {
		t.Errorf("cleanArtists = %q", got)
	}
	if got := withFeatured([]string{"A"}, "Tune [ft. B, C and A]"); !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Errorf("withFeatured = %q", got)
	}
	in := []string{"A"}
	if got := withFeatured(in, "Plain title"); !slices.Equal(got, in) {
		t.Errorf("no credit, no change: %q", got)
	}
	// the caller's slice must not be written through
	base := make([]string, 1, 4)
	base[0] = "A"
	_ = withFeatured(base, "X (feat. B)")
	if len(base) != 1 || base[:2][1] == "B" {
		t.Error("withFeatured wrote into the caller's backing array")
	}
}

func TestResolvedDateSkipsUploadDate(t *testing.T) {
	if r := resolvedFrom(ytInfo{UploadDate: "20240101", ReleaseYear: 2001}); r.Date != "" || r.Year != 2001 {
		t.Errorf("a release year must beat the upload date: %+v", r)
	}
	if r := resolvedFrom(ytInfo{UploadDate: "20240101"}); r.Date != "2024-01-01" {
		t.Errorf("upload date is still the last resort: %q", r.Date)
	}
}

func TestCatalogCoverHosts(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://i.scdn.co/image/ab67":                               true,
		"https://is1-ssl.mzstatic.com/image/thumb/x/1200x1200bb.jpg": true,
		"http://i.scdn.co/image/ab67":                                false,
		"https://evil.example/i.scdn.co/x.jpg":                       false,
		"https://i.scdn.co.evil.example/x.jpg":                       false,
		"https://mzstatic.com.evil.example/x":                        false,
		"":                                                           false,
	} {
		if catalogCover(u) != ok {
			t.Errorf("catalogCover(%q) = %v", u, !ok)
		}
	}
}

// TestOpusGetsCover runs the real encode for an Opus download with cover art and reads
// the result back: the tags (track split into TRACKNUMBER/TRACKTOTAL) and the picture must
// both be there. Opus downloads used to get no art at all.
func TestOpusGetsCover(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("needs ffmpeg and ffprobe")
	}
	dir := t.TempDir()
	src, img := filepath.Join(dir, "src.webm"), filepath.Join(dir, "cover.jpg")
	for _, args := range [][]string{
		{"-f", "lavfi", "-i", "sine=d=1", "-c:a", "libopus", src},
		{"-f", "lavfi", "-i", "color=red:s=320x180", "-frames:v", "1", img},
	} {
		if out, err := exec.Command(ffmpeg, append([]string{"-loglevel", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("ffmpeg can't make test media: %v %s", err, out)
		}
	}
	e := &engine{tmp: dir}
	e.Tools.FFmpeg = ffmpeg
	meta := e.oggCover(context.Background(), "aaaaaaaaaaa", img)
	if meta == "" {
		t.Fatal("no Ogg cover made")
	}
	out := filepath.Join(dir, "out.opus")
	tags := map[string]string{"title": "T", "artist": "A", "track": "2/9", "isrc": "USABC1900001"}
	if err := runFFmpeg(context.Background(), ffmpeg, encodeArgs(src, meta, out, "opus", "opus", tags)); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(ffprobe, "-v", "error", "-show_streams", "-of", "json", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Streams []struct {
			CodecType string            `json:"codec_type"`
			Width     int               `json:"width"`
			Height    int               `json:"height"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	var cover bool
	got := map[string]string{}
	for _, s := range j.Streams {
		if s.CodecType == "video" {
			cover = s.Width == 180 && s.Height == 180 // squared
		}
		for k, v := range s.Tags {
			got[names.CanonicalTag(k)] = v
		}
	}
	if !cover {
		t.Errorf("no square cover in the opus file: %s", b)
	}
	for k, want := range map[string]string{"title": "T", "track": "2", "tracktotal": "9", "isrc": "USABC1900001"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], want, got)
		}
	}
	if _, err := os.Stat(out); err != nil || strings.Contains(string(b), "metadata_block_picture") {
		t.Errorf("the cover should read back as a picture, not a raw comment")
	}
}
