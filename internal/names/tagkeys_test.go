package names

import "testing"

func TestTagsFor(t *testing.T) {
	in := map[string]string{"title": "T", "track": "2/12", "disc": "1/2", "isrc": "USABC1234567",
		"musicbrainz_trackid": "rec", "label": "L", "explicit": "1"}

	v := TagsFor("opus", in)
	for k, want := range map[string]string{"track": "2", "tracktotal": "12", "disc": "1", "disctotal": "2",
		"isrc": "USABC1234567", "musicbrainz_trackid": "rec", "label": "L"} {
		if v[k] != want {
			t.Errorf("opus %s = %q, want %q", k, v[k], want)
		}
	}
	if _, ok := v["explicit"]; ok {
		t.Error("opus has no explicit field")
	}

	m := TagsFor(".mp3", in)
	for k, want := range map[string]string{"track": "2/12", "TSRC": "USABC1234567",
		"MusicBrainz Track Id": "rec", "publisher": "L"} {
		if m[k] != want {
			t.Errorf("mp3 %s = %q, want %q", k, m[k], want)
		}
	}
	if _, ok := m["isrc"]; ok {
		t.Error("mp3 isrc must go to TSRC, not a TXXX frame")
	}

	a := TagsFor("m4a", in)
	if a["title"] != "T" || a["track"] != "2/12" || len(a) != 3 {
		t.Errorf("m4a keeps only what MP4 atoms hold: %v", a)
	}
	for _, k := range []string{"isrc", "musicbrainz_trackid", "label"} {
		if TagSupported("m4a", k) {
			t.Errorf("m4a can't store %s", k)
		}
	}
	if !TagSupported("mp3", "isrc") || TagSupported("mp3", "explicit") || !TagSupported("flac", "isrc") {
		t.Error("TagSupported disagrees with TagsFor")
	}
}

func TestSplitArtists(t *testing.T) {
	for in, want := range map[string][]string{
		"A; B":                {"A", "B"},
		"A, B & C":            {"A", "B", "C"},
		"A feat. B":           {"A", "B"},
		"A ft. B x C":         {"A", "B", "C"},
		"A / B":               {"A", "B"},
		"Adele - Topic":       {"Adele"},
		"Lil Nas X":           {"Lil Nas X"},
		"A, a":                {"A"},
		"Tyler, The Creator ": {"Tyler", "The Creator"}, // ambiguous: callers keep the whole credit too
	} {
		got := SplitArtists(in)
		if len(got) != len(want) {
			t.Errorf("SplitArtists(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("SplitArtists(%q) = %q, want %q", in, got, want)
			}
		}
	}
}

func TestCanonicalTag(t *testing.T) {
	for in, want := range map[string]string{"TSRC": "isrc", "MusicBrainz Album Id": "musicbrainz_albumid",
		"MUSICBRAINZ_TRACKID": "musicbrainz_trackid", "TRACKTOTAL": "tracktotal", "Title": "title", "publisher": "label"} {
		if got := CanonicalTag(in); got != want {
			t.Errorf("CanonicalTag(%q) = %q, want %q", in, got, want)
		}
	}
}
