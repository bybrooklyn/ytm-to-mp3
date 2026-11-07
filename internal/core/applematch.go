package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/apple"
	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/model"
	"github.com/bybrooklyn/mfetchy/internal/names"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/takeout"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// appleFound is a library song and what Apple Music has for it.
type appleFound struct {
	yt    model.Track
	m     apple.Match
	found bool
}

// appleMatch loads a YouTube library export, finds each song on Apple Music (from the
// cache where it can), shows what it found and downloads the chosen matches as AAC.
func appleMatch(ctx context.Context, u *ui.UI, o opts, paths []string) error {
	if len(paths) == 0 {
		p, ok := findExisting()
		if !ok {
			return errors.New("pick your Takeout export first")
		}
		paths = []string{p}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("%q: %w", p, err)
		}
	}
	lib, err := takeout.LoadAll(paths, o.all)
	if err != nil {
		return err
	}
	u.Note("Loaded %s", takeout.Describe(lib))

	s, err := openApple(ctx, u, o)
	if err != nil {
		return err
	}
	defer s.Close()

	res, err := appleLookup(ctx, u, s, lib)
	if err != nil {
		return err
	}
	sure, maybe, missing := appleTally(res)
	report := filepath.Join(appname.StateDir(o.out), "apple-match.txt")
	if err := writeAppleReport(report, res); err != nil {
		u.Warn("couldn't write %s: %v", report, err)
	}
	u.Head("On Apple Music: %d of %d songs · %d sure, %d less certain, %d not found",
		sure+maybe, len(res), sure, maybe, missing)
	headline := fmt.Sprintf("%d of %d songs are on Apple Music: %d sure, %d less certain, %d not found.",
		sure+maybe, len(res), sure, maybe, missing)
	if maybe > 0 {
		u.Note("Less certain matches · the first few; all of them are in %s", tilde(report))
		shown := 0
		for _, r := range res {
			if r.found && !r.m.Sure && shown < 8 {
				shown++
				u.Item(songLabel(r.yt.PrimaryArtist(), r.yt.Title), "→  %s", songLabel(r.m.Track.Artist, r.m.Track.Title))
			}
		}
	}
	if sure+maybe == 0 || o.dryRun {
		return nil
	}

	includeMaybe, replace := o.maybe, o.replace
	if (u.Interactive() || o.ask) && !o.yes {
		which, where := "sure", "next"
		if includeMaybe {
			which = "all"
		}
		if replace {
			where = "replace"
		}
		whichOpts := []Option{{Value: "sure", Label: fmt.Sprintf("The sure matches (%d)", sure)}}
		if maybe > 0 {
			whichOpts = append(whichOpts, Option{Value: "all", Label: fmt.Sprintf("All matches, also the less certain ones (%d)", sure+maybe)})
		}
		whichOpts = append(whichOpts, Option{Value: "none", Label: "Nothing for now"})
		whereOpts := []Option{
			{Value: "next", Label: "Next to the YouTube copies (keep both)"},
			{Value: "replace", Label: "Instead of the YouTube copies (those move to .mfetchy/replaced)"},
		}
		var err error
		if p, ok := o.prompt.(interface {
			SelectBoth(ctx context.Context, t1 string, o1 []Option, d1 string, t2 string, o2 []Option, d2 string) (string, string, error)
		}); ok {
			// the terminal shows both questions in one form
			which, where, err = p.SelectBoth(ctx, "Download which songs from Apple Music?", whichOpts, which, "Where do they go?", whereOpts, where)
		} else if which, err = o.prompt.Select(ctx, "Download which songs from Apple Music?", headline, whichOpts, which); err == nil && which != "none" {
			where, err = o.prompt.Select(ctx, "Where do they go?", "", whereOpts, where)
		}
		if err != nil {
			return err
		}
		if which == "none" {
			return nil
		}
		includeMaybe, replace = which == "all", where == "replace"
	}

	var jobs []appleJob
	taken := map[string]bool{} // one Apple song can match several library entries
	for _, r := range res {
		if !r.found || (!r.m.Sure && !includeMaybe) || taken[r.m.Track.ID] {
			continue
		}
		taken[r.m.Track.ID] = true
		jobs = append(jobs, appleLibraryJob(o.out, s.st.Path(r.yt.ID), r, replace))
	}
	return appleRun(ctx, u, o, s, jobs)
}

// appleLibraryJob places a matched song: beside its YouTube file under the same name
// (with .m4a) when that is downloaded, else by the usual Artist/Album/NN - Title scheme.
func appleLibraryJob(root, ytPath string, r appleFound, replace bool) appleJob {
	j := appleJob{t: r.m.Track, ytID: r.yt.ID, ytPath: ytPath, replace: replace}
	if ytPath == "" || !fileExists(ytPath) {
		j.ytPath = ""
		j.dest = applePath(root, r.m.Track)
		return j
	}
	base := strings.TrimSuffix(ytPath, filepath.Ext(ytPath))
	j.dest = base + ".m4a"
	if j.dest == ytPath && !replace {
		j.dest = base + " (Apple Music).m4a" // the YouTube copy is m4a too: keep both
	}
	return j
}

