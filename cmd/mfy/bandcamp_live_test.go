package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/bandcamp"
	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/store"
)

// Downloads ONE real purchase (a single track if there is one) from the signed-in browser into
// a temp folder and reports counts and formats only. YTM_LIVE_ACCOUNT=1 go test ./cmd/ytm -run LiveBandcamp -v
func TestLiveBandcampPurchase(t *testing.T) {
	if appname.Getenv("LIVE_ACCOUNT") == "" {
		t.Skip("set YTM_LIVE_ACCOUNT=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var cs []browsercookies.Cookie
	for _, b := range browsercookies.Detect() {
		if c, err := b.BandcampCookies(); err == nil {
			cs = c
			t.Logf("using %s", b.Name)
			break
		}
	}
	if cs == nil {
		t.Skip("no browser signed in to Bandcamp")
	}
	c, err := bandcamp.New(ctx, &http.Client{Timeout: 5 * time.Minute}, cs)
	if err != nil {
		t.Fatal(err)
	}
	coll, err := c.Collection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d purchases in the collection", len(coll))
	pick := -1
	for i, it := range coll {
		if it.DownloadPage != "" && it.Type == "track" {
			pick = i
			break
		}
	}
	if pick < 0 {
		for i, it := range coll {
			if it.DownloadPage != "" {
				pick = i
				break
			}
		}
	}
	if pick < 0 {
		t.Skip("no purchase with a digital download")
	}
	it := coll[pick]
	fs, err := c.Formats(ctx, it.DownloadPage)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, f := range fs {
		names = append(names, f.Name)
	}
	t.Logf("picked a %s; formats offered: %v", it.Type, names)

	tools, err := deps.Ensure(ctx, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(appname.StateDir(root))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := saveOwned(ctx, c, tools, st, heldByName(st), root, it)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("filed %d song(s)", n)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(filepath.Dir(p)) != appname.StateDirName {
			info, _ := d.Info()
			t.Logf("  %s  %d bytes", filepath.Ext(p), info.Size())
		}
		return nil
	})
	if n == 0 {
		t.Fatal("nothing was filed")
	}
	// A second run must find it already held.
	n2, err := saveOwned(ctx, c, tools, st, heldByName(st), root, it)
	t.Logf("second run adds %d (err=%v)", n2, err)
	if n2 != 0 {
		t.Fatal("a second run must not add it again")
	}
}
