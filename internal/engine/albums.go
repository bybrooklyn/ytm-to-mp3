package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/innertube"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

// An album found on YouTube Music: its playlist and every track on it.
type Album struct {
	Playlist string       `json:"playlist"`
	Title    string       `json:"title"`
	Tracks   []AlbumTrack `json:"tracks"`
	// The album's own square art (up to 1200 px) and year, when YouTube Music has them:
	// every song on it is tagged with these instead of its video's thumbnail and upload
	// date. Albums cached before these existed have neither.
	Cover string `json:"cover,omitempty"`
	Year  int    `json:"year,omitempty"`
}

// Date is the album's year as a date tag ("" when unknown).
func (a *Album) Date() string {
	if a == nil || a.Year <= 0 {
		return ""
	}
	return strconv.Itoa(a.Year)
}

// AlbumTrack is one song on a found album.
type AlbumTrack struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// albumCandidates is how many album search results are opened before giving up.
const albumCandidates = 3

// FindAlbum looks for album by artist on YouTube Music and returns it with its track list.
// saved are songs from it that are in the library: a result only counts if its title is the
// album's and it holds one of them (by video id, or by title for a saved music video), so a
// same-named album by someone else, or a different edition, isn't taken. nil, nil = not
// found. proxy is the exit to search through ("" = direct).
func FindAlbum(ctx context.Context, tools deps.Tools, proxy, artist, album string, saved []model.Track) (*Album, error) {
	y := ytdlp{tools: tools, proxy: proxy}
	q := strings.TrimSpace(artist + " " + album)
	if q == "" {
		return nil, nil
	}
	if a, ok, err := y.nativeFindAlbum(ctx, q, album, saved); ok {
		return a, err
	}
	b, err := y.run(ctx, "--ignore-config", "--no-plugin-dirs", "--no-warnings", "--flat-playlist",
		"--playlist-items", "1:"+strconv.Itoa(albumCandidates), "-J", "--",
		"https://music.youtube.com/search?q="+url.QueryEscape(q)+"#albums")
	if err != nil {
		return nil, err
	}
	var res struct {
		Entries []struct {
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, &Error{kindTransient, "unreadable yt-dlp output"}
	}
	var best *Album
	for _, e := range res.Entries {
		if !isAlbumURL(e.URL) {
			continue
		}
		a, err := y.expandAlbum(ctx, e.URL)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if albumMatches(album, a, saved) {
			best = fullerAlbum(best, a, saved) // several editions match: the fullest one
		}
	}
	return best, nil
}

// onAlbum counts how many of the saved songs are on a (by id, or by title).
func onAlbum(a *Album, saved []model.Track) int {
	n := 0
	for _, s := range saved {
		for _, t := range a.Tracks {
			if s.ID == t.ID || titleMatches(s.Title, t.Title) {
				n++
				break
			}
		}
	}
	return n
}

// fullerAlbum picks between two editions of one album: the one holding more of the saved
// songs, then the one with more tracks (the deluxe over the standard edition, so the whole
// of it is downloaded and the standard songs sit in the same folder); the first on a tie.
func fullerAlbum(best, cand *Album, saved []model.Track) *Album {
	if best == nil {
		return cand
	}
	bh, ch := onAlbum(best, saved), onAlbum(cand, saved)
	if ch > bh || ch == bh && len(cand.Tracks) > len(best.Tracks) {
		return cand
	}
	return best
}

// isAlbumURL accepts only YouTube Music album pages, so nothing else from search results is
// ever handed to yt-dlp.
func isAlbumURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host == "music.youtube.com" &&
		strings.HasPrefix(u.Path, "/browse/MPREb_") && !strings.ContainsAny(u.Path[len("/browse/"):], "/.")
}

