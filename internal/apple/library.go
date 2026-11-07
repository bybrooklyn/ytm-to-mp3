package apple

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/apple/download"
)

// Where the library is read from, and the HTTP client used; both are variables so tests can
// point them at a local server.
var (
	libraryBase = "https://amp-api.music.apple.com"
	libraryHTTP = func() *http.Client { return download.Client }
)

// LibrarySongs reads the songs in the signed-in account's Apple Music library
// (/v1/me/library/songs, with each song's catalog entry so it carries the catalog id and
// ISRC the downloader needs). Songs without a catalog entry (files the user uploaded) can't
// be downloaded from Apple and are counted in skipped instead. progress, if set, is told
// how many songs have been read.
func (c *Client) LibrarySongs(ctx context.Context, progress func(n int)) (songs []Track, skipped int, err error) {
	cr, err := c.creds(ctx)
	if err != nil {
		return nil, 0, err
	}
	next := "/v1/me/library/songs?limit=100&include=catalog&l=" + url.QueryEscape(c.language)
	for pages := 0; next != "" && pages < 500; pages++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, libraryBase+next, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+cr.AuthToken)
		req.Header.Set("Music-User-Token", cr.MediaUserToken)
		req.Header.Set("media-user-token", cr.MediaUserToken)
		req.Header.Set("Origin", "https://music.apple.com")
		req.Header.Set("User-Agent", browserUA)
		if cr.Cookie != "" {
			req.Header.Set("Cookie", cr.Cookie)
		}
		resp, err := libraryHTTP().Do(req)
		if err != nil {
			return nil, 0, err
		}
		var page struct {
			Next string `json:"next"`
			Data []struct {
				Attributes struct {
					Name       string `json:"name"`
					ArtistName string `json:"artistName"`
					AlbumName  string `json:"albumName"`
				} `json:"attributes"`
				Relationships struct {
					Catalog struct {
						Data []struct {
							ID         string `json:"id"`
							Attributes struct {
								Name             string   `json:"name"`
								ArtistName       string   `json:"artistName"`
								AlbumName        string   `json:"albumName"`
								Isrc             string   `json:"isrc"`
								TrackNumber      int      `json:"trackNumber"`
								DiscNumber       int      `json:"discNumber"`
								DurationInMillis int      `json:"durationInMillis"`
								ReleaseDate      string   `json:"releaseDate"`
								GenreNames       []string `json:"genreNames"`
								ComposerName     string   `json:"composerName"`
								ContentRating    string   `json:"contentRating"`
								Artwork          struct {
									URL string `json:"url"`
								} `json:"artwork"`
							} `json:"attributes"`
						} `json:"data"`
					} `json:"catalog"`
				} `json:"relationships"`
			} `json:"data"`
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			return nil, 0, fmt.Errorf("%w (%s)", ErrLogin, resp.Status)
		case resp.StatusCode != http.StatusOK:
			resp.Body.Close()
			return nil, 0, fmt.Errorf("Apple library: %s", resp.Status)
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("Apple's library answer wasn't readable: %w", err)
		}
		for _, d := range page.Data {
			cat := d.Relationships.Catalog.Data
			if len(cat) == 0 || cat[0].ID == "" {
				skipped++
				continue
			}
			a := cat[0].Attributes
			songs = append(songs, Track{
				ID: cat[0].ID, Title: a.Name, Artist: a.ArtistName, Album: a.AlbumName,
				AlbumArtist: a.ArtistName, TrackNumber: a.TrackNumber, DiscNumber: a.DiscNumber,
				DurationMillis: a.DurationInMillis, ISRC: a.Isrc, ReleaseDate: a.ReleaseDate,
				Genre: firstGenre(a.GenreNames), ArtworkURL: a.Artwork.URL, Storefront: c.storefront,
				Composer: a.ComposerName, Explicit: a.ContentRating == "explicit",
			})
		}
		if progress != nil {
			progress(len(songs))
		}
		next = page.Next
		if next != "" && !strings.HasPrefix(next, "/") {
			break // only follow Apple's own relative paging links
		}
	}
	return songs, skipped, nil
}
