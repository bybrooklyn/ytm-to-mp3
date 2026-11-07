package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/bandcamp"
	"github.com/bybrooklyn/ytm-to-mp3/internal/browsercookies"
	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// chooseBandcampBrowser asks which browser is signed in to Bandcamp and remembers it.
func chooseBandcampBrowser(ctx context.Context) error {
	_, err := pickBandcampBrowser(ctx)
	return err
}

// downloadOwnedBandcamp downloads, in their original quality, the Bandcamp links the signed-in
// account has bought (or, given the account's own profile link, everything it has bought).
// It returns the links that are not purchases, which still go the stream way. When no
// browser is set up for Bandcamp, or its sign-in doesn't work, every link is returned
// untouched.
func downloadOwnedBandcamp(ctx context.Context, u *ui.UI, o opts, tools deps.Tools, links []string) ([]string, error) {
	var bc []string
	for _, l := range links {
		if engine.WebSite(l) == "bandcamp" {
			bc = append(bc, l)
		}
	}
	cfg := config.Load()
	if len(bc) == 0 || cfg.BandcampBrowser == "" {
		return links, nil
	}
	var b browsercookies.Browser
	for _, d := range browsercookies.Detect() {
		if d.ID == cfg.BandcampBrowser {
			b = d
		}
	}
	if b.ID == "" {
		u.Note("the browser set up for Bandcamp isn't installed any more: streaming instead (Connections > Bandcamp)")
		return links, nil
	}
	cs, err := b.BandcampCookies()
	if err != nil {
		u.Note("%s isn't signed in to Bandcamp: streaming instead", b.Name)
		return links, nil
	}
	c, err := bandcamp.New(ctx, &http.Client{Timeout: 5 * time.Minute}, cs)
	if err != nil {
		u.Note("Bandcamp didn't accept that sign-in: streaming instead (%v)", err)
		return links, nil
	}
	var coll []bandcamp.Item
	if err := working(ctx, "Reading your Bandcamp purchases...", func(ctx context.Context) (e error) {
		coll, e = c.Collection(ctx)
		return
	}); err != nil {
		return links, err
	}
	byURL := map[string]bandcamp.Item{}
	for _, it := range coll {
		byURL[bandcamp.Normalize(it.URL)] = it
	}

	var owned []bandcamp.Item
	rest := make([]string, 0, len(links))
	for _, l := range links {
		switch n := bandcamp.Normalize(l); {
		case engine.WebSite(l) != "bandcamp":
			rest = append(rest, l)
		case c.FanURL != "" && n == bandcamp.Normalize(c.FanURL): // your own profile: everything you bought
			owned = append(owned, coll...)
		case byURL[n].URL != "":
			owned = append(owned, byURL[n])
		default:
			rest = append(rest, l)
		}
	}
	if len(owned) == 0 {
		return rest, nil
	}
	u.Note("%s you own: downloading the original files", count(len(owned), "Bandcamp purchase"))

	var st *store.Store
	if _, serr := os.Stat(filepath.Join(o.out, ".ytm")); serr == nil || !o.dryRun {
		if st, err = store.Open(filepath.Join(o.out, ".ytm")); err != nil {
			return rest, err
		}
		defer st.Close()
	}
	if o.dryRun || st == nil {
		for _, it := range owned {
			u.Item("", "%s - %s (%s)", it.Artist, it.Title, it.Type)
		}
		return rest, nil
	}
	byName := heldByName(st)
	okN, failN := 0, 0
	for _, it := range owned {
		if ctx.Err() != nil {
			return rest, ctx.Err()
		}
		n, err := saveOwned(ctx, c, tools, st, byName, o.out, it)
		switch {
		case err != nil:
			failN++
			u.Bad("%s - %s · %v", it.Artist, it.Title, err)
		case n == 0:
			u.Note("%s - %s: already have it in this quality or better", it.Artist, it.Title)
		default:
			okN++
			u.Good("%s - %s (%s)", it.Artist, it.Title, count(n, "song"))
		}
	}
	u.Head("%d purchases downloaded · %d failed", okN, failN)
	return rest, nil
}

// saveOwned downloads one purchase in its best format and files each song. It returns how
// many songs were added (0 when everything was already held at this quality or better).
func saveOwned(ctx context.Context, c *bandcamp.Client, tools deps.Tools, st *store.Store, byName map[string]store.HeldCopy, root string, it bandcamp.Item) (int, error) {
	if it.DownloadPage == "" {
		return 0, fmt.Errorf("no digital download offered")
	}
	formats, err := c.Formats(ctx, it.DownloadPage)
	if err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp(root, ".ytm-bandcamp-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	files, err := c.Fetch(ctx, formats[0], tmp)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, f := range files {
		info, err := tag.Probe(ctx, tools.FFprobe, f)
		if err != nil {
			return added, fmt.Errorf("reading %s: %w", filepath.Base(f), err)
		}
		no, total := names.ParseTrack(info.Tags["track"])
		wt := engine.WebTrack{
			Title:  firstNonEmpty(info.Tags["title"], strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))),
			Artist: firstNonEmpty(info.Tags["artist"], it.Artist), Album: info.Tags["album"],
			AlbumArtist: info.Tags["album_artist"], TrackNo: no, TrackTotal: total,
		}
		ok, err := placeOwned(st, byName, root, f, wt)
		if err != nil {
			return added, err
		}
		if ok {
			added++
		}
	}
	return added, nil
}

// placeOwned files one extracted purchase as Artist/Album/NN - Title.ext and records it,
// unless a copy of at least this quality is already held (then nothing changes). A held copy of
// lower quality is moved to .ytm/replaced.
func placeOwned(st *store.Store, byName map[string]store.HeldCopy, root, file string, wt engine.WebTrack) (bool, error) {
	ct := catalog.Track{Title: wt.Title, Artists: []string{wt.Artist}, Album: wt.Album}
	rank := catalog.Rank(catalog.BandcampPurchase)
	if lib := st.Library(ct.Key()); lib.Done && fileExists(lib.Path) && libRank(lib) >= rank {
		return false, nil
	}
	var old *store.HeldCopy
	for _, a := range splitArtists(wt.Artist) {
		h, ok := byName[catalog.NameKey(wt.Title, a)]
		if !ok || !fileExists(h.Path) {
			continue
		}
		if catalog.Rank(catalog.Source(h.Source)) >= rank {
			return false, nil // already held at this quality or better
		}
		old = &h
		break
	}
	dest := webPath(root, wt, strings.ToLower(filepath.Ext(file)))
	if fileExists(dest) {
		return false, nil // that name is taken by another file: leave it alone
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(file, dest); err != nil {
		return false, err
	}
	if old != nil && old.Path != dest { // the lower-quality copy goes aside, not away
		_ = moveAside(root, old.Path)
	}
	return true, st.LibraryPut(ct.Key(), wt.Title, wt.Artist, string(catalog.BandcampPurchase), rank, dest)
}