// appleLookup finds every library song on Apple Music, a few at a time over the exits.
// Answers are cached in the database, so a second run only searches what is new.
func appleLookup(ctx context.Context, u *ui.UI, s *appleSession, lib []model.Track) ([]appleFound, error) {
	res := make([]appleFound, len(lib))
	var todo []int
	cached := 0
	for i, t := range lib {
		res[i].yt = t
		if !model.ValidID(t.ID) || strings.TrimSpace(t.Title) == "" {
			continue
		}
		data, score, ok := s.st.AppleMatch(t.ID)
		if !ok {
			todo = append(todo, i)
			continue
		}
		cached++
		if data != "" {
			var tr apple.Track
			if json.Unmarshal([]byte(data), &tr) == nil && tr.ID != "" {
				res[i].m, res[i].found = apple.Match{Track: tr, Score: score, Sure: score >= appleSureCached}, true
			}
		}
	}
	if cached > 0 {
		u.Note("%d songs were looked up before · remembered", cached)
	}
	if len(todo) == 0 {
		return res, nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var next, finished, found, limited, looked atomic.Int64
	var wg sync.WaitGroup
	workers := min(len(todo), max(2, 2*s.exitCount()), 8)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				k := int(next.Add(1)) - 1
				if k >= len(todo) {
					return
				}
				i := todo[k]
				q := appleQuery(lib[i])
				var m apple.Match
				var ok bool
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					if attempt > 0 {
						select {
						case <-time.After(time.Duration(attempt*attempt) * 3 * time.Second): // 3 s, 12 s
						case <-ctx.Done():
						}
					}
					m, ok, err = s.c.Match(apple.WithExit(ctx, s.exit(w+attempt)), q)
					// a 429 was already rested on and retried once underneath
					if !apple.Transient(err) || apple.RateLimited(err) || ctx.Err() != nil {
						break
					}
				}
				u.Status("Looking songs up on Apple Music: %d/%d (%d found)", finished.Add(1), len(todo), found.Load())
				switch {
				case errors.Is(err, apple.ErrLogin):
					cancel(err)
					continue
				case apple.RateLimited(err):
					// Once Apple's search starts refusing, it keeps at it for a long time:
					// stop and keep what was found rather than wait it out song by song.
					if limited.Add(1) >= int64(max(4, workers)) {
						cancel(errAppleRateLimited)
					}
					continue
				case err != nil:
					continue // not cached: the next run tries again
				}
				limited.Store(0)
				data := ""
				if ok {
					b, _ := json.Marshal(m.Track)
					data = string(b)
					found.Add(1)
					res[i].m, res[i].found = m, true
				}
				score := m.Score
				if ok && m.Sure {
					score = max(score, appleSureCached) // the cache keeps "sure" as a score
				} else if ok {
					score = min(score, appleSureCached-0.01)
				}
				_ = s.st.SetAppleMatch(lib[i].ID, data, score)
				looked.Add(1)
			}
		}()
	}
	wg.Wait()
	u.ClearStatus()
	if cause := context.Cause(ctx); errors.Is(cause, errAppleRateLimited) {
		left := len(todo) - int(looked.Load())
		u.Warn("Apple Music is limiting searches from this address for now · %d songs are still to look up", left)
		u.Hint("What was found so far is kept; run it again later (an hour or so) to look up the rest.")
		return res, nil
	} else if cause != nil {
		return nil, cause
	}
	return res, ctx.Err()
}

var errAppleRateLimited = errors.New("Apple Music search is rate-limited")

// appleSureCached is the cached score at and above which a match counts as sure.
const appleSureCached = 0.9

// appleQuery is the search for a library song, with YouTube's upload noise removed.
func appleQuery(t model.Track) apple.Query {
	var artists []string
	for _, a := range t.Artists {
		if a = tag.CleanArtist(a); a != "" {
			artists = append(artists, a)
		}
	}
	first := ""
	if len(artists) > 0 {
		first = artists[0]
	}
	return apple.Query{Title: tag.CleanTitle(t.Title, first), Artists: artists, Album: t.Album}
}

func appleTally(res []appleFound) (sure, maybe, missing int) {
	for _, r := range res {
		switch {
		case !r.found:
			missing++
		case r.m.Sure:
			sure++
		default:
			maybe++
		}
	}
	return
}

func songLabel(artist, title string) string {
	if artist == "" {
		return names.Clean(title)
	}
	return names.Clean(artist + " - " + title)
}

// writeAppleReport lists the less certain matches and the songs not found, for a look
// before (or after) downloading.
func writeAppleReport(path string, res []appleFound) error {
	var maybe, missing []string
	for _, r := range res {
		yt := songLabel(r.yt.PrimaryArtist(), r.yt.Title)
		switch {
		case !r.found:
			missing = append(missing, "  "+yt)
		case !r.m.Sure:
			t := r.m.Track
			maybe = append(maybe, fmt.Sprintf("  %.2f  %s  →  %s  (%s)  %s", r.m.Score, yt,
				songLabel(t.Artist, t.Title), names.Clean(t.Album), t.URL()))
		}
	}
	sort.Strings(missing)
	var b strings.Builder
	fmt.Fprintf(&b, "mfy: library on Apple Music, %s\n\n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Less certain matches (%d): downloaded only when you choose \"all matches\" (or --maybe)\n", len(maybe))
	b.WriteString(strings.Join(maybe, "\n"))
	fmt.Fprintf(&b, "\n\nNot found on Apple Music (%d)\n", len(missing))
	b.WriteString(strings.Join(missing, "\n"))
	b.WriteString("\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
