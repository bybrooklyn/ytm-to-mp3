package engine

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
)

// An album found on YouTube Music: its playlist and every track on it.
type Album struct {
	Playlist string       `json:"playlist"`
	Title    string       `json:"title"`
	Tracks   []AlbumTrack `json:"tracks"`
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
			return a, nil
		}
	}
	return nil, nil
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
		ID      string `json:"id"`
		Title   string `json:"title"`
		Entries []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, &Error{kindTransient, "unreadable yt-dlp output"}
	}
	a := &Album{Playlist: pl.ID, Title: albumTitle(pl.Title)}
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

// SameSong reports whether two track titles name the same song (ignoring case,
// punctuation and bracketed parts like "(2023 Remaster)").
func SameSong(a, b string) bool { return titleMatches(a, b) }
