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

const spotifyUsage = `ytm spotify - download public Spotify albums, playlists and tracks

  ytm spotify <link>...     download a Spotify album, playlist or track
      --source youtube      always use YouTube (skip Apple Music even if signed in)
      --maybe               accept less certain Apple Music matches
      --dry-run             show what would download, and from which source
      -o DIR / -f FORMAT    music folder / format (for YouTube downloads)

Spotify is used only for the song list; Spotify's own audio is DRM'd and is not
downloaded. The audio comes from the best source you have: Apple Music (256 kbps
AAC) when you're signed in and the song matches, otherwise YouTube. Songs
already held are skipped, and a song held from a worse source is upgraded.`

// runSpotify dispatches the `ytm spotify ...` command.
func runSpotify(ctx context.Context, u *ui.UI, o opts, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(spotifyUsage)
		return nil
	}
	for _, a := range args {
		if !spotify.IsURL(strings.Trim(a, `"' `)) {
			return fmt.Errorf("%q is not a Spotify album, playlist or track link", a)
		}
	}
	return downloadUnified(ctx, u, o, args)
}

// item is one imported song and the decision made for it.
type item struct {
	t        catalog.Track
	key      string
	target   catalog.Source // apple or youtube
	match    apple.Track    // set when target == apple
	upgrade  bool           // replacing a copy we already have from a worse source
	oldPath  string         // the file to replace on upgrade
}

// downloadUnified imports songs from the given sources (Spotify/Apple links),
// then downloads each from the best available source — Apple Music (256k AAC)
// when signed in and confidently matched, YouTube otherwise — skipping songs
// already held and upgrading ones held from a worse source.
func downloadUnified(ctx context.Context, u *ui.UI, o opts, inputs []string) error {
	xs, err := buildExits(ctx, u, o)
	if err != nil {
		return err
	}
	defer xs.Close()
	proxies := exitProxies(xs)

	_ = spotify.SetProxy(proxies[0])
	tracks, err := importCatalog(ctx, u, proxies[0], inputs)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return errors.New("nothing to import")
	}
	u.Printf("Imported %d songs", len(tracks))

	st, err := store.Open(filepath.Join(o.out, ".ytm"))
	if err != nil {
		return err
	}
	defer st.Close()

	// Apple is the preferred source when a working login is present.
	var ac *apple.Client
	if o.source != "youtube" && appleSignedIn() {
		cfg := config.Load()
		c, err := apple.New(config.AppleToken(), config.AppleCookies(), cfg.AppleStorefront, cfg.AppleLanguage)
		if err == nil {
			_ = apple.SetProxy(proxies[0])
			if _, verr := c.Verify(ctx); verr != nil {
				u.Printf("(Apple Music login isn't working: %v)", verr)
				u.Printf("(falling back to YouTube for everything)")
			} else {
				ac = c
			}
		}
	}

	items := make([]item, len(tracks))
	for i, t := range tracks {
		items[i] = item{t: t, key: t.Key(), target: catalog.YouTube}
	}
	if ac != nil {
		matchAppleAll(ctx, u, ac, proxies, items, o.maybe)
	}

	// Dedup and upgrade decisions against what is already held.
	var appleItems, ytItems []*item
	held, upgrades := 0, 0
	for i := range items {
		it := &items[i]
		lib := st.Library(it.key)
		want := catalog.Rank(it.target)
		if lib.Done && fileExists(lib.Path) {
			if lib.Quality >= want {
				held++
				continue
			}
			it.upgrade, it.oldPath = true, lib.Path
			upgrades++
		}
		if it.target == catalog.Apple {
			appleItems = append(appleItems, it)
		} else {
			ytItems = append(ytItems, it)
		}
	}
	u.Printf("%d from Apple Music, %d from YouTube, %d already held%s.",
		len(appleItems), len(ytItems), held, plural(upgrades, ", %d to upgrade"))
	if o.dryRun {
		for _, it := range append(append([]*item{}, appleItems...), ytItems...) {
			u.Printf("  [%s] %s", it.target, it.t.Label())
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

// importCatalog turns Spotify and Apple Music links into canonical tracks.
func importCatalog(ctx context.Context, u *ui.UI, proxy string, inputs []string) ([]catalog.Track, error) {
	// Apple catalog reads need only the anonymous developer token, so links
	// resolve without a login.
	var ac *apple.Client
	appleClient := func() *apple.Client {
		if ac == nil {
			ac, _ = apple.New("anonymous", "", "us", "")
			_ = apple.SetProxy(proxy)
		}
		return ac
	}
	var out []catalog.Track
	seen := map[string]bool{}
	add := func(t catalog.Track) {
		k := string(t.Origin) + ":" + t.OriginID
		if t.OriginID == "" {
			k = t.Key()
		}
		if seen[k] {
			return
		}
		seen[k] = true
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
			ts, err := appleClient().ResolveURL(ctx, in)
			if err != nil {
				u.Printf("  %s: %v", in, err)
				continue
			}
			for _, t := range ts {
				add(fromApple(t))
			}
		default:
			return nil, fmt.Errorf("%q is not a Spotify or Apple Music link", in)
		}
	}
	return out, nil
}

// matchAppleAll looks every item up on Apple Music over the exits and marks the
// ones with an acceptable match for the Apple source.
func matchAppleAll(ctx context.Context, u *ui.UI, ac *apple.Client, proxies []string, items []item, includeMaybe bool) {
	var next, finished, found atomic.Int64
	workers := min(len(items), max(2, 2*len(proxies)), 8)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(items) {
					return
				}
				it := &items[i]
				q := apple.Query{Title: it.t.Title, Artists: it.t.Artists, Album: it.t.Album, DurationMillis: it.t.DurationMillis}
				m, ok, err := ac.Match(apple.WithExit(ctx, proxies[(w+int(finished.Load()))%len(proxies)]), q)
				u.Status("Matching on Apple Music: %d/%d (%d found)", finished.Add(1), len(items), found.Load())
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
			jobs[i].ytPath, jobs[i].replace = it.oldPath, true
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
		ArtworkURL: t.ArtworkURL, ReleaseDate: t.ReleaseDate, Origin: catalog.Spotify, OriginID: t.ID,
	}
}

func fromApple(t apple.Track) catalog.Track {
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

func plural(n int, format string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(format, n)
}
