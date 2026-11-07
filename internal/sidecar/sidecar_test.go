package sidecar

import (
	"bytes"
	"context"
	"encoding/xml"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/tag"
)

// fake is a pretend library: file path -> what ffprobe would say, and the picture the file
// carries (its pixel size).
type fake struct {
	infos map[string]tag.Info
}

func (f *fake) opts() Options {
	return Options{
		JPG: true, NFO: true,
		Probe: func(_ context.Context, p string) (tag.Info, error) { return f.infos[p], nil },
		Extract: func(_ context.Context, audio, dst string) error {
			in := f.infos[audio]
			img := image.NewRGBA(image.Rect(0, 0, in.CoverW, in.CoverH))
			for x := 0; x < in.CoverW; x++ {
				img.Set(x, 0, color.RGBA{R: 200, A: 255})
			}
			var b bytes.Buffer
			_ = png.Encode(&b, img)
			return os.WriteFile(dst, b.Bytes(), 0o644)
		},
		Square: func(_ context.Context, src, dst string) error {
			data, _ := os.ReadFile(src)
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				return err
			}
			s := min(img.Bounds().Dx(), img.Bounds().Dy(), CoverPx)
			out := image.NewRGBA(image.Rect(0, 0, s, s))
			var b bytes.Buffer
			_ = jpeg.Encode(&b, out, nil)
			return os.WriteFile(dst, b.Bytes(), 0o644)
		},
	}
}

func song(t *testing.T, root string, f *fake, rel string, in tag.Info) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in.Path = p
	f.infos[p] = in
	return p
}

func sideOf(t *testing.T, p string) int {
	t.Helper()
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	c, _, err := image.DecodeConfig(fh)
	if err != nil {
		t.Fatal(err)
	}
	return c.Width
}

func TestFolderJpgPicksLargestSquare(t *testing.T) {
	root, f := t.TempDir(), &fake{infos: map[string]tag.Info{}}
	a := song(t, root, f, "A/Al/01 - a.opus", tag.Info{Tags: map[string]string{"album": "Al"}, HasCover: true, CoverW: 3000, CoverH: 2000}) // big, not square
	song(t, root, f, "A/Al/02 - b.opus", tag.Info{Tags: map[string]string{"album": "Al"}, HasCover: true, CoverW: 600, CoverH: 600})
	song(t, root, f, "A/Al/03 - c.opus", tag.Info{Tags: map[string]string{"album": "Al"}, HasCover: true, CoverW: 1000, CoverH: 1000})
	song(t, root, f, "A/Al/04 - d.opus", tag.Info{Tags: map[string]string{"album": "Al"}})
	res, err := Sync(context.Background(), f.opts(), root, []string{a}, true)
	if err != nil || res.JPG != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	jp := filepath.Join(root, "A/Al/folder.jpg")
	if s := sideOf(t, jp); s != 1000 {
		t.Fatalf("folder.jpg side %d, want 1000 (largest square)", s)
	}
	if !owned(jp) {
		t.Fatal("folder.jpg carries no marker")
	}
	if _, err := os.Stat(filepath.Join(root, "A/Al/cover.png")); err == nil {
		t.Fatal("cover.png is off by default")
	}
	// A bigger square cover arrives: replaced. A smaller one: left.
	f.infos[a] = tag.Info{Path: a, Tags: map[string]string{"album": "Al"}, HasCover: true, CoverW: 2000, CoverH: 2000}
	if r, _ := Sync(context.Background(), f.opts(), root, []string{a}, true); r.JPG != 1 {
		t.Fatalf("larger cover should replace: %+v", r)
	}
	if s := sideOf(t, jp); s != CoverPx {
		t.Fatalf("side %d, want capped at %d", s, CoverPx)
	}
}

func TestCoverPNGOptIn(t *testing.T) {
	root, f := t.TempDir(), &fake{infos: map[string]tag.Info{}}
	a := song(t, root, f, "A/Al/01 - a.opus", tag.Info{Tags: map[string]string{}, HasCover: true, CoverW: 800, CoverH: 800})
	o := f.opts()
	o.PNG, o.JPG, o.NFO = true, false, false
	if r, err := Sync(context.Background(), o, root, []string{a}, true); err != nil || r.PNG != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	p := filepath.Join(root, "A/Al/cover.png")
	if sideOf(t, p) != 800 || !owned(p) {
		t.Fatal("cover.png wrong or unmarked")
	}
	if _, err := os.Stat(filepath.Join(root, "A/Al/folder.jpg")); err == nil {
		t.Fatal("folder.jpg was off")
	}
}

