package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/bandcamp"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/library"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// albumMarkSource is the library_tracks source of the marker recorded for a Bandcamp album
// purchase that has been downloaded. The album's songs are not known until it is fetched, so
// the marker is how a later run knows the purchase is done. Store.Held lists no such source,
// so it never shows up as a song.
const albumMarkSource = "bandcamp-album"

func albumMarkKey(url string) string { return "bcalbum:" + url }

// allAccountsFlow reads the library of every account that is set up, merges them into one,
// shows what is missing from the music folder and downloads it from the best source each
// song has: a Bandcamp purchase's original files, then Apple Music, then YouTube.
func allAccountsFlow(ctx context.Context, u *ui.UI, o opts) error {
	srcs := library.Sources()
	u.Styled("")
	setUp := 0
	var status []string // the same lines as plain text, for a dialog
	for _, s := range srcs {
		if st := s.Detect(); st.SetUp {
			setUp++
			u.Styled(fmt.Sprintf("  %s %s %s", ui.OK("✓"), ui.Bold(s.Name()), ui.Dim(st.Detail)))
			status = append(status, "Ready: "+s.Name()+" ("+st.Detail+")")
		} else {
			u.Styled(fmt.Sprintf("  %s %s %s", ui.Dim("–"), s.Name(), ui.Dim("skipped: "+st.Hint)))
			status = append(status, "Skipped: "+s.Name()+" ("+st.Hint+")")
		}
	}
	u.Styled("")
	if setUp == 0 {
		return errors.New("no account is set up yet: pick a browser or sign in under Connections")
	}
	ok, err := o.prompt.Confirm(ctx, Question{
		Title: "Read the library of " + count(setUp, "account") + "?",
		Detail: strings.Join(status, "\n") + "\n\nEach account is read only from its own service, from your own connection (not the VPN).\n" +
			"Browser sign-ins are read once, stay in memory and are never saved.",
		Yes: "Read", No: "Back", Default: true,
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrBack
	}

	var songs []library.Song
	var results []library.SourceResult
	if err := o.prompt.Working(ctx, "Reading your accounts...", func(ctx context.Context) error {
		songs, results = library.ReadAll(ctx, srcs, nil)
		return ctx.Err()
	}); err != nil {
		return err
	}
	read := 0
	for _, r := range results {
		switch {
		case r.Skipped:
		case r.Err != nil:
			u.Bad("%s · %v", r.Name, r.Err)
		default:
			read++
			u.Styled(fmt.Sprintf("  %s %s %s", ui.OK("✓"), r.Name, ui.Dim(count(r.Songs, "song"))))
		}
	}
	if read == 0 {
		return errors.New("none of the accounts could be read")
	}

	entries := library.Combine(songs)
	if len(entries) == 0 {
		return errors.New("the accounts have no songs")
	}

	// The music folder's database; a dry run in a folder with none must not create it.
	var st *store.Store
	if _, serr := os.Stat(appname.StateDir(o.out)); serr == nil || !o.dryRun {
		var err error
		if st, err = store.Open(appname.StateDir(o.out)); err != nil {
			return err
		}
		defer st.Close()
	}
	plan := library.MakePlan(entries, heldForPlan(st))
	summary := printPlan(u, plan, o)

	todo := plan.Todo()
	if o.dryRun {
		for _, it := range todo {
			note := ""
			if it.State == library.Upgrade {
				note = "  (upgrade)"
			}
			u.Item(fmt.Sprintf("%-8s", sourceLabel(it.Best.Supply)), "%s%s", it.Entry.Track.Label(), note)
		}
		return nil
	}
	if len(todo) == 0 {
		return nil
	}
	ok, err = o.prompt.Confirm(ctx, Question{Title: "Download?", Detail: summary, Yes: "Download", No: "Back", Default: true})
	if err != nil {
		return err
	}
	if !ok {
		return ErrBack
	}
	return downloadPlan(ctx, u, o, st, srcs, todo)
}

// sourceLabel is a short name of an audio source for the preview.
func sourceLabel(s catalog.Source) string {
	switch s {
	case catalog.BandcampPurchase:
		return "bought"
	case catalog.Apple:
		return "apple"
	}
	return "youtube"
}

// printPlan shows the counts: songs per account, how many are new, how many would be
// upgrades, where the downloads come from and roughly how big they are.
func printPlan(u *ui.UI, p library.Plan, o opts) string {
	c := p.Counts()
	var plain []string // the same lines as plain text, for a dialog
	u.Styled(fmt.Sprintf("\n  %s %s %s", ui.OK("✓"), ui.Bold(count(c.Total, "song")+" in all your accounts"),
		ui.Dim("(merged, duplicates removed)")))
	plain = append(plain, count(c.Total, "song")+" in all your accounts (merged, duplicates removed)")
	for _, a := range c.Accounts() {
		only := ""
		if n := c.OnlyOn[a]; n > 0 {
			only = fmt.Sprintf(", %d only here", n)
		}
		u.Styled(fmt.Sprintf("    %s %s%s", ui.Dim("·"), fmt.Sprintf("%-14s %d", a, c.PerAccount[a]), ui.Dim(only)))
		plain = append(plain, fmt.Sprintf("%s: %d%s", a, c.PerAccount[a], only))
	}
	u.Styled(fmt.Sprintf("\n    %s already in %s · %s new · %s to upgrade", ui.Bold(fmt.Sprint(c.Held)), tilde(o.out),
		ui.Bold(fmt.Sprint(c.New)), ui.Bold(fmt.Sprint(c.Upgrades))))
	plain = append(plain, "", fmt.Sprintf("%d already in %s, %d new, %d to upgrade", c.Held, tilde(o.out), c.New, c.Upgrades))
	var from []string
	for _, s := range []catalog.Source{catalog.BandcampPurchase, catalog.Apple, catalog.YouTube} {
		if n := c.Download[s]; n > 0 {
			from = append(from, fmt.Sprintf("%d from %s", n, map[catalog.Source]string{
				catalog.BandcampPurchase: "your Bandcamp purchases (original files)", catalog.Apple: "Apple Music", catalog.YouTube: "YouTube"}[s]))
		}
	}
	if len(from) > 0 {
		u.Styled(fmt.Sprintf("    %s%s%s", cDim, strings.Join(from, " · "), cReset))
		plain = append(plain, strings.Join(from, ", "))
		if c.EstimateOf > 0 {
			est := fmt.Sprintf("about %s (estimated from %s with a known length)", engine.HumanBytes(c.EstimateBytes), count(c.EstimateOf, "song"))
			u.Styled(fmt.Sprintf("    %s%s%s", cDim, est, cReset))
			plain = append(plain, est)
		}
		u.Styled(fmt.Sprintf("    %s%s%s\n", cDim, extras(o), cReset))
		plain = append(plain, capitalFirst(strings.TrimSuffix(extras(o), "  (change in settings)"))+".")
	} else {
		u.Styled(fmt.Sprintf("    %sNothing to download.%s\n", cDim, cReset))
		plain = append(plain, "Nothing to download.")
	}
	return strings.Join(plain, "\n")
}

// heldForPlan looks entries up in the music folder: under their key, as the Apple song they
// are, by title and artist, and (for a Bandcamp album purchase) by the marker saved when the
// purchase was downloaded. st may be nil (a dry run with no database yet): nothing is held.
func heldForPlan(st *store.Store) library.HeldFunc {
	if st == nil {
		return nil
	}
	byName := heldByName(st)
	return func(e library.Entry, best library.Song) (library.HeldCopy, bool) {
		if best.Whole {
			if m := st.Library(albumMarkKey(best.OriginID)); m.Done && fileExists(m.Path) {
				return library.HeldCopy{Source: catalog.BandcampPurchase, Rank: catalog.Rank(catalog.BandcampPurchase)}, true
			}
			if e.Whole {
				return library.HeldCopy{}, false // the songs of an album we haven't fetched aren't known
			}
		}
		it := &item{t: e.Track, key: e.Key, target: best.Supply}
		if at, ok := best.Ref.(apple.Track); ok {
			it.match = at
		}
		h, ok := heldCopy(st, byName, it)
		if !ok {
			return library.HeldCopy{}, false
		}
		return library.HeldCopy{Source: catalog.Source(h.source), Rank: h.quality}, true
	}
}

// downloadPlan downloads the planned songs through the existing pipelines: purchases as the
// original files (saveOwned), Apple Music songs through appleRun, the rest through the
// YouTube engine.
func downloadPlan(ctx context.Context, u *ui.UI, o opts, st *store.Store, srcs []library.LibrarySource, todo []library.Item) error {
	var owned []bandcamp.Item
	seen := map[string]bool{}
	var tracks []catalog.Track
	fromApple := map[string]apple.Track{}
	for _, it := range todo {
		best := it.Best
		if best.Supply == catalog.BandcampPurchase {
			if bi, ok := best.Ref.(bandcamp.Item); ok {
				if k := bandcamp.Normalize(bi.URL); !seen[k] {
					seen[k] = true
					owned = append(owned, bi)
				}
				continue
			}
		}
		t := it.Entry.Track
		t.Origin, t.OriginID = best.Origin, best.OriginID
		if at, ok := best.Ref.(apple.Track); ok && best.Supply == catalog.Apple {
			fromApple[at.ID] = at
		}
		tracks = append(tracks, t)
	}

	ex := &runExits{ctx: ctx, u: u, o: o}
	ex.tools = sync.OnceValues(func() (deps.Tools, error) { return ensureTools(ctx, u.Logf) })
	defer ex.Close()
	setCurrentRun(ex) // the tag fixing that follows reuses these exits
	defer setCurrentRun(nil)

	var firstErr error
	if len(owned) > 0 {
		var bc *library.Bandcamp
		for _, s := range srcs {
			if b, ok := s.(*library.Bandcamp); ok && b.Client != nil {
				bc = b
			}
		}
		if bc == nil {
			u.Bad("Bandcamp purchases: the account was not read")
		} else if tools, err := ex.ensureTools(ctx, u); err != nil {
			return err
		} else {
			u.Head("Downloading %s in their original quality", count(len(owned), "Bandcamp purchase"))
			err := saveOwnedAll(ctx, u, bc.Client, tools, st, o.out, owned, func(it bandcamp.Item) {
				_ = st.LibraryPut(albumMarkKey(bandcamp.Normalize(it.URL)), it.Title, it.Artist, albumMarkSource,
					catalog.Rank(catalog.BandcampPurchase), o.out)
			}, ownedHooks(ex))
			if err != nil {
				return err
			}
		}
	}
	if len(tracks) == 0 || ctx.Err() != nil {
		return ctx.Err()
	}

	xs, err := ex.get()
	if err != nil {
		return err
	}
	proxies := exitProxies(xs)
	_ = apple.SetProxy(proxies[0])
	ac := appleForUnified(ctx, u, o)
	tc := tidalForUnified(ctx, u, o) // nil unless signed in to Tidal: then it is a candidate best source
	items := decideSources(ctx, u, ac, proxies, tracks, fromApple, o.maybe, tc, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	appleItems, ytItems, _, _ := planUnified(st, items, true)
	// planUnified files everything that is not Apple under YouTube: peel the Tidal songs off,
	// as downloadUnified does.
	var tidalItems []*item
	ytItems = slices.DeleteFunc(ytItems, func(it *item) bool {
		if it.target == catalog.Tidal {
			tidalItems = append(tidalItems, it)
			return true
		}
		return false
	})
	if len(tidalItems) > 0 {
		tools, err := ex.ensureTools(ctx, u)
		if err != nil {
			return err
		}
		failed, err := runTidalItems(ctx, u, o, ex, st, tools.FFmpeg, tools.FFprobe, tc, tidalItems)
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
	if len(appleItems) > 0 {
		if err := runAppleItems(ctx, u, o, ex, ac, st, proxies, appleItems); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(ytItems) > 0 && ctx.Err() == nil {
		began := time.Now()
		if err := runYouTubeItems(ctx, u, o, st, ex, xs, ytItems); err != nil && firstErr == nil {
			firstErr = err
		}
		if firstErr == nil && ctx.Err() == nil {
			firstErr = afterDownload(ctx, u, o, st, began, nil)
		}
	}
	return firstErr
}
