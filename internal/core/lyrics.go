package core

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/deps"
	"github.com/bybrooklyn/mfetchy/internal/lyrics"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

const (
	lyricsWorkers = 2                      // songs looked up at once
	lyricsPace    = 400 * time.Millisecond // pause after each request, per worker
)

// lyricsProviders lists the providers switched on in settings, in order of preference.
// LRCLIB is first; NetEase (unofficial) only if switched on.
func lyricsProviders(cfg config.Config) []lyrics.Provider {
	var ps []lyrics.Provider
	if !cfg.NoLyricsLRCLIB {
		ps = append(ps, &lyrics.LRCLIB{})
	}
	if cfg.LyricsNetEase {
		ps = append(ps, &lyrics.NetEase{})
	}
	return ps
}

// newLyricsService builds the lyrics service for a music folder (its cache lives in
// <folder>/.mfetchy/lyrics, so it follows the folder, not the files).
func newLyricsService(ctx context.Context, u *ui.UI, out string) (*lyrics.Service, error) {
	ps := lyricsProviders(config.Load())
	if len(ps) == 0 {
		return nil, fmt.Errorf("every lyrics source is switched off in settings")
	}
	ffmpeg, ffprobe, err := deps.EnsureMedia(ctx, u.Logf)
	if err != nil {
		return nil, err
	}
	return &lyrics.Service{
		Fetcher: &lyrics.Fetcher{Providers: ps, Cache: &lyrics.Cache{Dir: lyrics.CacheDir(out)}, Pace: lyricsPace},
		FFmpeg:  ffmpeg, FFprobe: ffprobe,
	}, nil
}

// fetchLyrics gives each of files its lyrics (a .lrc or .txt beside it and the lyrics tag
// inside it). Songs that have lyrics already, and songs the cache says nobody has, cost
// nothing, so a run that was interrupted just continues. One line per song, at most
// lyricsWorkers at a time, and a short pause after each request.
func fetchLyrics(ctx context.Context, u *ui.UI, out string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	svc, err := newLyricsService(ctx, u, out)
	if err != nil {
		return err
	}
	var (
		mu                        sync.Mutex
		done, synced, plain, none int
		had, failed, instr        int
		firstErr                  error
	)
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < lyricsWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				res, err := svc.Process(ctx, f)
				name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
				mu.Lock()
				done++
				n := fmt.Sprintf("%d/%d", done, len(files))
				switch {
				case ctx.Err() != nil:
				case err != nil:
					failed++
					if lyrics.IsOutage(err) {
						if firstErr == nil {
							firstErr = err
						}
					}
					u.Bad("%s  %s · %v", n, name, err)
				case res.Had:
					had++
				case res.Kind == lyrics.KindSynced:
					synced++
					u.Good("%s  %s · synced, %s", n, name, lyricsFrom(res))
				case res.Kind == lyrics.KindPlain:
					plain++
					u.Good("%s  %s · plain only, %s", n, name, lyricsFrom(res))
				case res.Kind == lyrics.KindInstrumental:
					instr++
					u.Note("%s  %s · instrumental", n, name)
				default:
					none++
					u.Note("%s  %s · no lyrics found", n, name)
				}
				u.Status("Lyrics %s", n)
				mu.Unlock()
				if err == nil && !res.Cached && !res.Had {
					select {
					case <-time.After(lyricsPace):
					case <-ctx.Done():
					}
				}
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case jobs <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	u.ClearStatus()
	if ctx.Err() != nil {
		return context.Canceled
	}
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{synced, "synced"}, {plain, "plain only"}, {instr, "instrumental"}, {none, "none found"}, {had, "already had them"}, {failed, "failed"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	u.Note("lyrics: %s", strings.Join(parts, ", "))
	if firstErr != nil {
		u.Hint("a lyrics service could not be reached; run it again later, nothing is lost")
	}
	return nil
}

func lyricsFrom(r lyrics.Outcome) string {
	if r.Cached {
		return "from the cache"
	}
	return r.Source
}

// lyricsLibraryFlow is "Fetch lyrics for my library": every song under the music folder.
func lyricsLibraryFlow(ctx context.Context, u *ui.UI, o opts) error {
	files := audioFiles(o.out)
	if len(files) == 0 {
		u.Note("no songs in %s yet", tilde(o.out))
		return nil
	}
	u.Head("Fetching lyrics for %s · turn sources off in settings", count(len(files), "song"))
	u.Hint("LRCLIB gets each song's title, artist, album and length, nothing else. Songs done before are skipped.")
	return fetchLyrics(ctx, u, o.out, files)
}

var lyricsAudioExt = map[string]bool{".mp3": true, ".m4a": true, ".opus": true, ".flac": true, ".ogg": true}

// audioFiles lists the songs under dir (not mfetchy's own .mfetchy folder), in a stable order.
func audioFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == appname.StateDirName || d.Name() == appname.OldStateDirName || (strings.HasPrefix(d.Name(), ".") && p != dir) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && lyricsAudioExt[strings.ToLower(filepath.Ext(p))] && !strings.HasPrefix(d.Name(), ".") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}