func (y ytdlp) expandAlbum(ctx context.Context, u string) (*Album, error) {
	b, err := y.run(ctx, "--ignore-config", "--no-plugin-dirs", "--no-warnings", "--flat-playlist", "-J", "--", u)
	if err != nil {
		return nil, err
	}
	var pl struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		ReleaseYear int    `json:"release_year"`
		Thumbnails  []struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"thumbnails"`
		Entries []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, &Error{kindTransient, "unreadable yt-dlp output"}
	}
	a := &Album{Playlist: pl.ID, Title: albumTitle(pl.Title), Year: pl.ReleaseYear}
	// The largest square one (album art), at most 1200 px. A signed URL (with its query) is
	// preferred over a bare one of any size: YouTube answers the bare 1200 px one with 404
	// for many albums.
	best := 0
	for _, t := range pl.Thumbnails {
		if t.Width != t.Height || t.Width > 1200 || !AlbumCoverURL(t.URL) {
			continue
		}
		score := t.Width
		if strings.Contains(t.URL, "?") {
			score += 10000
		}
		if score > best {
			a.Cover, best = t.URL, score
		}
	}
	for _, e := range pl.Entries {
		if model.ValidID(e.ID) {
			a.Tracks = append(a.Tracks, AlbumTrack{ID: e.ID, Title: e.Title})
		}
	}
	return a, nil
}

// albumTitle drops the kind YouTube Music puts in front of an album playlist's title
// ("Album - Minecraft - Volume Alpha" -> "Minecraft - Volume Alpha").
func albumTitle(s string) string {
	for _, p := range []string{"Album - ", "EP - ", "Single - "} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			return rest
		}
	}
	return s
}

// albumMatches: same album title, and at least one saved song is on it. Album titles are
// compared whole (only bracketed parts like "(Deluxe)" are ignored): unlike song titles,
// " - " is part of the name ("Minecraft - Volume Alpha" vs "... Beta").
func albumMatches(want string, a *Album, saved []model.Track) bool {
	if len(a.Tracks) == 0 || normTitle(want) == "" || normTitle(want) != normTitle(a.Title) {
		return false
	}
	for _, s := range saved {
		for _, t := range a.Tracks {
			if s.ID == t.ID || titleMatches(s.Title, t.Title) {
				return true
			}
		}
	}
	return false
}

// SongAlbum is what a pasted song link downloads when albums are on: every song on the
// album it is from, numbered, with the pasted one among them. A song that isn't on an
// album (a single, a plain video) or can't be looked up comes back on its own, with
// whatever was learnt about it. The song is looked up with one player API request (yt-dlp
// when that has no album), then its album is found as FindAlbum does. proxy is the exit
// to go through ("" = direct).
func SongAlbum(ctx context.Context, tools deps.Tools, proxy, id string) ([]model.Track, *Album) {
	if !model.ValidID(id) {
		return []model.Track{{ID: id}}, nil
	}
	song := model.Track{ID: id}
	if hc, err := vpn.NewClient(proxy); err == nil {
		song = SongInfo(ctx, hc, song)
	}
	if song.Album == "" && ctx.Err() == nil {
		y := ytdlp{tools: tools, proxy: proxy}
		if r, err := y.Resolve(ctx, id); err == nil {
			song.Title, song.Album, song.Artists = firstNonEmpty(r.Title, song.Title), r.Album, r.Artists
		}
	}
	// No album: the link stays a bare id, exactly as without albums (the download looks it
	// up and names it from YouTube's own details).
	alone := []model.Track{{ID: id}}
	if song.Album == "" || len(song.Artists) == 0 {
		return alone, nil
	}
	a, err := FindAlbum(ctx, tools, proxy, song.Artists[0], song.Album, []model.Track{song})
	if err != nil || a == nil || len(a.Tracks) < 2 {
		return alone, nil
	}
	out := make([]model.Track, 0, len(a.Tracks))
	for i, t := range a.Tracks {
		mt := model.Track{ID: t.ID, Title: t.Title, Album: song.Album, Artists: []string{song.Artists[0]},
			TrackNo: i + 1, TrackTotal: len(a.Tracks), AlbumArtist: song.Artists[0], Cover: a.Cover, Date: a.Date()}
		if t.ID == id {
			mt.Artists = song.Artists // the pasted song keeps its featured artists
		}
		out = append(out, mt)
	}
	return out, a
}

// FindSongAlbum finds a song known only by title and artists (a Spotify song) on YouTube
// Music and returns the album it is on, as SongAlbum does. nil when the song isn't found
// by that title and artist, or isn't on an album there.
func FindSongAlbum(ctx context.Context, tools deps.Tools, proxy, title string, artists []string) []model.Track {
	q := musicQuery(model.Track{Title: title, Artists: artists})
	if q == "" {
		return nil
	}
	y := ytdlp{tools: tools, proxy: proxy}
	hits, err := y.SearchMusic(ctx, q, musicSearchHits)
	if err != nil {
		return nil
	}
	for _, h := range orderHits(title, "", hits) {
		ts, a := SongAlbum(ctx, tools, proxy, h.ID)
		if a == nil {
			return nil // the song, but on no album: nothing more to bring
		}
		for _, t := range ts {
			if t.ID == h.ID && artistMatches(artists, t.Artists) {
				return ts
			}
		}
		return nil
	}
	return nil
}

// SongInfo fills in t's title, artists and album from one player API request through hc
// (about 0.2 s), when YouTube has them: songs YouTube Music distributes carry them in
// their description. What t already has is kept; a failed lookup returns t unchanged.
func SongInfo(ctx context.Context, hc *http.Client, t model.Track) model.Track {
	if !model.ValidID(t.ID) {
		return t
	}
	v, _ := player(ctx, hc, t.ID) // an unplayable video still has its description
	if v == nil {
		return t
	}
	if m, ok := innertube.ParseMusic(v.Description); ok {
		t.Title = firstNonEmpty(t.Title, m.Title)
		t.Album = firstNonEmpty(t.Album, m.Album)
		if len(t.Artists) == 0 {
			t.Artists = m.Artists
		}
	}
	return t
}

// player is one player API request with the visitor-id retry engine.player does, for a
// lookup outside a run (no lane yet).
func player(ctx context.Context, hc *http.Client, id string) (*innertube.Video, error) {
	c := innertube.Clients[0]
	v, err := innertube.Player(ctx, hc, c, id, "")
	var up *innertube.Unplayable
	if v != nil && v.VisitorData != "" && errors.As(err, &up) && up.Status == "LOGIN_REQUIRED" {
		v, err = innertube.Player(ctx, hc, c, id, v.VisitorData)
	}
	return v, err
}

// SameSong reports whether two track titles name the same song (ignoring case,
// punctuation and bracketed parts like "(2023 Remaster)").
func SameSong(a, b string) bool { return titleMatches(a, b) }
