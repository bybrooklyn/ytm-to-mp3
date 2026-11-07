package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/catalog"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/engine"
	"github.com/bybrooklyn/mfetchy/internal/httpfetch"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/names"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/verify"
)

// webCatalog is a site track in the shared song identity.
func webCatalog(t engine.WebTrack) catalog.Track {
	src := catalog.SoundCloud
	if t.Site == "bandcamp" {
		src = catalog.Bandcamp
	}
	ct := catalog.Track{
		Title: t.Title, Album: t.Album, AlbumArtist: t.AlbumArtist, TrackNo: t.TrackNo, TrackTotal: t.TrackTotal,
		DurationMillis: int(t.Duration * 1000), ArtworkURL: t.Cover, ReleaseDate: t.Date, Genre: t.Genre,
		Origin: src, OriginID: t.ID,
	}
	if t.Artist != "" {
		ct.Artists = []string{t.Artist}
	}
	return ct
}

// webPath is where a site song goes: Artist/Album/NN - Title.ext, as every other download.
func webPath(root string, t engine.WebTrack, ext string) string {
	artist := firstNonEmpty(t.AlbumArtist, t.Artist)
	parts := []string{names.Dirs(root, artist, t.Album)}
	prefix := ""
	if t.Album != "" {
		prefix = names.TrackPrefix(t.TrackNo, t.TrackTotal)
	}
	parts = append(parts, names.PartBetween(prefix, t.Title, "Unknown", ext))
	return filepath.Join(parts...)
}

// downloadWeb takes SoundCloud and Bandcamp links (songs, albums, playlists, artist pages):
// every song not already in the library is fetched, tagged, and filed.
func downloadWeb(ctx context.Context, u *ui.UI, o opts, ex *runExits, links []string) error {
	tools, err := ex.ensureTools(ctx, u)
	if err != nil {
		return err
	}
	// Bandcamp music you have bought comes as the original files, when a browser signed in
	// to Bandcamp is set up (Connections); the rest streams.
	if links, err = downloadOwnedBandcamp(ctx, u, o, tools, links); err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	var tracks []engine.WebTrack
	seen := map[string]bool{}
	for _, l := range links {
		// A status row, not a full-screen spinner: other sources may be drawing at the same time.
		var ts []engine.WebTrack
		u.Status("Looking up %s", truncURL(l))
		err := jobs.Retry(ctx, 2, 2*time.Second, func() (e error) {
			ts, e = engine.ListWeb(ctx, tools, "", l)
			return
		})
		u.ClearStatus()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			u.Bad("%s · %s", truncURL(l), jobs.Human(err))
			continue
		}
		for _, t := range ts {
			if !seen[t.URL] {
				seen[t.URL] = true
				tracks = append(tracks, t)
			}
		}
	}
	if len(tracks) == 0 {
		return errors.New("nothing to download")
	}

	var st *store.Store
	if _, serr := os.Stat(appname.StateDir(o.out)); serr == nil || !o.dryRun {
		if st, err = store.Open(appname.StateDir(o.out)); err != nil {
			return err
		}
		defer st.Close()
	}
	var byName map[string]store.HeldCopy
	if st != nil {
		byName = heldByName(st)
	}
	var todo []engine.WebTrack
	held := 0
	for _, t := range tracks {
		ct := webCatalog(t)
		if st != nil {
			if lib := st.Library(ct.Key()); lib.Done && fileExists(lib.Path) {
				held++
				continue
			}
			if h, ok := byName[catalog.NameKey(t.Title, t.Artist)]; ok && fileExists(h.Path) {
				held++
				continue
			}
		}
		todo = append(todo, t)
	}
	u.Styled(planLine(len(todo), held, o.out, "as the site serves them"))
	if o.dryRun || len(todo) == 0 {
		for _, t := range todo {
			u.Item(t.ID, "%s", ct0(t).Label())
		}
		return nil
	}

	ex.led.NoteHeld(held)
	httpfetch.Sweep(filepath.Join(appname.StateDir(o.out), "partial"), 3*24*time.Hour)
	queue := make(chan engine.WebTrack)
	var wg sync.WaitGroup
	var ok, failed atomic.Int32
	var mu sync.Mutex
	br := jobs.NewBreaker(4) // SoundCloud/Bandcamp refusing again and again: stop, say so once
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range queue {
				if ctx.Err() != nil {
					return
				}
				ct := ct0(t)
				ev := jobs.Event{ID: ct.Key(), Label: ct.Label(), Source: string(ct.Origin)}
				if br.Open() {
					ev.Kind, ev.Reason, ev.Class, ev.Retryable = jobs.Failed, "skipped: "+br.Why(), jobs.ClassThrottle, true
					failed.Add(1)
					ex.record(ev) // counted in the summary; the one message was already printed
					continue
				}
				path, err := saveWeb(ctx, tools, st, o.out, t)
				mu.Lock()
				switch {
				case ctx.Err() != nil:
				case err != nil:
					failed.Add(1)
					if br.Record(err) {
						u.Warn("%s keeps refusing · %s", sourceName(ev.Source), br.Why())
						u.Hint("the rest of its songs are skipped for this run · run mfy again later to continue")
					}
					ev.Kind, ev.Reason = jobs.Failed, jobs.Human(err)
					ev.Class = jobs.Classify(err)
					ev.Retryable = ev.Class.Retryable()
					if br.Open() {
						ex.record(ev)
					} else {
						ex.emit(ev)
					}
				default:
					br.Record(nil)
					ok.Add(1)
					ev.Kind, ev.Path = jobs.Done, path
					ex.emit(ev)
				}
				mu.Unlock()
			}
		}()
	}
