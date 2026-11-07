package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple"
	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/lyrics"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/spotify"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tidal"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// item is one imported song and the decision made for it.
type item struct {
	t      catalog.Track
	key    string
	target catalog.Source // tidal, apple or youtube
	match  apple.Track    // set when target == apple (kept when Tidal outranks it, for fallback)

	tidalTrack tidal.Track // set when target == tidal
	upgrade    bool        // replacing a copy we already have from a worse source
	oldPath    string      // the file to replace on upgrade
	oldID      string      // that file's YouTube library id, when it is a library song
	heldRank   int         // catalog.Rank of the source of that old copy
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
	var albumOf func(catalog.Track) []model.Track
	if o.albums && !o.song {
		// A Spotify song's link brings its album, found on YouTube Music when Apple Music
		// can't place it (the Spotify page doesn't say which album it is).
		albumOf = func(t catalog.Track) []model.Track {
			tools, err := ex.ensureTools(ctx, u)
			if err != nil {
				return nil
			}
			return engine.FindSongAlbum(ctx, tools, proxies[0], t.Title, t.Artists)
		}
	}
	// Tidal links are read with the Tidal login; the rest are Spotify and Apple Music links.
	var tidalLinks, otherLinks []string
	for _, in := range inputs {
		if t := strings.Trim(in, `"' `); tidal.IsURL(t) {
			tidalLinks = append(tidalLinks, t)
		} else {
			otherLinks = append(otherLinks, in)
		}
	}
	tc := tidalForUnified(ctx, u, o)
	tidalTracks, fromTidal, err := importTidal(ctx, u, tc, tidalLinks, o.albums && !o.song)
	if err != nil {
		return err
	}
	tracks, fromApple, err := importCatalog(ctx, u, ac, otherLinks, albumOf)
	if err != nil {
		return err
	}
	tracks = append(tracks, tidalTracks...)
	if len(tracks) == 0 {
		return errors.New("nothing to import")
	}
	u.Note("Imported %s", count(len(tracks), "song"))
	if ac == nil && len(fromApple) > 0 && o.source != "youtube" {
		u.Note("not signed in to Apple Music, so these come from YouTube · sign in in Settings for 256 kbps AAC")
	}

	// The music folder's database (it also remembers album lookups); a dry run in a folder
	// with none must not create it.
	var st *store.Store
	if _, serr := os.Stat(filepath.Join(o.out, ".ytm")); serr == nil || !o.dryRun {
		if st, err = store.Open(filepath.Join(o.out, ".ytm")); err != nil {
			return err
		}
		defer st.Close()
	}

	// An album or playlist link gets the rest of every album it has a song from, as a
	// library download does (asking first, with the size).
	if o.albums && !o.song && slices.ContainsFunc(inputs, func(in string) bool { return !isSongLink(in) }) {
		tools, err := ex.ensureTools(ctx, u)
		if err != nil {
			return err
		}
		// Spotify playlists don't say which album a song is from: look that up first, or
		// there is nothing to group.
		if c, err := catalogClient(ac); err == nil {
			fillAlbums(ctx, u, c, proxies, tracks)
		}
		mts := make([]model.Track, len(tracks))
		for i, t := range tracks {
			mts[i] = model.Track{ID: synthID(t), Title: t.Title, Album: t.Album, Artists: t.Artists}
		}
		for _, x := range completeAlbums(ctx, u, o, tools, proxies, st, mts) {
			tracks = append(tracks, catalog.Track{
				Title: x.Title, Artists: x.Artists, Album: x.Album, AlbumArtist: x.AlbumArtist,
				TrackNo: x.TrackNo, TrackTotal: x.TrackTotal, ReleaseDate: x.Date, ArtworkURL: x.Cover,
				Origin: catalog.YouTube, OriginID: x.ID,
			})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	items := decideSources(ctx, u, ac, proxies, tracks, fromApple, o.maybe, tc, fromTidal)
	if ctx.Err() != nil {
		return ctx.Err()
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
	// planUnified files everything that is not Apple under YouTube: peel the Tidal songs off.
	var tidalItems []*item
	ytItems = slices.DeleteFunc(ytItems, func(it *item) bool {
		if it.target == catalog.Tidal {
			tidalItems = append(tidalItems, it)
			return true
		}
		return false
	})
	u.Styled(ui.Mark(ui.Accent("→"), sourceSummary(len(tidalItems), len(appleItems), len(ytItems), heldN, upgrades)))
	if o.dryRun {
		for _, it := range slices.Concat(tidalItems, appleItems, ytItems) {
			note := ""
			if it.upgrade {
				note = "  (upgrade)"
			}
			u.Item(fmt.Sprintf("%-7s", it.target), "%s%s", it.t.Label(), note)
		}
		return nil
	}
	if len(tidalItems)+len(appleItems)+len(ytItems) == 0 {
		return nil
	}

	var firstErr error
	if len(tidalItems) > 0 {
		tools, err := ex.ensureTools(ctx, u)
		if err != nil {
			return err
		}
		failed, err := runTidalItems(ctx, u, o, st, tools.FFmpeg, tools.FFprobe, tc, tidalItems)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		// What Tidal couldn't give comes from the next best source, never dropped.
		for _, it := range failed {
			next := catalog.YouTube
			if it.match.ID != "" {
				next = catalog.Apple
			}
			if it.upgrade && it.heldRank >= catalog.Rank(next) {
				continue // the copy already held is as good as the fallback
			}
			it.target = next
			if next == catalog.Apple {
				appleItems = append(appleItems, it)
			} else {
				ytItems = append(ytItems, it)
			}
		}
	}
	if len(appleItems) > 0 && ctx.Err() == nil {
		if err := runAppleItems(ctx, u, o, ac, st, proxies, appleItems); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(ytItems) > 0 && ctx.Err() == nil {
		began := time.Now()
		if err := runYouTubeItems(ctx, u, o, st, ex, xs, ytItems); err != nil && firstErr == nil {
			firstErr = err
		}
		// Songs fetched from YouTube get the same MusicBrainz pass as a library download
		// (Apple Music songs come fully tagged).
		if firstErr == nil && ctx.Err() == nil {
			firstErr = afterDownload(ctx, u, o, st, began)
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
		u.Warn("Apple Music login isn't working: %v", err)
		u.Hint("falling back to YouTube for everything; sign in again in Settings")
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
	fromApple map[string]apple.Track, includeMaybe bool, tc *tidal.Client, fromTidal map[string]tidal.Track) []item {
	items := make([]item, len(tracks))
	for i, t := range tracks {
		items[i] = item{t: t, key: t.Key(), target: catalog.YouTube}
		if at, ok := fromApple[t.OriginID]; ok && ac != nil && t.Origin == catalog.Apple {
			items[i].target, items[i].match = catalog.Apple, at
		}
		if tt, ok := fromTidal[t.OriginID]; ok && t.Origin == catalog.Tidal {
			items[i].target, items[i].tidalTrack = catalog.Tidal, tt
		}
	}
	if ac != nil {
		matchAppleAll(ctx, u, ac, proxies, items, includeMaybe)
	}
	// Tidal's lossless FLAC outranks Apple's AAC: songs it has (by ISRC, then name) go there.
	if tc != nil && ctx.Err() == nil {
		matchTidalAll(ctx, u, tc, items)
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
			it.upgrade, it.oldPath, it.oldID, it.heldRank = true, h.path, h.ytID, h.quality
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

// libRank is the quality of a library entry, from its source's name; the number stored
// with it is only a fallback for an entry with no source.
func libRank(lib store.LibraryState) int {
	if lib.Source != "" {
		return catalog.Rank(catalog.Source(lib.Source))
	}
	return lib.Quality
}

// heldCopy finds the copy of it already on disk, if any: under its key, then as the Apple
// song it was matched to, then by title and artist.
func heldCopy(st *store.Store, byName map[string]store.HeldCopy, it *item) (held, bool) {
	if lib := st.Library(it.key); lib.Done && fileExists(lib.Path) {
		// Rank by the source's name: the number stored with the song may be from before
		// sources were added or reordered.
		return held{path: lib.Path, source: lib.Source, quality: libRank(lib)}, true
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
	out := []string{s} // the whole credit first: it may be one act's name ("Simon & Garfunkel")
	if parts := names.SplitArtists(s); len(parts) > 1 {
		out = append(out, parts...)
	}
	return out
}

// importCatalog turns Spotify and Apple Music links into canonical tracks. Apple songs are
// also returned by id (fromApple), so they download as themselves with no search. ac is
// the signed-in client, if any; without one, Apple links resolve anonymously.
//
// albumOf, when set (albums on), means a song's link brings its whole album: an Apple Music
// song resolves to its album, and a Spotify song (its page names no album) is looked for on
// Apple Music, then on YouTube Music through albumOf. Songs found that way on YouTube come
// back with Origin YouTube and their video id.
func importCatalog(ctx context.Context, u *ui.UI, ac *apple.Client, inputs []string, albumOf func(catalog.Track) []model.Track) ([]catalog.Track, map[string]apple.Track, error) {
	whole := albumOf != nil
	appleClient := func() (*apple.Client, error) {
		c, err := catalogClient(ac)
		if err == nil {
			ac = c
		}
		return c, err
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
				u.Bad("%s · %v", in, err)
				continue
			}
			if whole && len(ts) == 1 && ts[0].Album == "" {
				if album, byID := spotifySongAlbum(ctx, appleClient, albumOf, ts[0]); len(album) > 0 {
					for id, at := range byID {
						fromApple[id] = at
					}
					for _, t := range album {
						add(t)
					}
					continue
				}
			}
			for _, t := range ts {
				add(fromSpotify(t))
			}
		case apple.IsURL(in):
			c, err := appleClient()
			if err != nil {
				return nil, nil, err
			}
			resolve := c.ResolveURL
			if whole {
				resolve = c.ResolveWholeAlbum // a song's link brings its album
			}
			ts, err := resolve(ctx, in)
			if err != nil {
				u.Bad("%s · %v", in, err)
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

// catalogClient is ac, or without a login an anonymous Apple Music client: catalog reads
// (links, searches) need only the developer token.
func catalogClient(ac *apple.Client) (*apple.Client, error) {
	if ac != nil {
		return ac, nil
	}
	cfg := config.Load()
	return apple.New("anonymous", "", orDefault(cfg.AppleStorefront, "us"), cfg.AppleLanguage)
}

// fillAlbums looks up, in Apple Music's catalog, the album of every song that came without
// one (Spotify playlists name none), a few at a time spread over the exits. Only sure
// matches count; the album's details fill what the song lacks.
func fillAlbums(ctx context.Context, u *ui.UI, c *apple.Client, proxies []string, tracks []catalog.Track) {
	var todo []int
	for i, t := range tracks {
		if t.Album == "" && t.Title != "" && len(t.Artists) > 0 {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return
	}
	var next, finished atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < min(len(todo), 2*len(proxies), 6); w++ {
		wctx := apple.WithExit(ctx, proxies[w%len(proxies)])
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				n := int(next.Add(1)) - 1
				if n >= len(todo) {
					return
				}
				t := &tracks[todo[n]]
				q := apple.Query{Title: t.Title, Artists: t.Artists, DurationMillis: t.DurationMillis}
				if m, ok, err := c.Match(wctx, q); err == nil && ok && m.Sure {
					t.Album = m.Track.Album
					t.AlbumArtist = firstNonEmpty(t.AlbumArtist, m.Track.AlbumArtist)
					t.ReleaseDate = firstNonEmpty(t.ReleaseDate, m.Track.ReleaseDate)
					t.ISRC = firstNonEmpty(t.ISRC, m.Track.ISRC)
					t.Genre = firstNonEmpty(t.Genre, m.Track.Genre)
				}
				u.Status("Finding which album each song is from: %d/%d", finished.Add(1), len(todo))
			}
		}()
	}
	wg.Wait()
	u.ClearStatus()
}

// spotifySongAlbum is the album a Spotify song is on, as catalog tracks: from Apple Music
// when its catalog has the song for sure (full details; byID holds those songs for
// fromApple), else from YouTube Music via albumOf. nil when neither places it.
func spotifySongAlbum(ctx context.Context, appleClient func() (*apple.Client, error), albumOf func(catalog.Track) []model.Track,
	s spotify.Track) (album []catalog.Track, byID map[string]apple.Track) {
	if c, err := appleClient(); err == nil {
		q := apple.Query{Title: s.Title, Artists: s.Artists, DurationMillis: s.DurationMillis}
		if m, ok, err := c.Match(ctx, q); err == nil && ok && m.Sure {
			if ts, err := c.ResolveWholeAlbum(ctx, m.Track.URL()); err == nil && len(ts) > 1 {
				byID = map[string]apple.Track{}
				for _, t := range ts {
					byID[t.ID] = t
					album = append(album, appleToCatalog(t))
				}
				return album, byID
			}
		}
	}
	for _, t := range albumOf(fromSpotify(s)) {
		album = append(album, catalog.Track{
			Title: t.Title, Artists: t.Artists, Album: t.Album, AlbumArtist: t.AlbumArtist,
			TrackNo: t.TrackNo, TrackTotal: t.TrackTotal, ReleaseDate: t.Date, ArtworkURL: t.Cover,
			Origin: catalog.YouTube, OriginID: t.ID,
		})
	}
	return album, nil
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
		if items[i].target != catalog.Apple && items[i].target != catalog.Tidal {
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
	var fresh []string
	for _, it := range items {
		if ast := st.Apple(it.match.ID); ast.Done && fileExists(ast.Path) {
			_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), string(catalog.Apple), catalog.Rank(catalog.Apple), ast.Path)
			fresh = append(fresh, ast.Path)
		}
	}
	if o.lyricsAfter && err == nil && len(fresh) > 0 {
		u.Head("Fetching lyrics for %s · turn off in settings", count(len(fresh), "Apple Music song"))
		if lerr := fetchLyrics(ctx, u, o.out, fresh); lerr != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			u.Bad("lyrics: %v", lerr)
		}
	}
	if err == nil && len(fresh) > 0 {
		writeSidecars(ctx, u, o.out, fresh)
	}
	return err
}

// runYouTubeItems downloads the rest from YouTube through the engine, finding
// each song by a YouTube Music search (the tracks carry no video id), then
// records them in the library.
func runYouTubeItems(ctx context.Context, u *ui.UI, o opts, st *store.Store, ex *runExits, xs exitSet, items []*item) error {
	tools, err := ex.ensureTools(ctx, u)
	if err != nil {
		return err
	}
	todo := make([]model.Track, 0, len(items))
	musicFirst := map[string]bool{}
	byID := map[string]*item{}
	for _, it := range items {
		id := synthID(it.t)
		search := true
		if it.t.Origin == catalog.YouTube && model.ValidID(it.t.OriginID) {
			id, search = it.t.OriginID, false // found on YouTube Music already: its own video
		}
		_ = st.AddPending(id, it.t.Title, it.t.PrimaryArtist())
		mt := catalogToModel(id, it.t)
		todo = append(todo, mt)
		if search {
			musicFirst[id] = true
		}
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
			if it.upgrade {
				lyrics.MoveSidecars(it.oldPath, path) // the lyrics follow the song to its new file
			}
			_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), string(catalog.YouTube), catalog.Rank(catalog.YouTube), path)
		}
	}
	u.Styled(runSummary(sum.Done, sum.Failed, sum.Bytes, sum.Elapsed, "YouTube"))
	if sum.Interrupted {
		return context.Canceled
	}
	return nil
}

// isSongLink reports whether a Spotify or Apple Music link names one song (not an album or
// playlist): a Spotify /track/, an Apple /song/, or an Apple song's place on its album.
func isSongLink(link string) bool {
	u, err := url.Parse(strings.Trim(link, `"' `))
	if err != nil {
		return false
	}
	if t, ok := tidal.ParseURL(link); ok {
		return t.Kind == "track" || t.Track != 0
	}
	return strings.Contains(u.Path, "/track/") || strings.Contains(u.Path, "/song/") ||
		(strings.Contains(u.Path, "/album/") && u.Query().Get("i") != "")
}

// --- conversions -----------------------------------------------------------

// catalogToModel is a catalog song as the engine's track, carrying everything the catalog
// knows (release date, album art, disc, ISRC) so the YouTube copy is tagged from it.
func catalogToModel(id string, t catalog.Track) model.Track {
	date, _, _ := strings.Cut(t.ReleaseDate, "T") // Spotify gives "2001-03-12T00:00:00Z"
	return model.Track{
		ID: id, Title: t.Title, Album: t.Album, Artists: t.Artists,
		TrackNo: t.TrackNo, TrackTotal: t.TrackTotal,
		AlbumArtist: t.AlbumArtist, Disc: t.DiscNo, DiscTotal: t.DiscTotal, Date: date, Genre: t.Genre,
		ISRC: t.ISRC, Cover: artworkURL(t.ArtworkURL),
	}
}

// artworkURL fills in an Apple artwork template ("…/{w}x{h}bb.jpg") at 1200 px; other
// URLs are used as they are.
func artworkURL(u string) string {
	return strings.NewReplacer("{w}", "1200", "{h}", "1200", "{f}", "jpg").Replace(u)
}

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
		TrackNo: t.TrackNumber, TrackTotal: t.TrackCount, DiscNo: t.DiscNumber, DiscTotal: t.DiscCount, DurationMillis: t.DurationMillis,
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
func sourceSummary(fromTidal, fromApple, fromYouTube, held, upgrades int) string {
	var parts []string
	if fromTidal > 0 {
		parts = append(parts, fmt.Sprintf("%d from Tidal", fromTidal))
	}
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
