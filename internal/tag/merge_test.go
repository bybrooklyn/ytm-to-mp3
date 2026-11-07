package tag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// put writes a file with a modification time `age` ago (older = downloaded earlier).
func put(t *testing.T, root, rel string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// caseSensitive reports whether dir's filesystem tells "a" and "A" apart.
func caseSensitive(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, "cs"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(dir, "cs"))
	_, err := os.Stat(filepath.Join(dir, "CS"))
	return err != nil
}

func testOpts(root string, apply bool) (Options, struct{}, *[][2]string) {
	var moves [][2]string
	return Options{Dir: root, Apply: apply, UI: ui.New(), OnMove: func(a, b string) { moves = append(moves, [2]string{a, b}) }}, struct{}{}, &moves
}

func TestMergeCaseVariantFoldersKeepsFirstDownloaded(t *testing.T) {
	root := t.TempDir()
	if !caseSensitive(t, root) {
		t.Skip("case-insensitive filesystem: variants cannot coexist")
	}
	// The all-caps folder was downloaded first (older files) and has 2 songs; the other has
	// 1 new song and one that is in both.
	put(t, root, "MASTER BOOT RECORD/Album/01 - Alpha.opus", 100*time.Hour)
	put(t, root, "MASTER BOOT RECORD/Album/02 - Beta.opus", 99*time.Hour)
	put(t, root, "Master Boot Record/Album/03 - Gamma.opus", time.Hour)
	put(t, root, "Master Boot Record/album/01 - ALPHA.m4a", time.Hour) // the same song again
	put(t, root, "meganeko/Single/Song.opus", 50*time.Hour)
	put(t, root, "Meganeko/Single/Other.opus", 2*time.Hour)

	groups, err := PlanMerges(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) == 0 {
		t.Fatal("no merge planned")
	}

	// preview writes nothing
	o, _, _ := testOpts(root, false)
	if _, err := MergeFolders(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !exists(root, "Master Boot Record/Album/03 - Gamma.opus") {
		t.Fatal("preview moved files")
	}

	o, _, moved := testOpts(root, true)
	if _, err := MergeFolders(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"MASTER BOOT RECORD/Album/01 - Alpha.opus", "MASTER BOOT RECORD/Album/02 - Beta.opus",
		"MASTER BOOT RECORD/Album/03 - Gamma.opus", "meganeko/Single/Song.opus", "meganeko/Single/Other.opus",
	} {
		if !exists(root, rel) {
			t.Errorf("missing %s after merge", rel)
		}
	}
	if exists(root, "Master Boot Record") || exists(root, "Meganeko") {
		t.Error("the newer spellings should be gone")
	}
	// the duplicate went to .ytm/replaced, not away
	if !exists(root, ".ytm/replaced/Master Boot Record/album/01 - ALPHA.m4a") {
		t.Error("duplicate not kept in .ytm/replaced")
	}
	if len(*moved) != 3 {
		t.Errorf("the store should hear of every move, got %v", *moved)
	}

	// undo puts everything back
	o, _, _ = testOpts(root, true)
	o.Undo = true
	if err := undo(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"Master Boot Record/Album/03 - Gamma.opus", "Master Boot Record/album/01 - ALPHA.m4a",
		"Meganeko/Single/Other.opus", "MASTER BOOT RECORD/Album/01 - Alpha.opus", "meganeko/Single/Song.opus",
	} {
		if !exists(root, rel) {
			t.Errorf("undo did not restore %s", rel)
		}
	}
	if exists(root, ".ytm/replaced/Master Boot Record/album/01 - ALPHA.m4a") {
		t.Error("undo left the duplicate in .ytm/replaced")
	}
}

func TestMergeEditionsPrefersFullest(t *testing.T) {
	root := t.TempDir()
	put(t, root, "Tyler, The Creator/CHROMAKOPIA/01 - St. Chroma.opus", 200*time.Hour)
	put(t, root, "Tyler, The Creator/CHROMAKOPIA/02 - Noid.opus", 200*time.Hour)
	for i, n := range []string{"St. Chroma", "Rah Tah Tah", "Noid", "Darling, I", "Hey Jane", "I Killed You"} {
		put(t, root, "Tyler, The Creator/CHROMAKOPIA +/"+string(rune('0'+i+1))+" - "+n+".opus", time.Hour)
	}
	// a genuinely different album must stay alone
	put(t, root, "Tyler, The Creator/CHROMAKOPIA (Instrumentals)/01 - St. Chroma.opus", time.Hour)

	groups, err := PlanMerges(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || filepath.Base(groups[0].Target) != "CHROMAKOPIA +" {
		t.Fatalf("want one merge into the deluxe, got %+v", groups)
	}
	o, _, _ := testOpts(root, true)
	if _, err := MergeFolders(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if exists(root, "Tyler, The Creator/CHROMAKOPIA") {
		t.Error("standard edition folder should be merged away")
	}
	// Noid is already in the deluxe: the standard copy is kept aside, nothing is lost
	if !exists(root, "Tyler, The Creator/CHROMAKOPIA +/3 - Noid.opus") || !exists(root, ".ytm/replaced/Tyler, The Creator/CHROMAKOPIA/02 - Noid.opus") {
		t.Error("duplicate handling wrong")
	}
	if !exists(root, "Tyler, The Creator/CHROMAKOPIA (Instrumentals)/01 - St. Chroma.opus") {
		t.Error("a different album was touched")
	}
}

func TestMergeNothingToDo(t *testing.T) {
	root := t.TempDir()
	put(t, root, "A/One/01 - x.opus", time.Hour)
	put(t, root, "B/Two/01 - y.opus", time.Hour)
	groups, err := PlanMerges(root)
	if err != nil || len(groups) != 0 {
		t.Fatalf("want no groups, got %v %v", groups, err)
	}
}

func TestMergeArtistSpellingMovesAlbumsAndLooseFiles(t *testing.T) {
	root := t.TempDir()
	if !caseSensitive(t, root) {
		t.Skip("case-insensitive filesystem")
	}
	put(t, root, "Meganeko/Old Album/01 - a.opus", 90*time.Hour)
	put(t, root, "meganeko/New Album/01 - b.opus", time.Hour)
	put(t, root, "meganeko/folder.jpg", time.Hour)
	o, _, _ := testOpts(root, true)
	if _, err := MergeFolders(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"Meganeko/Old Album/01 - a.opus", "Meganeko/New Album/01 - b.opus", "Meganeko/folder.jpg"} {
		if !exists(root, rel) {
			t.Errorf("missing %s", rel)
		}
	}
	if exists(root, "meganeko") {
		t.Error("variant folder not removed")
	}
	if strings.Contains(strings.Join(listAll(t, root), "\n"), ".ytm-") {
		t.Error("stray temp files")
	}
}

func listAll(t *testing.T, root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		out = append(out, p)
		return nil
	})
	return out
}
