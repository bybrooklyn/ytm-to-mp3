package engine

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/deps"
)

// Sites other than YouTube that ytm can take music from. yt-dlp does the extraction (these
// sites change often, and Bandcamp shows non-browser clients a bot check that only yt-dlp
// keeps up with); everything about the music library (what you already have, tags, file
// names) is ytm's own.
//
// SoundCloud and Bandcamp serve music at stream quality: Bandcamp 128 kbps MP3, SoundCloud
// 128 kbps MP3 or 64 kbps Opus/AAC. Files keep the format the site serves (a lossy-to-lossy
// re-encode would only make them worse).

// WebTrack is one song from a site yt-dlp extracts.
type WebTrack struct {
	Site        string // "soundcloud" or "bandcamp"
	URL         string // the song's own page, which FetchWeb downloads
	ID          string
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Genre       string
	Date        string
	TrackNo     int
	TrackTotal  int
	Duration    float64
	Cover       string
	// ISRC, Downloadable and Policy are what SoundCloud says; recorded only.
	ISRC         string
	Downloadable bool
	Policy       string
}

// WebSite names the supported site a link belongs to ("" = not one of them).
func WebSite(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case h == "soundcloud.com" || strings.HasSuffix(h, ".soundcloud.com"):
		return "soundcloud"
	case h == "bandcamp.com" || strings.HasSuffix(h, ".bandcamp.com"):
		return "bandcamp"
	}
	return ""
}

type webInfo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Track       string   `json:"track"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"album_artist"`
	TrackNumber int      `json:"track_number"`
	Genre       string   `json:"genre"`
	ReleaseDate string   `json:"release_date"`
	UploadDate  string   `json:"upload_date"`
	Uploader    string   `json:"uploader"`
	Duration    float64  `json:"duration"`
	Thumbnail   string   `json:"thumbnail"`
	Thumbnails  []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"thumbnails"`
	WebpageURL string    `json:"webpage_url"`
	Extractor  string    `json:"extractor_key"`
	Entries    []webInfo `json:"entries"`
}

// ListWeb expands a SoundCloud or Bandcamp link (a song, album, playlist, or an artist's
// page) into its songs, with the details the site gives. proxy is the exit to go through
// ("" = direct).
func ListWeb(ctx context.Context, tools deps.Tools, proxy, link string) ([]WebTrack, error) {
	site := WebSite(link)
	if site == "" {
		return nil, fmt.Errorf("%q is not a SoundCloud or Bandcamp link", link)
	}
	if site == "soundcloud" {
		if ts, ok := nativeListSoundCloud(ctx, proxy, link); ok {
			return ts, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	y := ytdlp{tools: tools, proxy: proxy}
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-warnings", "--yes-playlist", "--socket-timeout", "20", "-J", "--", link}
	b, err := y.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var root webInfo
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, &Error{kindTransient, "unreadable yt-dlp output"}
	}
	var out []WebTrack
	flattenWeb(root, site, "", &out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no songs found at %s", link)
	}
	// An album's songs know their position; the total is how many there are.
	albums := map[string]int{}
	for _, t := range out {
		if t.Album != "" {
			albums[t.Site+"\x00"+t.AlbumArtist+"\x00"+t.Album]++
		}
	}
	for i, t := range out {
		if t.Album != "" && t.TrackNo > 0 {
			out[i].TrackTotal = albums[t.Site+"\x00"+t.AlbumArtist+"\x00"+t.Album]
		}
	}
	return out, nil
}

func flattenWeb(in webInfo, site, parentTitle string, out *[]WebTrack) {
	if len(in.Entries) > 0 {
		for _, e := range in.Entries {
			flattenWeb(e, site, in.Title, out)
		}
		return
	}
	if in.WebpageURL == "" && in.ID == "" {
		return
	}
	t := WebTrack{
		Site: site, URL: in.WebpageURL, ID: in.ID,
		Title: firstNonEmpty(in.Track, in.Title), Album: in.Album, AlbumArtist: in.AlbumArtist,
		Genre: in.Genre, TrackNo: in.TrackNumber, Duration: in.Duration,
		Date: isoDate(firstNonEmpty(in.ReleaseDate, in.UploadDate)), Cover: webCover(in),
	}
	switch {
	case in.Artist != "":
		t.Artist = in.Artist
	case len(in.Artists) > 0:
		t.Artist = strings.Join(in.Artists, ", ")
	default:
		t.Artist = in.Uploader
	}
	if t.AlbumArtist == "" && t.Album != "" {
		t.AlbumArtist = t.Artist
	}
	*out = append(*out, t)
}

// webCover picks the best cover: the largest square-ish thumbnail, with Bandcamp's
// "_5" (700 px) and "_16" sizes bumped to "_10" (1200 px original).
func webCover(in webInfo) string {
	best, bestPx := in.Thumbnail, 0
	for _, t := range in.Thumbnails {
		px := t.Width * t.Height
		if t.Width > 0 && t.Height > 0 && abs(t.Width-t.Height) <= 2 && px > bestPx {
			best, bestPx = t.URL, px
		}
		if bestPx == 0 && strings.Contains(t.URL, "original") {
			best = t.URL
		}
	}
	if u, err := url.Parse(best); err == nil && strings.HasSuffix(u.Hostname(), ".bcbits.com") {
		for _, s := range []string{"_5.", "_16.", "_2.", "_9."} {
			if strings.Contains(best, s) {
				return strings.Replace(best, s, "_10.", 1)
			}
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// FetchWeb downloads the audio of t into dir with yt-dlp and returns the file's path (its
// extension is whatever the site serves). onProgress is told the bytes so far and the total
// (0 when unknown).
func FetchWeb(ctx context.Context, tools deps.Tools, proxy string, t WebTrack, dir string, onProgress func(got, total int64)) (string, error) {
	if WebSite(t.URL) == "" {
		return "", &Error{kindPermanent, "not a supported link"}
	}
	if WebSite(t.URL) == "soundcloud" {
		if p, ok := nativeFetchSoundCloud(ctx, proxy, t, dir, onProgress); ok {
			return p, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	y := ytdlp{tools: tools, proxy: proxy}
	sum := sha1.Sum([]byte(t.URL))
	name := "web-" + hex.EncodeToString(sum[:6])
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-warnings", "--no-playlist", "--socket-timeout", "20",
		// yt-dlp writes name.ext.part and renames it only when complete, and continues a
		// leftover part when called again in the same dir: an interrupted fetch resumes.
		"-f", "bestaudio/best", "--continue", "--quiet",
		"--progress", "--newline", "--progress-template", progTemplate,
		"-o", strings.ReplaceAll(filepath.Join(dir, name), "%", "%%") + ".%(ext)s",
		"--print", "after_move:filepath", "--", t.URL}
	var path string
	stderr, err := y.runLines(ctx, func(line []byte, _ bool) {
		s := strings.TrimSpace(string(line))
		if got, total, ok := parseProgress(s); ok {
			if onProgress != nil {
				onProgress(got, total)
			}
			return
		}
		if s != "" && !strings.HasPrefix(s, "[") && !strings.HasPrefix(s, "ERROR") && !strings.HasPrefix(s, "WARNING") {
			path = s
		}
	}, args...)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", classify(stderr)
	}
	if path == "" {
		return "", &Error{kindTransient, "yt-dlp produced no file"}
	}
	if _, err := os.Stat(path); err != nil {
		return "", &Error{kindTransient, "yt-dlp produced no file"}
	}
	return path, nil
}
