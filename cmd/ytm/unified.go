package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/spotify"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// item is one imported song and the decision made for it.
type item struct {
	t       catalog.Track
	key     string
	target  catalog.Source // apple or youtube
	match   apple.Track    // set when target == apple
	upgrade bool           // replacing a copy we already have from a worse source
	oldPath string         // the file to replace on upgrade
	oldID   string         // that file's YouTube library id, when it is a library song
}

// downloadUnified imports songs from Spotify and Apple Music links, then downloads each
// from the best available source — Apple Music (256k AAC) when signed in and confidently
// matched, YouTube otherwise — skipping songs already held (however they were downloaded)
// and upgrading ones held from a worse source.
func downloadUnified(ctx context.Context, u *ui.UI, o opts, ex *runExits, inputs []string) error {
	xs, err := ex.get()
	if err != nil {
		return err
	}
	proxies := exitProxies(xs)
	_ = spotify.SetProxy(proxies[0])
	_ = apple.SetProxy(proxies[0])

	ac := appleForUnified(ctx, u, o)
	tracks, fromApple, err := importCatalog(ctx, u, ac, inputs)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return errors.New("nothing to import")
	}
	u.Printf("Imported %s", count(len(tracks), "song"))
	if ac == nil && len(fromApple) > 0 && o.source != "youtube" {
		u.Printf("(not signed in to Apple Music, so these come from YouTube; sign in in Settings for 256 kbps AAC)")
	}

	items := decideSources(ctx, u, ac, proxies, tracks, fromApple, o.maybe)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// The music folder's database; a dry run in a folder with none must not create it.
	var st *store.Store
	if _, serr := os.Stat(filepath.Join(o.out, ".ytm")); serr == nil || !o.dryRun {
		if st, err = store.Open(filepath.Join(o.out, ".ytm")); err != nil {
			return err
		}
		defer st.Close()
	}
	var appleItems, ytItems []*item
	heldN, upgrades := 0, 0
	if st != nil {
		appleItems, ytItems, heldN, upgrades = planUnified(st, items, !o.dryRun)
	} else {
		for i := range items {
			if items[i].target == catalog.Apple {
				appleItems = append(appleItems, &items[i])
			} else {
				ytItems = append(ytItems, &items[i])
			}
		}
	}
	u.Printf("%s.", sourceSummary(len(appleItems), len(ytItems), heldN, upgrades))
	if o.dryRun {
		for _, it := range append(append([]*item{}, appleItems...), ytItems...) {
			note := ""
			if it.upgrade {
				note = "  (upgrade)"
			}
			u.Printf("  [%s] %s%s", it.target, it.t.Label(), note)
		}
		return nil
	}
	if len(appleItems)+len(ytItems) == 0 {
		return nil
	}

	var firstErr error
	if len(appleItems) > 0 {
		if err := runAppleItems(ctx, u, o, ac, st, proxies, appleItems); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(ytItems) > 0 && ctx.Err() == nil {
		if err := runYouTubeItems(ctx, u, o, st, xs, ytItems); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// appleForUnified is the signed-in Apple Music client when there is a login that works
// (checked with Apple) and the run may use it; nil means YouTube for everything.
func appleForUnified(ctx context.Context, u *ui.UI, o opts) *apple.Client {
	if o.source == "youtube" || !appleSignedIn() {
		return nil
	}
	cfg := config.Load()
	c, err := apple.New(config.AppleToken(), config.AppleCookies(), cfg.AppleStorefront, cfg.AppleLanguage)
	if err != nil {
		return nil
	}
	sf, err := c.Verify(ctx)
	if err != nil {
		u.Printf("(Apple Music login isn't working: %v)", err)
		u.Printf("(falling back to YouTube for everything; sign in again in Settings)")
		return nil
	}
	if sf != "" && sf != cfg.AppleStorefront {
		cfg.AppleStorefront = sf
		_ = cfg.Save()
	}
	return c
}

// decideSources picks each track's source: Apple Music when there is a working login and
// a match (an Apple Music link's own songs need no search), YouTube otherwise.
func decideSources(ctx context.Context, u *ui.UI, ac *apple.Client, proxies []string, tracks []catalog.Track,
	fromApple map[string]apple.Track, includeMaybe bool) []item {
	items := make([]item, len(tracks))
	for i, t := range tracks {
		items[i] = item{t: t, key: t.Key(), target: catalog.YouTube}
		if at, ok := fromApple[t.OriginID]; ok && ac != nil && t.Origin == catalog.Apple {
			items[i].target, items[i].match = catalog.Apple, at
		}
	}
	if ac != nil {
		matchAppleAll(ctx, u, ac, proxies, items, includeMaybe)
	}
	return items
}

// planUnified checks every item against what the music folder already holds — songs this
// pipeline downloaded, Apple Music downloads, and the YouTube library — and sorts the rest
// by source. A song held from a worse source than its target is marked for upgrade.
func planUnified(st *store.Store, items []item, remember bool) (appleItems, ytItems []*item, heldN, upgrades int) {
	byName := heldByName(st)
	for i := range items {
		it := &items[i]
		h, ok := heldCopy(st, byName, it)
		if ok && h.quality >= catalog.Rank(it.target) {
			heldN++
			if h.indirect && remember { // remember it under this song's key: the next run is one lookup
				_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), h.source, h.quality, h.path)
			}
			continue
		}
		if ok {
			it.upgrade, it.oldPath, it.oldID = true, h.path, h.ytID
			upgrades++
		}
		if it.target == catalog.Apple {
			appleItems = append(appleItems, it)
		} else {
			ytItems = append(ytItems, it)
		}
	}
	return appleItems, ytItems, heldN, upgrades
}

