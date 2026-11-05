package tag

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGoodCover(t *testing.T) {
	for _, c := range []struct {
		i    Info
		good bool
	}{
		{Info{}, false},
		{Info{HasCover: true}, true}, // size unknown: leave it
		{Info{HasCover: true, CoverW: 1200, CoverH: 1200}, true},
		{Info{HasCover: true, CoverW: 1280, CoverH: 720}, false}, // a video frame
		{Info{HasCover: true, CoverW: 300, CoverH: 300}, false},  // too small
		{Info{HasCover: true, CoverW: 601, CoverH: 600}, true},   // rounding
	} {
		if c.i.GoodCover() != c.good {
			t.Errorf("%+v: GoodCover = %v", c.i, !c.good)
		}
	}
}

func TestSameTrack(t *testing.T) {
	vorbis := Info{Tags: map[string]string{"track": "2", "tracktotal": "9"}}
	if !sameTrack("track", "2/9", vorbis) || sameTrack("track", "3/9", vorbis) {
		t.Error("a Vorbis TRACKNUMBER+TRACKTOTAL must compare equal to N/T")
	}
	if !sameTrack("track", "2/9", Info{Tags: map[string]string{"track": "2/9"}}) || sameTrack("title", "x", vorbis) {
		t.Error("sameTrack is only for track and disc")
	}
}

func TestEnrichAlbumArtistAndIDs(t *testing.T) {
	r := rec("rec1", "Song", "Guest", 100, 200000)
	r.ArtistCredit[0].Artist.ID = "art1"
	comp := album("rel1", "Hits", "2001")
	comp.ArtistCredit = append(comp.ArtistCredit, r.ArtistCredit[0])
	comp.ArtistCredit[0].Artist.Name, comp.ArtistCredit[0].Artist.ID = "Various Artists", "va"
	comp.Media = append(comp.Media, struct {
		Position   int `json:"position"`
		TrackCount int `json:"track-count"`
		Track      []struct {
			Number string `json:"number"`
		} `json:"track"`
	}{Position: 1, TrackCount: 14})

	pr := &proposal{set: map[string]string{}}
	info := Info{Tags: map[string]string{"album_artist": "Guest", "musicbrainz_recordingid": "old"}}
	enrich(pr, info, &Match{Recording: r, Release: &comp, Track: "5", Disc: 1}, "Guest")
	for k, want := range map[string]string{
		"album_artist": "Various Artists", "musicbrainz_albumartistid": "va", "musicbrainz_trackid": "rec1",
		"musicbrainz_recordingid": "", "musicbrainz_albumid": "rel1", "musicbrainz_artistid": "art1",
	} {
		if v, ok := pr.set[k]; !ok || v != want {
			t.Errorf("%s = %q (set %v), want %q", k, v, ok, want)
		}
	}
	if pr.set["track"] != "5/14" {
		t.Errorf("track = %q, want 5/14 (the disc's track count from the release)", pr.set["track"])
	}

	// no release credit: a matching album artist is only re-spelled, a different one kept
	pr = &proposal{set: map[string]string{}}
	plain := album("rel2", "LP", "2001")
	enrich(pr, Info{Tags: map[string]string{"album_artist": "Band"}}, &Match{Recording: r, Release: &plain}, "Guest")
	if _, ok := pr.set["album_artist"]; ok {
		t.Errorf("a featured artist must not replace the album artist: %v", pr.set)
	}
}

// TestWriteStandardKeys writes ISRC and MusicBrainz ids to an mp3 and reads them back
// under ytm's names: ISRC as the real TSRC frame, the ids as Picard's TXXX names.
func TestWriteStandardKeys(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("no ffmpeg/ffprobe")
	}
	p := filepath.Join(t.TempDir(), "a.mp3")
	if err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "sine=d=1", "-c:a", "libmp3lame", p).Run(); err != nil {
		t.Skip("cannot make mp3: ", err)
	}
	ctx := context.Background()
	set := map[string]string{"isrc": "USABC1900001", "musicbrainz_trackid": "rec1", "musicbrainz_albumid": "rel1", "track": "3/12"}
	if err := Write(ctx, ffmpeg, ffprobe, p, set, ""); err != nil {
		t.Fatal(err)
	}
	info, err := Probe(ctx, ffprobe, p)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range set {
		if info.Tags[k] != want {
			t.Errorf("%s = %q, want %q (all: %v)", k, info.Tags[k], want, info.Tags)
		}
	}
}
