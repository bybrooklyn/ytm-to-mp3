package tag

import "testing"

func rec(id, title, artist string, score, lengthMs int, rels ...mbRelease) mbRecording {
	r := mbRecording{ID: id, Title: title, Score: score, Length: lengthMs, Releases: rels}
	r.ArtistCredit = append(r.ArtistCredit, struct {
		Name   string `json:"name"`
		Artist struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artist"`
	}{Name: artist})
	r.ArtistCredit[0].Artist.Name = artist
	return r
}

func album(id, title, date string) mbRelease {
	r := mbRelease{ID: id, Title: title, Date: date, Status: "Official"}
	r.ReleaseGroup.PrimaryType = "Album"
	return r
}

func TestCleanTitle(t *testing.T) {
	cases := [][3]string{
		{"Rick Astley - Never Gonna Give You Up (Official Video)", "Rick Astley", "Never Gonna Give You Up"},
		{"Song [HD]", "X", "Song"},
		{"Song (Remastered 2011)", "X", "Song (Remastered 2011)"},
		{"Song (Live)", "X", "Song (Live)"},
		{"Song (Official Music Video) ", "X", "Song"},
	}
	for _, c := range cases {
		if got := CleanTitle(c[0], c[1]); got != c[2] {
			t.Errorf("CleanTitle(%q) = %q, want %q", c[0], got, c[2])
		}
	}
	if CleanArtist("Rick Astley - Topic") != "Rick Astley" {
		t.Error("topic suffix")
	}
}

func TestDecideStrict(t *testing.T) {
	good := rec("r1", "Never Gonna Give You Up", "Rick Astley", 100, 213000, album("a1", "Whenever You Need Somebody", "1987-11-12"))

	if m, why := Decide("Never Gonna Give You Up", "Rick Astley", "", 213.4, []mbRecording{good}); m == nil || m.Release == nil || m.Release.ID != "a1" {
		t.Fatalf("expected confident match with release, got %v (%s)", m, why)
	}
	// Each of these must refuse to match, leaving YouTube's tags alone.
	refuse := map[string]struct {
		title, artist string
		dur           float64
		rec           mbRecording
	}{
		"low score":      {"Never Gonna Give You Up", "Rick Astley", 213, rec("r", "Never Gonna Give You Up", "Rick Astley", 90, 213000)},
		"wrong length":   {"Never Gonna Give You Up", "Rick Astley", 260, good},
		"wrong artist":   {"Never Gonna Give You Up", "Someone Else", 213, good},
		"wrong title":    {"Never Gonna Give", "Rick Astley", 213, good},
		"no MB length":   {"Never Gonna Give You Up", "Rick Astley", 213, rec("r", "Never Gonna Give You Up", "Rick Astley", 100, 0)},
		"missing artist": {"Never Gonna Give You Up", "", 213, good},
	}
	for name, c := range refuse {
		if m, _ := Decide(c.title, c.artist, "", c.dur, []mbRecording{c.rec}); m != nil {
			t.Errorf("%s: should not match", name)
		}
	}
	// Two equally good recordings: don't guess.
	other := rec("r2", "Never Gonna Give You Up", "Rick Astley", 100, 213500)
	if m, why := Decide("Never Gonna Give You Up", "Rick Astley", "", 213, []mbRecording{good, other}); m != nil || why == "" {
		t.Error("ambiguous recordings must not match")
	}
}

func TestPickReleaseAmbiguous(t *testing.T) {
	rels := []mbRelease{album("a1", "Album One", "1990"), album("a2", "Greatest Hits", "1995")}
	if r, _, _ := pickRelease(rels, ""); r != nil {
		t.Error("two different albums and no hint: leave album alone")
	}
	if r, _, _ := pickRelease(rels, "greatest hits"); r == nil || r.ID != "a2" {
		t.Error("existing album should select the matching release")
	}
}