// held is a copy of a song that is already in the music folder.
type held struct {
	path, source string
	quality      int
	ytID         string // its YouTube library id, for a copy that is a library song
	indirect     bool   // found by Apple id or by name, not under the song's key
}

// heldCopy finds the copy of it already on disk, if any: under its key, then as the Apple
// song it was matched to, then by title and artist.
func heldCopy(st *store.Store, byName map[string]store.HeldCopy, it *item) (held, bool) {
	if lib := st.Library(it.key); lib.Done && fileExists(lib.Path) {
		return held{path: lib.Path, source: lib.Source, quality: lib.Quality}, true
	}
	if it.target == catalog.Apple {
		if a := st.Apple(it.match.ID); a.Done && fileExists(a.Path) {
			return held{path: a.Path, source: string(catalog.Apple), quality: catalog.Rank(catalog.Apple), indirect: true}, true
		}
	}
	for _, artist := range it.t.Artists {
		h, ok := byName[catalog.NameKey(it.t.Title, artist)]
		if !ok || !fileExists(h.Path) {
			continue
		}
		c := held{path: h.Path, source: h.Source, quality: catalog.Rank(catalog.Source(h.Source)), indirect: true}
		if h.Source == string(catalog.YouTube) && model.ValidID(h.ID) {
			c.ytID = h.ID
		}
		return c, true
	}
	return held{}, false
}

// heldByName indexes every downloaded song by title and artist (and by each name in a
// joined "A, B & C" credit); when two share a name the better copy wins.
func heldByName(st *store.Store) map[string]store.HeldCopy {
	hs, err := st.Held()
	if err != nil {
		return nil
	}
	idx := map[string]store.HeldCopy{}
	for _, h := range hs {
		for _, a := range splitArtists(h.Artist) {
			k := catalog.NameKey(h.Title, a)
			if old, ok := idx[k]; !ok || catalog.Rank(catalog.Source(h.Source)) > catalog.Rank(catalog.Source(old.Source)) {
				idx[k] = h
			}
		}
	}
	return idx
}

