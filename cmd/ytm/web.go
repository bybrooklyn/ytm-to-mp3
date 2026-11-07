package main

import (
	"context"
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

	"github.com/bybrooklyn/ytm-to-mp3/internal/catalog"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
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
	parts := []string{root, names.Part(artist, "Unknown Artist")}
	prefix := ""
	if t.Album != "" {
		parts = append(parts, names.Part(t.Album, "Unknown Album"))
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
		var ts []engine.WebTrack
		if err := working(ctx, "Looking up "+truncURL(l)+"...", func(c context.Context) (e error) {
			ts, e = engine.ListWeb(c, tools, "", l)
			return
		}); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			u.Bad("%s · %v", truncURL(l), err)
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
	if _, serr := os.Stat(filepath.Join(o.out, ".ytm")); serr == nil || !o.dryRun {
		if st, err = store.Open(filepath.Join(o.out, ".ytm")); err != nil {
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

	jobs := make(chan engine.WebTrack)
	var wg sync.WaitGroup
	var ok, failed atomic.Int32
	var mu sync.Mutex
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if ctx.Err() != nil {
					return
				}
				err := saveWeb(ctx, tools, st, o.out, t)
				mu.Lock()
				if err != nil {
					failed.Add(1)
					u.Bad("%s · %v", ct0(t).Label(), err)
				} else {
					ok.Add(1)
					u.Good("%s", ct0(t).Label())
				}
				mu.Unlock()
			}
		}()
	}
loop:
	for _, t := range todo {
		select {
		case jobs <- t:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
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
func saveWeb(ctx context.Context, tools deps.Tools, st *store.Store, root string, t engine.WebTrack) error {
	tmpDir, err := os.MkdirTemp(root, ".ytm-web-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	got, err := engine.FetchWeb(ctx, tools, "", t, tmpDir, nil)
	if err != nil {
		return err
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
	if err := tag.Write(ctx, tools.FFmpeg, tools.FFprobe, got, set, cover); err != nil {
		return fmt.Errorf("tag: %w", err)
	}
	dest := webPath(root, t, strings.ToLower(filepath.Ext(got)))
	if fileExists(dest) {
		return nil // same name already there (another copy of the song): keep it
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(got, dest); err != nil {
		return err
	}
	ct := webCatalog(t)
	return st.LibraryPut(ct.Key(), t.Title, t.Artist, string(ct.Origin), catalog.Rank(ct.Origin), dest)
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