loop:
	for _, t := range todo {
		select {
		case queue <- t:
		case <-ctx.Done():
			break loop
		}
	}
	close(queue)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	u.Head("%d downloaded · %d failed", ok.Load(), failed.Load())
	return nil
}

func ct0(t engine.WebTrack) catalog.Track { return webCatalog(t) }

func truncURL(s string) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if len(s) > 60 {
		s = s[:59] + "…"
	}
	return s
}

// saveWeb fetches one song, tags it, moves it into place and records it.
func saveWeb(ctx context.Context, tools deps.Tools, st *store.Store, root string, t engine.WebTrack) (string, error) {
	// A work folder named after the link, inside .mfetchy (the library's own disk, so the final
	// rename is atomic). It is kept when the fetch is cut short, so the next run resumes the
	// partial download, and removed once the song is filed or failed for good.
	sum := sha1.Sum([]byte(t.URL))
	tmpDir := filepath.Join(appname.StateDir(root), "partial", "web-"+hex.EncodeToString(sum[:6]))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(tmpDir)
		}
	}()
	got, err := engine.FetchWeb(ctx, tools, "", t, tmpDir, nil)
	if err != nil {
		keep = ctx.Err() != nil || jobs.Classify(err).Retryable()
		return "", err
	}
	cover := ""
	if t.Cover != "" {
		cp := filepath.Join(tmpDir, "cover.img")
		if fetchCover(ctx, t.Cover, cp) == nil {
			cover = cp
		}
	}
	set := map[string]string{
		"title":        names.StripControl(t.Title),
		"artist":       names.StripControl(t.Artist),
		"album":        names.StripControl(t.Album),
		"album_artist": names.StripControl(firstNonEmpty(t.AlbumArtist, t.Artist)),
		"comment":      t.URL,
	}
	if t.Album == "" {
		delete(set, "album")
		delete(set, "album_artist")
	}
	opt := func(k, v string) {
		if v = names.StripControl(v); v != "" {
			set[k] = v
		}
	}
	opt("track", names.TrackTag(t.TrackNo, t.TrackTotal))
	opt("date", t.Date)
	opt("genre", t.Genre)
	opt("isrc", t.ISRC)
	if err := tag.Write(ctx, tools.FFmpeg, tools.FFprobe, got, set, cover); err != nil {
		return "", fmt.Errorf("tag: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(got))
	// Never file a damaged or cut-short download (a stream that ended early).
	if err := verify.File(ctx, tools.FFprobe, got, verify.Want{Ext: ext, Duration: t.Duration}); err != nil {
		return "", err
	}
	dest := webPath(root, t, ext)
	ct := webCatalog(t)
	if fileExists(dest) {
		// The same name is already there (another copy of the song): keep it, and remember it
		// under this song's key so the next run doesn't fetch it again.
		return dest, st.LibraryPut(ct.Key(), t.Title, t.Artist, string(ct.Origin), catalog.Rank(ct.Origin), dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(got, dest); err != nil {
		return "", err
	}
	return dest, st.LibraryPut(ct.Key(), t.Title, t.Artist, string(ct.Origin), catalog.Rank(ct.Origin), dest)
}

// fetchCover downloads a cover image (https only, at most 10 MB) to dst.
func fetchCover(ctx context.Context, link, dst string) error {
	if !strings.HasPrefix(link, "https://") {
		return errors.New("not https")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}