// splitArtists is the whole credit plus each name in it ("A, B & C" -> "A, B & C", A, B,
// C), with YouTube's " - Topic" channel suffix dropped.
func splitArtists(s string) []string {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), " - Topic"))
	if s == "" {
		return nil
	}
	out := []string{s}
	joined := strings.NewReplacer(" & ", ",", " feat. ", ",", " ft. ", ",", " x ", ",").Replace(s)
	if parts := strings.Split(joined, ","); len(parts) > 1 {
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// importCatalog turns Spotify and Apple Music links into canonical tracks. Apple songs are
// also returned by id (fromApple), so they download as themselves with no search. ac is
// the signed-in client, if any; without one, Apple links resolve anonymously.
func importCatalog(ctx context.Context, u *ui.UI, ac *apple.Client, inputs []string) ([]catalog.Track, map[string]apple.Track, error) {
	appleClient := func() (*apple.Client, error) {
		if ac == nil {
			// Catalog reads need only the developer token, so links resolve without a login.
			cfg := config.Load()
			c, err := apple.New("anonymous", "", orDefault(cfg.AppleStorefront, "us"), cfg.AppleLanguage)
			if err != nil {
				return nil, err
			}
			ac = c
		}
		return ac, nil
	}
	var out []catalog.Track
	fromApple := map[string]apple.Track{}
	seen := map[string]bool{}
	byName := map[string][]int{} // NameKey -> indexes into out
	add := func(t catalog.Track) {
		k := string(t.Origin) + ":" + t.OriginID
		if t.OriginID == "" {
			k = t.Key()
		}
		if seen[k] {
			return
		}
		seen[k] = true
		// The same song linked from two services (an album pasted from Spotify and from
		// Apple Music) is one song: keep the Apple copy, which has the ISRC and full details
		// and downloads with no search.
		nk := catalog.NameKey(t.Title, t.PrimaryArtist())
		for _, i := range byName[nk] {
			if sameSong(out[i], t) {
				if t.Origin == catalog.Apple && out[i].Origin != catalog.Apple {
					out[i] = t
				}
				return
			}
		}
		byName[nk] = append(byName[nk], len(out))
		out = append(out, t)
	}
	for _, in := range inputs {
		in = strings.Trim(in, `"' `)
		switch {
		case spotify.IsURL(in):
			ts, err := spotify.Resolve(ctx, in)
			if err != nil {
				u.Printf("  %s: %v", in, err)
				continue
			}
			for _, t := range ts {
				add(fromSpotify(t))
			}
		case apple.IsURL(in):
			c, err := appleClient()
			if err != nil {
				return nil, nil, err
			}
			ts, err := c.ResolveURL(ctx, in)
			if err != nil {
				u.Printf("  %s: %v", in, err)
				continue
			}
			for _, t := range ts {
				fromApple[t.ID] = t
				add(appleToCatalog(t))
			}
		default:
			return nil, nil, fmt.Errorf("%q is not a Spotify or Apple Music link", in)
		}
	}
	return out, fromApple, nil
}

// sameSong reports whether two tracks with the same title and artist are one recording:
// not when both name an album and the albums differ, or both know the length and it
// differs by more than a few seconds (a live or extended version).
func sameSong(a, b catalog.Track) bool {
	if a.ISRC != "" && b.ISRC != "" {
		return strings.EqualFold(a.ISRC, b.ISRC)
	}
	if a.Album != "" && b.Album != "" && crossAlbumKey(a.Album) != crossAlbumKey(b.Album) {
		return false
	}
	if a.DurationMillis > 0 && b.DurationMillis > 0 {
		d := a.DurationMillis - b.DurationMillis
		return d < 4000 && d > -4000
	}
	return true
}

// crossAlbumKey folds an album name for comparing across services; Apple Music adds " - Single"
// and " - EP" to the names of those.
func crossAlbumKey(s string) string {
	for _, suf := range []string{" - Single", " - EP"} {
		s = strings.TrimSuffix(s, suf)
	}
	return catalog.NameKey(s, "")
}

// matchAppleAll looks the items not yet on Apple up on Apple Music over the exits, and
// marks the ones with an acceptable match for the Apple source.
func matchAppleAll(ctx context.Context, u *ui.UI, ac *apple.Client, proxies []string, items []item, includeMaybe bool) {
	var need []*item
	for i := range items {
		if items[i].target != catalog.Apple {
			need = append(need, &items[i])
		}
	}
	if len(need) == 0 {
		return
	}
	var next, finished, found atomic.Int64
	workers := min(len(need), max(2, 2*len(proxies)), 8)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(need) {
					return
				}
				it := need[i]
				q := apple.Query{Title: it.t.Title, Artists: it.t.Artists, Album: it.t.Album, DurationMillis: it.t.DurationMillis}
				m, ok, err := ac.Match(apple.WithExit(ctx, proxies[(w+int(finished.Load()))%len(proxies)]), q)
				u.Status("Matching on Apple Music: %d/%d (%d found)", finished.Add(1), len(need), found.Load())
				if err != nil || !ok || (!m.Sure && !includeMaybe) {
					continue
				}
				it.target, it.match = catalog.Apple, m.Track
				found.Add(1)
			}
		}(w)
	}
	wg.Wait()
	u.ClearStatus()
}

// runAppleItems downloads the Apple-matched items via the Apple pipeline and
// records them in the library.
func runAppleItems(ctx context.Context, u *ui.UI, o opts, ac *apple.Client, st *store.Store, proxies []string, items []*item) error {
	s := &appleSession{c: ac, st: st, exits: proxies, close: func() {}}
	jobs := make([]appleJob, len(items))
	for i, it := range items {
		jobs[i] = appleJob{t: it.match, dest: applePath(o.out, it.match)}
		if it.upgrade {
			// The old copy moves to .ytm/replaced; a library song stays counted as done,
			// so a library download won't fetch it from YouTube again.
			jobs[i].ytID, jobs[i].ytPath, jobs[i].replace = it.oldID, it.oldPath, true
		}
	}
	err := appleRun(ctx, u, o, s, jobs)
	for _, it := range items {
		if ast := st.Apple(it.match.ID); ast.Done && fileExists(ast.Path) {
			_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), string(catalog.Apple), catalog.Rank(catalog.Apple), ast.Path)
		}
	}
	return err
}

