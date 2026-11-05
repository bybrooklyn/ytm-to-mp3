package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/engine"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

// albumGroup is one album of the library with the saved songs from it.
type albumGroup struct {
	key, artist, title string
	saved              []model.Track
}

func albumKey(artist, album string) string {
	return strings.ToLower(strings.TrimSpace(artist)) + "\x00" + strings.ToLower(strings.TrimSpace(album))
}

// groupAlbums collects the albums the library has songs from, in order of first
// appearance, keeping those with at least minSaved saved songs.
func groupAlbums(tracks []model.Track, minSaved int) []albumGroup {
	idx := map[string]int{}
	var gs []albumGroup
	for _, t := range tracks {
		artist, album := strings.TrimSpace(t.PrimaryArtist()), strings.TrimSpace(t.Album)
		if artist == "" || album == "" {
			continue // nothing to search for
		}
		k := albumKey(artist, album)
		i, ok := idx[k]
		if !ok {
			i = len(gs)
			idx[k] = i
			gs = append(gs, albumGroup{key: k, artist: artist, title: album})
		}
		gs[i].saved = append(gs[i].saved, t)
	}
	out := gs[:0]
	for _, g := range gs {
		if len(g.saved) >= max(1, minSaved) {
			out = append(out, g)
		}
	}
	return out
}

// albumTracks are the songs on a found album that the library doesn't have yet: not the
// same video, and not the same song under another id (a saved music video).
func albumTracks(g albumGroup, a *engine.Album, have map[string]bool) []model.Track {
	var out []model.Track
	for i, t := range a.Tracks {
		if have[t.ID] {
			continue
		}
		dup := false
		for _, s := range g.saved {
			if engine.SameSong(s.Title, t.Title) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		have[t.ID] = true
		// The album name as saved, so the new songs land in the same folder, numbered in
		// album order.
		out = append(out, model.Track{ID: t.ID, Title: t.Title, Album: g.title, Artists: []string{g.artist},
			TrackNo: i + 1, TrackTotal: len(a.Tracks), AlbumArtist: g.artist, Cover: a.Cover, Date: a.Date()})
	}
	return out
}

// numberFromAlbums gives tracks their position on their album wherever that album has
// been found on YouTube Music before (the --albums cache), matching by video id or, for a
// saved music video, by title.
func numberFromAlbums(st *store.Store, tracks []model.Track) {
	lookup := albumPlaces(st)
	for i := range tracks {
		t := &tracks[i]
		a, no := lookup(t.PrimaryArtist(), t.Album, t.Title, t.ID)
		if a == nil {
			continue
		}
		if t.TrackNo <= 0 && no > 0 {
			t.TrackNo, t.TrackTotal = no, len(a.Tracks)
		}
		// the album's square art and year beat the video's thumbnail and upload date
		if t.Cover == "" {
			t.Cover = a.Cover
		}
		if t.Date == "" {
			t.Date = a.Date()
		}
	}
}

// albumNumbers returns a lookup of a song's position on its album from the --albums cache
// in st (each album read once), matching by video id or, for a saved music video, by
// title. 0 = not known.
func albumNumbers(st *store.Store) func(artist, album, title, id string) (no, total int) {
	lookup := albumPlaces(st)
	return func(artist, album, title, id string) (int, int) {
		if a, no := lookup(artist, album, title, id); a != nil && no > 0 {
			return no, len(a.Tracks)
		}
		return 0, 0
	}
}

// albumPlaces returns a lookup of a song's album in st's album cache (each album read
// once) and its place on it (0 when the album is known but the song isn't found on it).
func albumPlaces(st *store.Store) func(artist, album, title, id string) (*engine.Album, int) {
	albums := map[string]*engine.Album{}
	return func(artist, album, title, id string) (*engine.Album, int) {
		if strings.TrimSpace(album) == "" || strings.TrimSpace(artist) == "" {
			return nil, 0
		}
		k := albumKey(artist, album)
		a, seen := albums[k]
		if !seen {
			if data, ok := st.Album(k); ok && data != "" {
				var al engine.Album
				if json.Unmarshal([]byte(data), &al) == nil {
					a = &al
				}
			}
			albums[k] = a
		}
		if a == nil {
			return nil, 0
		}
		for n, at := range a.Tracks {
			if (id != "" && at.ID == id) || engine.SameSong(title, at.Title) {
				return a, n + 1
			}
		}
		return nil, 0 // a same-named album this song isn't on: its art isn't this song's
	}
}

// findAlbums looks every group up on YouTube Music (from st's cache where possible), a few
// at a time over the given exits, and returns the albums found by group key. New answers
// are cached unless readOnly (a dry run).
func findAlbums(ctx context.Context, u *ui.UI, tools deps.Tools, proxies []string, st *store.Store, readOnly bool, gs []albumGroup) map[string]*engine.Album {
	if len(proxies) == 0 {
		proxies = []string{""}
	}
	found := map[string]*engine.Album{}
	var mu sync.Mutex
	var todo []albumGroup
	for _, g := range gs {
		if st == nil {
			todo = append(todo, g)
			continue
		}
		data, ok := st.Album(g.key)
		if !ok {
			todo = append(todo, g)
			continue
		}
		var a engine.Album
		if data != "" && json.Unmarshal([]byte(data), &a) == nil {
			found[g.key] = &a
		}
	}
	if len(todo) == 0 {
		return found
	}
	var next, finished atomic.Int64
	var wg sync.WaitGroup
	workers := min(len(todo), 2*len(proxies), 16)
	for w := 0; w < workers; w++ {
		proxy := proxies[w%len(proxies)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(todo) {
					return
				}
				g := todo[i]
				a, err := engine.FindAlbum(ctx, tools, proxy, g.artist, g.title, g.saved)
				u.Status("Finding albums on YouTube Music: %d/%d", finished.Add(1), len(todo))
				if err != nil {
					continue // a failed lookup isn't cached: next run tries again
				}
				data := ""
				if a != nil {
					b, _ := json.Marshal(a)
					data = string(b)
					mu.Lock()
					found[g.key] = a
					mu.Unlock()
				}
				if st != nil && !readOnly {
					_ = st.SetAlbum(g.key, data)
				}
			}
		}()
	}
	wg.Wait()
	u.ClearStatus()
	return found
}

