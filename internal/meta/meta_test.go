package meta

import "testing"

const (
	mbTrack = "5d7c1c36-8b0e-4a4f-9a1f-2f1f3f9a1e11"
	mbAlbum = "7e8d9c10-1111-4222-8333-944455556666"
)

// The Pronoun Palace case: a YouTube copy with MusicBrainz ids, an ISRC, a genre and a
// small cover is replaced by the purchased file, which has good tags and a big cover but
// no ids.
func TestMergeYouTubeToBandcampPurchase(t *testing.T) {
	yt := Set{Source: YouTube, CoverW: 544, CoverH: 544, Tags: map[string]string{
		"title": "Pronoun Palace (Official Audio)", "artist": "Composer - Topic", "album": "Pronoun Palace",
		"date": "2021-03-02", "genre": "Soundtrack", "isrc": "USRC12100001", "musicbrainz_trackid": mbTrack,
		"musicbrainz_albumid": mbAlbum, "comment": "https://music.youtube.com/watch?v=abcdefghijk",
		"lyrics": "la la la", "track": "3",
	}}
	bc := Set{Source: BandcampPurchase, CoverW: 1400, CoverH: 1400, Tags: map[string]string{
		"title": "Pronoun Palace", "artist": "Composer", "album": "Pronoun Palace", "album_artist": "Composer",
		"date": "2021", "track": "3/12", "disc": "1/1",
	}}
	m := Merge(bc, yt) // the purchase is the file that stays
	want := map[string]string{
		"title": "Pronoun Palace", "artist": "Composer", "album": "Pronoun Palace", "album_artist": "Composer",
		"date":  "2021", // earliest year, but the purchase's more exact date does not exist for 2021 alone...
		"genre": "Soundtrack", "isrc": "USRC12100001", "musicbrainz_trackid": mbTrack, "musicbrainz_albumid": mbAlbum,
		"comment": "https://music.youtube.com/watch?v=abcdefghijk", "lyrics": "la la la", "track": "3/12", "disc": "1/1",
	}
	// YouTube's upload date is never preferred over a real release year
	for k, v := range want {
		if m.Tags[k] != v {
			t.Errorf("%s = %q, want %q (from %s)", k, m.Tags[k], v, m.From[k])
		}
	}
	if m.Cover != 0 || m.From["cover"] != BandcampPurchase {
		t.Errorf("cover should be the purchase's 1400 px one, got index %d", m.Cover)
	}
	if m.From["isrc"] != YouTube || m.From["title"] != BandcampPurchase {
		t.Errorf("provenance wrong: %v", m.From)
	}
}

func TestMergeDate(t *testing.T) {
	for _, c := range []struct {
		name string
		sets []Set
		want string
	}{
		{"upload date is the last resort", []Set{{Source: YouTube, Tags: map[string]string{"date": "2023-05-01"}}}, "2023-05-01"},
		{"release year beats upload date", []Set{
			{Source: YouTube, Tags: map[string]string{"date": "2023-05-01"}},
			{Source: Apple, Tags: map[string]string{"date": "2019"}},
		}, "2019"},
		{"same day known exactly wins", []Set{
			{Source: Apple, Tags: map[string]string{"date": "2019"}},
			{Source: MusicBrainz, Tags: map[string]string{"date": "2019-06-07"}},
		}, "2019-06-07"},
		{"a reissue year does not replace the original", []Set{
			{Source: Apple, Tags: map[string]string{"date": "2015-01-01"}},
			{Source: Spotify, Tags: map[string]string{"date": "2009-03-03"}},
		}, "2009-03-03"},
		{"junk is ignored", []Set{{Source: Apple, Tags: map[string]string{"date": "n/a"}}}, ""},
	} {
		if got := Merge(c.sets...).Tags["date"]; got != c.want {
			t.Errorf("%s: date = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMergeIdentifiers(t *testing.T) {
	m := Merge(
		Set{Source: Apple, Tags: map[string]string{"isrc": "not-an-isrc", "musicbrainz_trackid": "garbage"}},
		Set{Source: YouTube, Tags: map[string]string{"isrc": "gb-abc-12-34567", "musicbrainz_recordingid": mbTrack}},
	)
	if m.Tags["isrc"] != "GBABC1234567" {
		t.Errorf("isrc = %q", m.Tags["isrc"])
	}
	if m.Tags["musicbrainz_trackid"] != mbTrack {
		t.Errorf("an old recording id should become the track id, got %q", m.Tags["musicbrainz_trackid"])
	}
}

func TestMergeArtistKeepsFeatured(t *testing.T) {
	m := Merge(
		Set{Source: Apple, Tags: map[string]string{"artist": "Drake"}},
		Set{Source: YouTube, Tags: map[string]string{"artist": "Drake; Rihanna"}},
	)
	if m.Tags["artist"] != "Drake; Rihanna" {
		t.Errorf("artist = %q, the featured artist was lost", m.Tags["artist"])
	}
	// a different artist is not "more"
	m = Merge(
		Set{Source: Apple, Tags: map[string]string{"artist": "Drake"}},
		Set{Source: YouTube, Tags: map[string]string{"artist": "Someone Else; Rihanna"}},
	)
	if m.Tags["artist"] != "Drake" {
		t.Errorf("artist = %q", m.Tags["artist"])
	}
}

func TestMergeTrackTotalFromAgreeingProvider(t *testing.T) {
	m := Merge(
		Set{Source: Apple, Tags: map[string]string{"track": "4"}},
		Set{Source: YouTube, Tags: map[string]string{"track": "4/10"}},
		Set{Source: Spotify, Tags: map[string]string{"track": "9/99"}},
	)
	if m.Tags["track"] != "4/10" {
		t.Errorf("track = %q", m.Tags["track"])
	}
}

func TestMergeCoverPrefersSquareAndLarge(t *testing.T) {
	m := Merge(
		Set{Source: Apple, CoverW: 1200, CoverH: 675}, // a video thumbnail
		Set{Source: YouTube, CoverW: 600, CoverH: 600},
	)
	if m.Cover != 1 {
		t.Errorf("cover = %d, want the square one", m.Cover)
	}
	if m := Merge(Set{Source: Apple}, Set{Source: YouTube}); m.Cover != -1 {
		t.Errorf("no pictures: cover = %d", m.Cover)
	}
}

func TestMergeEmptyNeverOverrides(t *testing.T) {
	m := Merge(
		Set{Source: BandcampPurchase, Tags: map[string]string{"genre": " ", "album": ""}},
		Set{Source: YouTube, Tags: map[string]string{"genre": "Rock", "album": "Hits"}},
	)
	if m.Tags["genre"] != "Rock" || m.Tags["album"] != "Hits" {
		t.Errorf("got %v", m.Tags)
	}
}

func TestMergeEqualTrustBaseWins(t *testing.T) {
	m := Merge(
		Set{Source: "unknown-a", Tags: map[string]string{"title": "A"}},
		Set{Source: "unknown-b", Tags: map[string]string{"title": "B"}},
	)
	if m.Tags["title"] != "A" {
		t.Errorf("title = %q, the base file should win among equals", m.Tags["title"])
	}
}

func TestDiff(t *testing.T) {
	d := Diff(map[string]string{"title": "x", "genre": "keep"}, map[string]string{"title": "x", "isrc": "I", "genre": "Rock"})
	if len(d) != 2 || d["isrc"] != "I" || d["genre"] != "Rock" {
		t.Errorf("diff = %v", d)
	}
}