// runYouTubeItems downloads the rest from YouTube through the engine, finding
// each song by a YouTube Music search (the tracks carry no video id), then
// records them in the library.
func runYouTubeItems(ctx context.Context, u *ui.UI, o opts, st *store.Store, xs exitSet, items []*item) error {
	logf := func(f string, a ...any) { u.Printf(f, a...) }
	tools, err := deps.Ensure(ctx, logf)
	if err != nil {
		return err
	}
	todo := make([]model.Track, 0, len(items))
	musicFirst := map[string]bool{}
	byID := map[string]*item{}
	for _, it := range items {
		id := synthID(it.t)
		_ = st.AddPending(id, it.t.Title, it.t.PrimaryArtist())
		mt := model.Track{ID: id, Title: it.t.Title, Album: it.t.Album, Artists: it.t.Artists, TrackNo: it.t.TrackNo, TrackTotal: it.t.TrackTotal}
		todo = append(todo, mt)
		musicFirst[id] = true
		byID[id] = it
		if it.upgrade && fileExists(it.oldPath) {
			_ = moveAside(o.out, it.oldPath)
		}
	}
	cache, _ := config.CacheDir()
	sum := engine.Run(ctx, engine.Options{
		Out: o.out, Format: o.format, Jobs: o.jobs, Tools: tools,
		Store: st, UI: u, CacheDir: filepath.Join(cache, "yt-dlp"), Exits: xs.Exits, Replace: xs.Replace, Healthy: xs.Healthy,
		OnBlocked: xs.Blocked, OnGood: xs.Good, NewExits: xs.New, MusicFirst: musicFirst,
	}, todo)
	for id, it := range byID {
		if done, path := st.TrackStatus(id); done && path != "" {
			_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), string(catalog.YouTube), catalog.Rank(catalog.YouTube), path)
		}
	}
	u.Printf("\nYouTube: %d downloaded, %d failed, %s", sum.Done, sum.Failed, engine.HumanBytes(sum.Bytes))
	if sum.Interrupted {
		return context.Canceled
	}
	return nil
}

// --- conversions -----------------------------------------------------------

func fromSpotify(t spotify.Track) catalog.Track {
	return catalog.Track{
		Title: t.Title, Artists: t.Artists, Album: t.Album, DurationMillis: t.DurationMillis,
		TrackNo: t.TrackNo, TrackTotal: t.TrackTotal,
		ArtworkURL: t.ArtworkURL, ReleaseDate: t.ReleaseDate, Origin: catalog.Spotify, OriginID: t.ID,
	}
}

func appleToCatalog(t apple.Track) catalog.Track {
	var artists []string
	if t.Artist != "" {
		artists = []string{t.Artist}
	}
	return catalog.Track{
		Title: t.Title, Artists: artists, Album: t.Album, AlbumArtist: t.AlbumArtist,
		TrackNo: t.TrackNumber, TrackTotal: t.TrackCount, DiscNo: t.DiscNumber, DurationMillis: t.DurationMillis,
		ISRC: t.ISRC, ArtworkURL: t.ArtworkURL, ReleaseDate: t.ReleaseDate, Genre: t.Genre,
		Origin: catalog.Apple, OriginID: t.ID,
	}
}

// synthID is a stable, unique id for a catalog track that has no YouTube video
// id, distinct from a real 11-char id so it is never mistaken for one.
func synthID(t catalog.Track) string {
	if t.OriginID != "" {
		return string(t.Origin)[:2] + "_" + t.OriginID
	}
	return "cat_" + t.Key()
}

func exitProxies(xs exitSet) []string {
	var ps []string
	for _, x := range xs.Exits {
		ps = append(ps, x.Proxy)
	}
	if len(ps) == 0 {
		return []string{""}
	}
	return ps
}

// moveAside moves an existing file to .ytm/replaced before an upgrade overwrites it.
func moveAside(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(path)
	}
	dst := filepath.Join(root, ".ytm", "replaced", rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Rename(path, dst)
}

// count is "1 song", "3 songs".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// sourceSummary is the plan in one line, leaving out what is zero:
// "12 from Apple Music, 1 from YouTube, 3 already held (2 to upgrade)".
func sourceSummary(fromApple, fromYouTube, held, upgrades int) string {
	var parts []string
	if fromApple > 0 {
		parts = append(parts, fmt.Sprintf("%d from Apple Music", fromApple))
	}
	if fromYouTube > 0 {
		parts = append(parts, fmt.Sprintf("%d from YouTube", fromYouTube))
	}
	if held > 0 {
		parts = append(parts, fmt.Sprintf("%d already held", held))
	}
	if len(parts) == 0 {
		return "Nothing to download"
	}
	return strings.Join(parts, ", ") + plural(upgrades, " (%d to upgrade)")
}

func plural(n int, format string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(format, n)
}