// linkLookup is one pasted link to look up before the download: a playlist/album link to
// expand, or a song whose album to fetch. slot is its place among the inputs.
type linkLookup struct {
	slot       int
	link, song string
}

// lookUpLinks looks up every link, a few at a time spread over the exits, and puts each
// one's songs in its slot. A song comes back with its whole album (engine.SongAlbum). With
// albums on, a playlist's songs also get their artist and album filled in, so the album
// completion after this knows what to look for.
func lookUpLinks(ctx context.Context, u *ui.UI, o opts, tools deps.Tools, proxies []string, ls []linkLookup, slots [][]model.Track) error {
	var (
		next, finished atomic.Int64
		wg             sync.WaitGroup
		mu             sync.Mutex
		firstErr       error
		albums         int
	)
	status := func() {
		u.Status("Looking up %s: %d/%d", count(len(ls), "link"), finished.Load(), len(ls))
	}
	status()
	workers := min(len(ls), max(1, len(proxies)), 4)
	for w := 0; w < workers; w++ {
		proxy := proxies[w%len(proxies)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(ls) {
					return
				}
				l := ls[i]
				var ts []model.Track
				if l.song != "" {
					var a *engine.Album
					ts, a = engine.SongAlbum(ctx, tools, proxy, l.song)
					if a != nil {
						mu.Lock()
						albums++
						mu.Unlock()
					}
				} else {
					var err error
					if ts, err = engine.ExpandURL(ctx, tools, proxy, l.link); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
						continue
					}
					if o.albums && !o.song {
						ts = songInfos(ctx, proxies, ts)
					}
				}
				slots[l.slot] = ts
				finished.Add(1)
				status()
			}
		}()
	}
	wg.Wait()
	u.ClearStatus()
	if firstErr != nil {
		return firstErr
	}
	if albums > 0 {
		u.Styled(fmt.Sprintf("  %s %s from the pasted songs %s", ui.Accent("♪"), ui.Bold(count(albums, "whole album")),
			ui.Dim("(add --song for just the song)")))
	}
	return ctx.Err()
}

// songInfos fills in the artist and album of every song that lacks them, with one quick
// player request each, many at once over the exits. Songs YouTube has no album for stay
// as they are.
func songInfos(ctx context.Context, proxies []string, ts []model.Track) []model.Track {
	clients := make([]*http.Client, 0, len(proxies))
	for _, p := range proxies {
		if c, err := vpn.NewClient(p); err == nil {
			clients = append(clients, c)
		}
	}
	if len(clients) == 0 {
		return ts
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < min(len(ts), 4*len(clients), 16); w++ {
		hc := clients[w%len(clients)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(ts) {
					return
				}
				if ts[i].Album == "" || len(ts[i].Artists) == 0 {
					ts[i] = engine.SongInfo(ctx, hc, ts[i])
				}
			}
		}()
	}
	wg.Wait()
	return ts
}

// perTrackMB is a rough size of a 4-minute song in each format, for the estimate.
var perTrackMB = map[string]float64{"opus": 3.5, "m4a": 4, "mp3": 8, "flac": 30}

// completeAlbums returns the rest of every album the library has a song from (with at
// least o.albumsMin saved), after showing how much that is and asking.
func completeAlbums(ctx context.Context, u *ui.UI, o opts, tools deps.Tools, proxies []string, st *store.Store, tracks []model.Track) []model.Track {
	gs := groupAlbums(tracks, o.albumsMin)
	if len(gs) == 0 {
		u.Printf("No albums to complete (songs need an artist and album name).")
		return nil
	}
	u.Printf("Looking up %d albums on YouTube Music (once; the answers are remembered)...", len(gs))
	found := findAlbums(ctx, u, tools, proxies, st, o.dryRun, gs)
	have := map[string]bool{}
	for _, t := range tracks {
		have[t.ID] = true
	}
	var extra []model.Track
	for _, g := range gs {
		if a := found[g.key]; a != nil {
			extra = append(extra, albumTracks(g, a, have)...)
		}
	}
	u.Printf("Found %d of %d albums: %d more songs, about %s.", len(found), len(gs), len(extra),
		engine.HumanBytes(int64(float64(len(extra))*perTrackMB[o.format]*(1<<20))))
	if len(extra) == 0 || o.dryRun || u.Confirm("Download them too?", true) {
		return extra
	}
	return nil
}
