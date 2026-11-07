package lyrics

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func synced(n int, step time.Duration) []Line {
	var out []Line
	for i := 0; i < n; i++ {
		out = append(out, Line{At: time.Duration(i) * step, Text: fmt.Sprintf("line %d", i)})
	}
	return out
}

func plain(n int) string {
	var b []string
	for i := 0; i < n; i++ {
		b = append(b, fmt.Sprintf("line %d", i))
	}
	return strings.Join(b, "\n")
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Bloom", "bloom"},
		{"Bloom (Remastered 2011)", "bloom"},
		{"Bloom - Remastered 2011", "bloom"},
		{"Bloom [Official Audio]", "bloom"},
		{"Bloom (feat. Beyoncé)", "bloom"},
		{"Bloom feat. Beyoncé", "bloom"},
		{"Radiohead - Topic", "radiohead"},
		{"Bloom (Live at Glastonbury)", "bloom"},
		{"Everything (Turning in Somersaults)", "everything turning in somersaults"},
		{"Don't Stop Me Now!", "don t stop me now"},
		{"Jay-Z", "jay z"},
		{"Soft Rock", "soft rock"},
		{"Lift Me Up", "lift me up"},
		{"  ", ""},
		{"Song - 2011 Remaster", "song"},
		{"Song - Single Version", "song"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVersions(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Bloom", ""},
		{"Bloom (Remastered 2011)", ""},
		{"Bloom (feat. X)", ""},
		{"Bloom (Live)", "live"},
		{"Bloom - Live at Glastonbury", "live"},
		{"Bloom (Acoustic) [Remastered]", "acoustic"},
		{"Bloom (Skrillex Remix)", "remix"},
		{"Bloom (Sped Up)", "sped"},
		{"Bloom - Instrumental", "instrumental"},
		{"Live and Let Die", ""}, // not in a bracket or after a dash
		{"Bloom (Part A)", ""},
	}
	for _, c := range cases {
		if got := strings.Join(Versions(c.in), ","); got != c.want {
			t.Errorf("Versions(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b     string
		min, max int
	}{
		{"Song Name (Live)", "song-name", 1000, 1000},
		{"Song Name Remastered", "Song Name", 800, 1000},
		{"Totally Different", "Song Name", 0, 599},
		{"", "x", 500, 500},
		{"Radiohead", "Radiohead - Topic", 1000, 1000},
	}
	for _, c := range cases {
		if got := Similarity(c.a, c.b); got < c.min || got > c.max {
			t.Errorf("Similarity(%q,%q) = %d, want %d..%d", c.a, c.b, got, c.min, c.max)
		}
	}
	if artistSimilarity("Daft Punk", "Pharrell Williams, Daft Punk, Nile Rodgers") != 1000 {
		t.Error("a credit list should match any member")
	}
	if artistSimilarity("Beyoncé", "Beyoncé feat. Jay-Z") != 1000 {
		t.Error("feat. artist")
	}
}

func cand(provider string, rank int, title, artist string, dur time.Duration) Candidate {
	return Candidate{Provider: provider, Rank: rank, Title: title, Artist: artist, Duration: dur}
}

func TestChoose(t *testing.T) {
	q := Query{Title: "Bloom", Artist: "Radiohead", Album: "The King of Limbs", Duration: 215 * time.Second}
	good := func() Candidate {
		c := cand("LRCLIB", 0, "Bloom", "Radiohead", 215*time.Second)
		c.Synced = synced(30, 7*time.Second)
		return c
	}
	with := func(f func(*Candidate)) Candidate { c := good(); f(&c); return c }

	cases := []struct {
		name     string
		cands    []Candidate
		query    Query
		wantOK   bool
		wantKind Kind
		wantFrom string
	}{
		{"exact synced", []Candidate{good()}, q, true, KindSynced, "LRCLIB"},
		{"synced within 2 s", []Candidate{with(func(c *Candidate) { c.Duration = 217 * time.Second })}, q, true, KindSynced, "LRCLIB"},
		{"synced 5 s off becomes plain (stamps would drift)", []Candidate{with(func(c *Candidate) {
			c.Duration = 220 * time.Second
			c.Plain = plain(30)
		})}, q, true, KindPlain, "LRCLIB"},
		{"synced 5 s off with no plain text still gives the words", []Candidate{with(func(c *Candidate) { c.Duration = 220 * time.Second })}, q, true, KindPlain, "LRCLIB"},
		{"extended mix 60 s off rejected", []Candidate{with(func(c *Candidate) { c.Duration = 275 * time.Second })}, q, false, "", ""},
		{"last stamp past the end of the song means another cut", []Candidate{with(func(c *Candidate) { c.Synced = synced(30, 10*time.Second) })}, q, true, KindPlain, "LRCLIB"},
		{"live version of a studio query rejected", []Candidate{with(func(c *Candidate) { c.Title = "Bloom (Live)" })}, q, false, "", ""},
		{"studio record for a live query rejected", []Candidate{good()}, Query{Title: "Bloom (Live)", Artist: "Radiohead", Duration: 215 * time.Second}, false, "", ""},
		{"remix rejected", []Candidate{with(func(c *Candidate) { c.Title = "Bloom - Jamie xx Remix" })}, q, false, "", ""},
		{"remastered title is the same song", []Candidate{with(func(c *Candidate) { c.Title = "Bloom (Remastered 2011)" })}, q, true, KindSynced, "LRCLIB"},
		{"feat credit in the title is the same song", []Candidate{good()}, Query{Title: "Bloom (feat. Thom)", Artist: "Radiohead", Duration: 215 * time.Second}, true, KindSynced, "LRCLIB"},
		{"different artist rejected", []Candidate{with(func(c *Candidate) { c.Artist = "The Paper Kites" })}, q, false, "", ""},
		{"different song rejected", []Candidate{with(func(c *Candidate) { c.Title = "Codex" })}, q, false, "", ""},
		{"stub record ignored", []Candidate{with(func(c *Candidate) { c.Synced, _ = ParseLRC("[00:00.00]probe"); c.Plain = "probe" })}, q, false, "", ""},
		{"synced beats longer plain", []Candidate{
			with(func(c *Candidate) { c.Rank = 1; c.Provider = "yt"; c.Synced = nil; c.Plain = plain(90) }), good()}, q, true, KindSynced, "LRCLIB"},
		{"closest length wins among synced", []Candidate{
			with(func(c *Candidate) {
				c.Provider = "far"
				c.Duration = 217 * time.Second
				c.Synced = synced(40, 5*time.Second)
			}),
			with(func(c *Candidate) { c.Provider = "near"; c.Duration = 215 * time.Second })}, q, true, KindSynced, "near"},
		{"provider order breaks a tie", []Candidate{
			with(func(c *Candidate) { c.Provider = "second"; c.Rank = 1 }),
			with(func(c *Candidate) { c.Provider = "first"; c.Rank = 0 })}, q, true, KindSynced, "first"},
		{"unknown record length is allowed", []Candidate{with(func(c *Candidate) { c.Duration = 0 })}, q, true, KindSynced, "LRCLIB"},
		{"unknown song length is allowed", []Candidate{good()}, Query{Title: "Bloom", Artist: "Radiohead"}, true, KindSynced, "LRCLIB"},
		{"instrumental alone", []Candidate{with(func(c *Candidate) { c.Synced, c.Instrumental = nil, true })}, q, true, KindInstrumental, "LRCLIB"},
		{"words beat an instrumental flag from another record", []Candidate{
			with(func(c *Candidate) { c.Synced, c.Instrumental, c.Rank = nil, true, 1 }), good()}, q, true, KindSynced, "LRCLIB"},
		{"instrumental of another length is not believed", []Candidate{with(func(c *Candidate) { c.Synced, c.Instrumental, c.Duration = nil, true, 300*time.Second })}, q, false, "", ""},
		{"nothing", nil, q, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := Choose(c.cands, c.query)
			if ok != c.wantOK || p.Kind != c.wantKind || (ok && p.Provider != c.wantFrom) {
				t.Fatalf("got ok=%v kind=%q from=%q; want ok=%v kind=%q from=%q", ok, p.Kind, p.Provider, c.wantOK, c.wantKind, c.wantFrom)
			}
		})
	}
}

func TestSearchTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Bloom (Remastered 2011)", "Bloom"},
		{"Bloom (feat. Thom Yorke)", "Bloom"},
		{"Bloom feat. Thom Yorke", "Bloom"},
		{"Bloom - 2011 Remaster", "Bloom"},
		{"Bloom (Live)", "Bloom (Live)"},
		{"Everything (Turning in Somersaults)", "Everything (Turning in Somersaults)"},
		{"Drift Away", "Drift Away"},
	}
	for _, c := range cases {
		if got := searchTitle(c.in); got != c.want {
			t.Errorf("searchTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
