package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/names"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/verify"
)

// tidalWarning is shown before every Tidal sign-in.
const tidalWarning = `Tidal's terms do not allow downloading its music.

  • This is for songs you can already stream with your own paid Tidal subscription, kept
    for your own use. Do not share the files.
  • Tidal can limit or close an account it thinks is downloading. Go gently, and use a
    spare account if you can.
  • mfetchy fetches only plain FLAC streams (lossless, 16-bit/44.1 kHz). It never decrypts
    anything: a stream Tidal serves encrypted is skipped, and the song comes from Apple
    Music or YouTube instead. The output says which quality you got.
  • Tidal has no public download API. This can stop working whenever Tidal changes things.`

var tidalClient = func() *tidal.Client {
	p, _ := tidal.SessionPath()
	return tidal.New(p)
}

// tidalSignedIn reports whether a Tidal login is saved (not whether it works).
func tidalSignedIn() bool { return tidalClient().SignedIn() }

func tidalQuality() tidal.Quality {
	if config.Load().TidalQuality == string(tidal.Best) {
		return tidal.Best
	}
	return tidal.Lossless
}

func tidalLogout(u *ui.UI) error {
	if err := tidalClient().SignOut(); err != nil {
		return err
	}
	u.Good("Forgot your Tidal login. (It stays listed among your devices in Tidal's own settings.)")
	return nil
}

// tidalForUnified is the signed-in client when there is a login that works and the run may
// use it; nil means no Tidal for this run. A dead login is said once.
func tidalForUnified(ctx context.Context, u *ui.UI, o opts) *tidal.Client {
	if o.source == "youtube" {
		return nil
	}
	c := tidalClient()
	if !c.SignedIn() {
		return nil
	}
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := c.Verify(vctx); err != nil {
		u.Warn("Tidal login isn't working: %v", err)
		u.Hint("skipping Tidal; sign in again in Connections")
		return nil
	}
	return c
}

// importTidal turns Tidal links into catalog tracks (and the Tidal songs by id, so they
// download as themselves with no search).
func importTidal(ctx context.Context, u *ui.UI, tc *tidal.Client, links []string, whole bool) ([]catalog.Track, map[string]tidal.Track, error) {
	if len(links) == 0 {
		return nil, nil, nil
	}
	if tc == nil {
		return nil, nil, errors.New("Tidal links need a Tidal sign-in: Connections > Tidal")
	}
	var out []catalog.Track
	by := map[string]tidal.Track{}
	for _, l := range links {
		ts, err := tc.ResolveURL(ctx, l, whole)
		if err != nil {
			u.Bad("%s · %v", l, err)
			continue
		}
		for _, t := range ts {
			id := strconv.FormatInt(t.ID, 10)
			if _, dup := by[id]; dup {
				continue
			}
			by[id] = t
			out = append(out, t.Catalog())
		}
	}
	return out, by, nil
}

// matchTidalAll looks the items not already Tidal songs up on Tidal and marks the sure
// matches for the Tidal source. Matching is by ISRC, then by name (and length).
func matchTidalAll(ctx context.Context, u *ui.UI, tc *tidal.Client, items []item) {
	var need []*item
	for i := range items {
		if items[i].target != catalog.Tidal {
			need = append(need, &items[i])
		}
	}
	found := 0
	for i, it := range need {
		if ctx.Err() != nil {
			break
		}
		u.Status("Matching on Tidal: %d/%d (%d found)", i, len(need), found)
		t, ok, err := tc.Match(ctx, it.t)
		if err != nil {
			if errors.Is(err, tidal.ErrSignedOut) {
				break
			}
			continue
		}
		if ok {
			it.target, it.tidalTrack = catalog.Tidal, t
			found++
		}
	}
	u.ClearStatus()
}

// tidalPath is Artist/Album/NN - Title.flac, like every other source's layout.
func tidalPath(root string, t tidal.Track) string {
	artist := firstNonEmpty(t.AlbumArtist, strings.Join(t.Artists, ", "))
	parts := []string{root, names.Part(artist, "Unknown Artist")}
	prefix := ""
	if t.Album != "" {
		parts = append(parts, names.Part(t.Album, "Unknown Album"))
		prefix = names.TrackPrefix(t.TrackNo, t.TrackTotal)
	}
	parts = append(parts, names.PartBetween(prefix, t.Title, "Unknown", ".flac"))
	return filepath.Join(parts...)
}