func TestNeverOverwritesUsersFiles(t *testing.T) {
	root, f := t.TempDir(), &fake{infos: map[string]tag.Info{}}
	a := song(t, root, f, "A/Al/01 - a.opus", tag.Info{Tags: map[string]string{"album": "Al"}, HasCover: true, CoverW: 900, CoverH: 900})
	dir := filepath.Join(root, "A/Al")
	mine := map[string]string{"folder.jpg": "my picture", "album.nfo": "<album><title>mine</title></album>", "../artist.nfo": "<artist/>"}
	for n, c := range mine {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Sync(context.Background(), f.opts(), root, []string{a}, true)
	if err != nil || r.Changed() != 0 || r.Kept != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	for n, c := range mine {
		if b, _ := os.ReadFile(filepath.Join(dir, n)); string(b) != c {
			t.Fatalf("%s was overwritten", n)
		}
	}
}

func TestIdempotentAndPreview(t *testing.T) {
	root, f := t.TempDir(), &fake{infos: map[string]tag.Info{}}
	a := song(t, root, f, "A/Al/01 - a.opus", tag.Info{Tags: map[string]string{"album": "Al", "title": "a", "genre": "Pop"}, Duration: 61.4, HasCover: true, CoverW: 700, CoverH: 700})
	o := f.opts()
	o.PNG = true
	pre, _ := Sync(context.Background(), o, root, []string{a}, false)
	if pre.Changed() != 4 || exists(filepath.Join(root, "A/Al/album.nfo")) {
		t.Fatalf("preview %+v must count 4 and write nothing", pre)
	}
	first, _ := Sync(context.Background(), o, root, []string{a}, true)
	if first != pre {
		t.Fatalf("preview %+v differs from run %+v", pre, first)
	}
	snap := map[string][]byte{}
	for _, n := range []string{"A/Al/folder.jpg", "A/Al/cover.png", "A/Al/album.nfo", "A/artist.nfo"} {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			t.Fatal(err)
		}
		snap[n] = b
	}
	second, _ := Sync(context.Background(), o, root, []string{a}, true)
	if second.Changed() != 0 || second.Kept != 0 {
		t.Fatalf("second run changed things: %+v", second)
	}
	for n, b := range snap {
		if now, _ := os.ReadFile(filepath.Join(root, n)); !bytes.Equal(now, b) {
			t.Fatalf("%s changed on the second run", n)
		}
	}
	// Tags change: the nfo follows, the images stay.
	in := f.infos[a]
	in.Tags = map[string]string{"album": "Al", "title": "renamed", "genre": "Pop"}
	f.infos[a] = in
	if r, _ := Sync(context.Background(), o, root, []string{a}, true); r.AlbumNFO != 1 || r.JPG != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestSongsOutsideArtistAlbumIgnored(t *testing.T) {
	root, f := t.TempDir(), &fake{infos: map[string]tag.Info{}}
	a := song(t, root, f, "loose.opus", tag.Info{Tags: map[string]string{}})
	b := song(t, root, f, "A/loose.opus", tag.Info{Tags: map[string]string{}})
	if r, _ := Sync(context.Background(), f.opts(), root, []string{a, b}, true); r.Albums != 0 || r.Changed() != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestAlbumNFOGolden(t *testing.T) {
	dir := filepath.Join("root", "Tom & Jerry", "Best <of> \"Both\"")
	mk := func(name string, tags map[string]string, dur float64) tag.Info {
		return tag.Info{Path: filepath.Join(dir, name), Tags: tags, Duration: dur}
	}
	infos := []tag.Info{
		mk("b.opus", map[string]string{"album": "Best <of> \"Both\"", "album_artist": "Tom & Jerry", "title": "Sé & <b>", "track": "1/2", "disc": "2/2", "date": "2021-03-04", "genre": "Pop;Rock", "label": "L'Label", "musicbrainz_albumid": "alb-1", "musicbrainz_albumartistid": "art-1", "musicbrainz_trackid": "trk-3", "isrc": "USX1"}, 200.5),
		mk("a.opus", map[string]string{"album": "Best <of> \"Both\"", "album_artist": "Tom & Jerry", "title": "First\x01 日本", "track": "2/2", "disc": "1/2", "date": "2021", "genre": "pop"}, 59.4),
		mk("c.opus", map[string]string{"album": "Best <of> \"Both\"", "album_artist": "Tom & Jerry", "title": "", "track": "1/2", "disc": "1/2"}, 0),
	}
	want := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<!-- written by ytm -->
<album>
  <title>Best &lt;of&gt; &#34;Both&#34;</title>
  <musicBrainzAlbumID>alb-1</musicBrainzAlbumID>
  <artist>Tom &amp; Jerry</artist>
  <genre>Pop</genre>
  <genre>Rock</genre>
  <label>L&#39;Label</label>
  <year>2021</year>
  <releasedate>2021-03-04</releasedate>
  <albumArtistCredits>
    <artist>Tom &amp; Jerry</artist>
    <musicBrainzArtistID>art-1</musicBrainzArtistID>
  </albumArtistCredits>
  <track>
    <position>1</position>
    <disc>1</disc>
    <title>c</title>
  </track>
  <track>
    <position>2</position>
    <disc>1</disc>
    <title>First 日本</title>
    <duration>59</duration>
  </track>
  <track>
    <position>1</position>
    <disc>2</disc>
    <title>Sé &amp; &lt;b&gt;</title>
    <duration>201</duration>
    <musicBrainzTrackID>trk-3</musicBrainzTrackID>
    <isrc>USX1</isrc>
  </track>
</album>
`
	got := albumNFO(dir, infos)
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if !bytes.Equal(got, albumNFO(dir, []tag.Info{infos[2], infos[1], infos[0]})) {
		t.Fatal("output depends on input order")
	}
	var v struct{ XMLName xml.Name }
	if err := xml.Unmarshal(got, &v); err != nil || v.XMLName.Local != "album" {
		t.Fatalf("not well-formed XML: %v", err)
	}
}

func TestArtistNFOGolden(t *testing.T) {
	infos := []tag.Info{
		{Tags: map[string]string{"album_artist": "AC/DC & Co", "genre": "Rock", "musicbrainz_albumartistid": "mb-9"}},
		{Tags: map[string]string{"album_artist": "AC/DC & Co", "genre": "Blues;rock"}},
	}
	want := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<!-- written by ytm -->
<artist>
  <name>AC/DC &amp; Co</name>
  <musicBrainzArtistID>mb-9</musicBrainzArtistID>
  <genre>Blues</genre>
  <genre>Rock</genre>
</artist>
`
	if got := string(artistNFO("x/AC_DC", infos)); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if !strings.Contains(string(artistNFO("x/Folder Name", nil)), "<name>Folder Name</name>") {
		t.Fatal("artist name falls back to the folder")
	}
}
