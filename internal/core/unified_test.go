package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/store"
)

func TestSplitArtists(t *testing.T) {
	got := splitArtists("Daft Punk, Pharrell Williams & Nile Rodgers")
	want := []string{"Daft Punk, Pharrell Williams & Nile Rodgers", "Daft Punk", "Pharrell Williams", "Nile Rodgers"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	if got := splitArtists("Tame Impala - Topic"); !reflect.DeepEqual(got, []string{"Tame Impala"}) {
		t.Errorf("topic channel: got %q", got)
	}
	if splitArtists("  ") != nil {
		t.Error("blank credit should give nothing")
	}
}

// planUnified must recognise a song however it was downloaded before, and upgrade only
// when the new source is better.
func TestPlanUnified(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(appname.StateDir(root))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	touch := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// A YouTube library song (credited jointly), and an Apple download.
	ytFile := touch("ytsong.opus")
	_ = st.AddPending("aaaaaaaaaaa", "Get Lucky", "Daft Punk, Pharrell Williams")
	_ = st.MarkDone("aaaaaaaaaaa", ytFile)
	appleFile := touch("applesong.m4a")
	_ = st.AppleDone("999", "Borderline", "Tame Impala", appleFile)
	// A library song whose file is gone: not held.
	_ = st.AddPending("bbbbbbbbbbb", "Gone", "Nobody")
	_ = st.MarkDone("bbbbbbbbbbb", filepath.Join(root, "missing.opus"))

	sp := func(title, artist string) catalog.Track {
		return catalog.Track{Title: title, Artists: []string{artist}, Origin: catalog.Spotify, OriginID: title}
	}
	items := []item{
		{t: sp("Get Lucky", "Pharrell Williams"), target: catalog.YouTube},                    // held from YouTube
		{t: sp("Get Lucky", "Daft Punk"), target: catalog.Apple, match: apple.Track{ID: "1"}}, // upgrade
		{t: sp("Borderline", "Tame Impala"), target: catalog.YouTube},                         // held from Apple
		{t: sp("Other", "Tame Impala"), target: catalog.Apple, match: apple.Track{ID: "999"}}, // held by Apple id
		{t: sp("Gone", "Nobody"), target: catalog.YouTube},                                    // file missing
	}
	for i := range items {
		items[i].key = items[i].t.Key()
	}
	appleItems, ytItems, held, upgrades := planUnified(st, items, true)
	if held != 3 || upgrades != 1 || len(appleItems) != 1 || len(ytItems) != 1 {
		t.Fatalf("held=%d upgrades=%d apple=%d youtube=%d", held, upgrades, len(appleItems), len(ytItems))
	}
	up := appleItems[0]
	if !up.upgrade || up.oldPath != ytFile || up.oldID != "aaaaaaaaaaa" {
		t.Errorf("upgrade item = %+v", up)
	}
	if ytItems[0].t.Title != "Gone" {
		t.Errorf("youtube item = %+v", ytItems[0].t)
	}
	// What was found indirectly is now remembered under the song's own key.
	if lib := st.Library(items[0].key); !lib.Done || lib.Path != ytFile || lib.Source != "youtube" {
		t.Errorf("library row = %+v", lib)
	}
}

func TestSameSong(t *testing.T) {
	sp := catalog.Track{Title: "Gossip", Artists: []string{"Tame Impala"}, Album: "Currents", DurationMillis: 55000}
	am := catalog.Track{Title: "Gossip", Artists: []string{"Tame Impala"}, Album: "Currents", DurationMillis: 55500, ISRC: "X"}
	if !sameSong(sp, am) {
		t.Error("same song from Spotify and Apple Music")
	}
	single := catalog.Track{Title: "Gossip", Album: "Gossip - Single"}
	if !sameSong(catalog.Track{Title: "Gossip", Album: "Gossip"}, single) {
		t.Error("Apple's ' - Single' suffix should not make albums differ")
	}
	if sameSong(sp, single) {
		t.Error("different albums are different songs")
	}
	live := sp
	live.DurationMillis = 70000
	if sameSong(sp, live) {
		t.Error("a much longer version is a different recording")
	}
}