// runTidalItems downloads the Tidal items one at a time (gentle on the account), filing and
// tagging each, and records them in the library. It returns the items that could not come
// from Tidal (no plain lossless stream, an error), for the caller to fetch elsewhere.
func runTidalItems(ctx context.Context, u *ui.UI, o opts, ex *runExits, st *store.Store, ffmpeg, ffprobe string, tc *tidal.Client, items []*item) (failed []*item, err error) {
	q := tidalQuality()
	for _, it := range items {
		ex.watch(jobs.Event{Kind: jobs.Queued, ID: it.key, Label: it.t.Label(), Source: "tidal"})
	}
	// Two at a time: enough to keep the line busy while one song is being tagged, still
	// gentle on the account. If Tidal keeps refusing (login, rate limit) the breaker stops
	// using it for the rest of the run with one message, and the remaining songs go on to
	// the next best source.
	workers := tidalWorkers
	br := jobs.NewBreaker(3)
	var mu sync.Mutex
	var okN, heldN, finished int
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(items) {
					return
				}
				it := items[i]
				mu.Lock()
				stop := br.Open() || ctx.Err() != nil
				if stop {
					failed = append(failed, it)
				}
				mu.Unlock()
				if stop {
					continue
				}
				label := it.t.Label()
				id := it.key
				var last atomic.Int64
				var res string
				ex.watch(jobs.Event{Kind: jobs.Started, ID: id, Label: label, Source: "tidal"})
				ex.watch(jobs.Event{Kind: jobs.Stage, ID: id, Label: label, Source: "tidal", Stage: "downloading"})
				err := jobs.Retry(ctx, 2, 2*time.Second, func() (e error) {
					res, e = tidalSave(ctx, tc, st, ffmpeg, ffprobe, o.out, it, q, func(done, total int64) {
						if total > 0 && time.Now().UnixMilli()-last.Load() > 200 {
							last.Store(time.Now().UnixMilli())
							ex.watch(jobs.Event{Kind: jobs.Progress, ID: id, Label: label, Source: "tidal", Bytes: done, Total: total})
							u.Status("Tidal %d/%d · %s · %d%%", finished+1, len(items), label, done*100/total)
						}
					})
					if errors.Is(e, errTidalExists) {
						return nil
					}
					return e
				})
				mu.Lock()
				finished++
				switch {
				case ctx.Err() != nil:
					failed = append(failed, it)
				case err == nil && res == "":
					heldN++
					ex.emit(jobs.Event{Kind: jobs.Skipped, ID: id, Label: label, Source: "tidal", Reason: "already on disk"})
				case err != nil:
					opened := br.Record(err)
					u.Warn("%s · Tidal: %s", label, jobs.Human(err))
					if opened {
						u.Warn("Tidal keeps refusing · %s", br.Why())
						u.Hint("the rest of this run's Tidal songs come from the next best source")
					} else if !br.Open() {
						u.Hint("getting it from %s instead", map[bool]string{true: "Apple Music", false: "YouTube"}[it.match.ID != ""])
					}
					failed = append(failed, it)
				default:
					br.Record(nil)
					okN++
					kind := jobs.Done
					if it.upgrade {
						kind = jobs.Upgraded
					}
					dest := st.Library(it.key).Path
					ex.emit(jobs.Event{Kind: kind, ID: id, Label: label, Source: "tidal", Path: dest, Quality: "Tidal " + res})
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	u.ClearStatus()
	if ctx.Err() != nil {
		return failed, ctx.Err()
	}
	u.Styled(runSummary(okN, 0, 0, 0, "Tidal"))
	if len(failed) > 0 {
		u.Note("%s not available from Tidal: fetched from another source", count(len(failed), "song"))
	}
	if heldN > 0 {
		u.Note("%s already on disk", count(heldN, "song"))
	}
	return failed, nil
}

var errTidalExists = errors.New("already downloaded")

// tidalWorkers is how many Tidal songs download at once.
var tidalWorkers = 2

// tidalSave downloads one song, tags it and files it, moving a worse copy it replaces aside.
// It returns the quality label of what was fetched.
func tidalSave(ctx context.Context, tc *tidal.Client, st *store.Store, ffmpeg, ffprobe, root string, it *item, q tidal.Quality, progress func(done, total int64)) (string, error) {
	t := it.tidalTrack
	tc.Enrich(ctx, &t)
	dest := tidalPath(root, t)
	if fileExists(dest) && dest != it.oldPath {
		_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), string(catalog.Tidal), catalog.Rank(catalog.Tidal), dest)
		return "", errTidalExists
	}
	s, err := tc.Stream(ctx, t.ID, q)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	// Work happens in .mfetchy/partial (the same disk as the library, so the final rename is
	// atomic): an interrupted download leaves a resumable ".part" there, never a half file
	// in the album folder.
	partDir := filepath.Join(appname.StateDir(root), "partial")
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(partDir, "tidal-"+strconv.FormatInt(t.ID, 10)+".flac")
	_ = os.Remove(tmp)
	defer os.Remove(tmp)
	if err := tc.Download(ctx, s, tmp, progress); err != nil {
		return "", err
	}
	cover := ""
	if cp := tmp + ".cover.jpg"; fetchFile(ctx, t.CoverURL(), cp) == nil {
		cover = cp
		defer os.Remove(cp)
	}
	set := map[string]string{
		"title":        names.StripControl(t.Title),
		"artist":       names.StripControl(strings.Join(t.Artists, ", ")),
		"album":        names.StripControl(t.Album),
		"album_artist": names.StripControl(firstNonEmpty(t.AlbumArtist, strings.Join(t.Artists, ", "))),
		"comment":      t.URL(),
	}
	opt := func(k, v string) {
		if v = names.StripControl(v); v != "" {
			set[k] = v
		}
	}
	opt("track", names.TrackTag(t.TrackNo, t.TrackTotal))
	opt("disc", names.TrackTag(t.DiscNo, t.DiscTotal))
	opt("date", t.ReleaseDate)
	opt("copyright", t.Copyright)
	opt("isrc", t.ISRC)
	if t.Explicit {
		set["explicit"] = "1"
	}
	if err := tag.Write(ctx, ffmpeg, ffprobe, tmp, set, cover); err != nil {
		return "", fmt.Errorf("tag: %w", err)
	}
	// Never file a damaged or cut-short download.
	if err := verify.File(ctx, ffprobe, tmp, verify.Want{Ext: ".flac", Duration: float64(t.DurationSec)}); err != nil {
		return "", err
	}
	var backup string
	if it.upgrade && fileExists(it.oldPath) {
		rel, rerr := filepath.Rel(root, it.oldPath)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(it.oldPath)
		}
		backup = filepath.Join(appname.StateDir(root), "replaced", rel)
		if err := moveAside(root, it.oldPath); err != nil {
			return "", fmt.Errorf("move the old copy aside: %w", err)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if backup != "" {
			_ = os.Rename(backup, it.oldPath)
		}
		return "", err
	}
	_ = st.LibraryPut(it.key, it.t.Title, it.t.PrimaryArtist(), string(catalog.Tidal), catalog.Rank(catalog.Tidal), dest)
	return s.Label(), nil
}

// fetchFile saves a small public file (album art) to dst.
func fetchFile(ctx context.Context, url, dst string) error {
	if url == "" {
		return errors.New("no url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(cctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// tidalDoctor is the self-check line for mfy doctor.
func tidalDoctor(ctx context.Context, bad func(fix, f string, a ...any), ok, note func(f string, a ...any)) {
	c := tidalClient()
	if !c.SignedIn() {
		note("not signed in (optional): Connections > Tidal, for lossless FLAC with your own subscription")
		return
	}
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.Verify(vctx); err != nil {
		bad("sign in again in Connections > Tidal", "the saved login doesn't work: %v", err)
		return
	}
	q := "lossless only"
	if tidalQuality() == tidal.Best {
		q = "hi-res when served as a plain file, else lossless"
	}
	ok("signed in (country %s), and Tidal accepts the login · quality: %s", c.Country(), q)
}
