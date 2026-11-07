// Package bandcamp reads the music a person has bought on Bandcamp and downloads the
// original files (FLAC, or the best format offered), using their own signed-in browser
// session instead of the 128 kbps stream anyone can hear.
//
// It talks to the same fan endpoints Bandcamp's own web pages use, with the browser's cookies
// and a plainly identified client (Bandcamp serves non-browser clients a bot-check page for
// its public artist pages, but not for these account endpoints). Cookies stay in memory.
// Only music the account has bought is ever downloaded this way.
package bandcamp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/browsercookies"
)

const (
	base      = "https://bandcamp.com"
	userAgent = "ytm"
)

// ErrNotSignedIn means Bandcamp didn't recognise the session.
var ErrNotSignedIn = errors.New("Bandcamp didn't recognise that browser's sign-in (open bandcamp.com there, sign in, and try again)")

// Client reads one account's purchases.
type Client struct {
	hc     *http.Client
	cookie string
	base   string // tests point it elsewhere
	FanID  int64
	// FanURL is the account's own profile page (https://bandcamp.com/name).
	FanURL string
}

// New makes a client from a browser's Bandcamp cookies and looks up the account.
func New(ctx context.Context, hc *http.Client, cookies []browsercookies.Cookie) (*Client, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	c := &Client{hc: hc, cookie: browsercookies.CookieHeader(cookies), base: base}
	var sum struct {
		FanID   int64 `json:"fan_id"`
		Summary struct {
			Username string `json:"username"`
			URL      string `json:"url"`
		} `json:"collection_summary"`
	}
	if err := c.getJSON(ctx, "/api/fan/2/collection_summary", &sum); err != nil {
		return nil, err
	}
	if sum.FanID == 0 {
		return nil, ErrNotSignedIn
	}
	c.FanID, c.FanURL = sum.FanID, sum.Summary.URL
	return c, nil
}

func (c *Client) do(ctx context.Context, method, u string, body []byte) (*http.Response, error) {
	if strings.HasPrefix(u, "/") {
		u = c.base + u
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return nil, fmt.Errorf("refusing to send a Bandcamp sign-in to %q", u)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sendsCookie(req.URL.Hostname()) {
		req.Header.Set("Cookie", c.cookie)
	}
	return c.hc.Do(req)
}

// sendsCookie: the sign-in goes only to Bandcamp's own hosts (and the loopback test server).
func sendsCookie(host string) bool {
	host = strings.ToLower(host)
	return host == "bandcamp.com" || strings.HasSuffix(host, ".bandcamp.com") || host == "127.0.0.1"
}

func (c *Client) getJSON(ctx context.Context, p string, v any) error {
	resp, err := c.do(ctx, http.MethodGet, p, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, v)
}

func decode(resp *http.Response, v any) error {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrNotSignedIn
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Bandcamp: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return ErrNotSignedIn // an HTML page instead of data: not signed in
	}
	return nil
}

// decodeCallback reads an answer that is either plain JSON or JSON wrapped in a JavaScript
// callback, which is how the "statdownload" endpoint answers: Downloads.statResult({...}).
func decodeCallback(resp *http.Response, v any) error {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrNotSignedIn
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Bandcamp: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	start := bytes.IndexByte(b, '{')
	if k := bytes.Index(b, []byte("statResult")); k >= 0 { // skip the wrapper's own braces
		if p := bytes.IndexByte(b[k:], '('); p >= 0 {
			start = bytes.IndexByte(b[k+p:], '{')
			if start >= 0 {
				start += k + p
			}
		}
	}
	if start < 0 {
		return errors.New("Bandcamp's download answer wasn't readable")
	}
	// Decode just the first object: whatever follows it (the callback's closing) is ignored.
	if err := json.NewDecoder(bytes.NewReader(b[start:])).Decode(v); err != nil {
		return fmt.Errorf("Bandcamp's download answer wasn't readable: %w", err)
	}
	return nil
}

// Item is one purchase.
type Item struct {
	Title, Artist string
	Type          string // "album" or "track"
	URL           string // the item's public page
	DownloadPage  string // where the original files are offered ("" = none)
}

// Collection lists everything the account has bought (newest first).
func (c *Client) Collection(ctx context.Context) ([]Item, error) {
	var out []Item
	token := fmt.Sprintf("%d:0:a::", time.Now().Unix()+86400)
	for page := 0; page < 400; page++ {
		body, _ := json.Marshal(map[string]any{"fan_id": c.FanID, "older_than_token": token, "count": 100})
		resp, err := c.do(ctx, http.MethodPost, "/api/fancollection/1/collection_items", body)
		if err != nil {
			return nil, err
		}
		var r struct {
			Items []struct {
				Title     string `json:"item_title"`
				Artist    string `json:"band_name"`
				Type      string `json:"item_type"`
				URL       string `json:"item_url"`
				SaleType  string `json:"sale_item_type"`
				SaleID    int64  `json:"sale_item_id"`
				Purchased string `json:"purchased"`
			} `json:"items"`
			More        bool              `json:"more_available"`
			LastToken   string            `json:"last_token"`
			Redownloads map[string]string `json:"redownload_urls"`
		}
		err = decode(resp, &r)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, it := range r.Items {
			if it.Purchased == "" { // not bought (a gift not yet claimed, say)
				continue
			}
			out = append(out, Item{
				Title: it.Title, Artist: it.Artist, Type: it.Type, URL: it.URL,
				DownloadPage: r.Redownloads[fmt.Sprintf("%s%d", it.SaleType, it.SaleID)],
			})
		}
		if !r.More || r.LastToken == "" || r.LastToken == token {
			break
		}
		token = r.LastToken
	}
	return out, nil
}

// Format is one downloadable version of an item.
type Format struct {
	Name string // "flac", "mp3-320", ...
	URL  string
}

var blobRe = regexp.MustCompile(`data-blob="([^"]*)"`)

// formatRank orders formats best first; anything else ranks after these.
var formatRank = []string{"flac", "alac", "wav", "aiff-lossless", "mp3-320", "mp3-v0", "aac-hi", "vorbis", "mp3-128"}

// Formats reads an item's download page and lists what is offered, best first.
func (c *Client) Formats(ctx context.Context, page string) ([]Format, error) {
	resp, err := c.do(ctx, http.MethodGet, page, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bandcamp download page: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return ParseFormats(b)
}

// ParseFormats finds the download formats in a download page's embedded data.
func ParseFormats(page []byte) ([]Format, error) {
	m := blobRe.FindSubmatch(page)
	if m == nil {
		return nil, errors.New("no download data on the page (a bot check, or not a purchase)")
	}
	var blob struct {
		Items []struct {
			Downloads map[string]struct {
				URL string `json:"url"`
			} `json:"downloads"`
		} `json:"download_items"`
		Digital []struct {
			Downloads map[string]struct {
				URL string `json:"url"`
			} `json:"downloads"`
		} `json:"digital_items"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &blob); err != nil {
		return nil, err
	}
	downloads := map[string]string{}
	for _, it := range blob.Items {
		for k, v := range it.Downloads {
			downloads[k] = v.URL
		}
	}
	for _, it := range blob.Digital {
		for k, v := range it.Downloads {
			downloads[k] = v.URL
		}
	}
	var out []Format
	seen := map[string]bool{}
	for _, name := range formatRank {
		if u := downloads[name]; u != "" {
			out = append(out, Format{name, u})
			seen[name] = true
		}
	}
	for name, u := range downloads {
		if !seen[name] && u != "" {
			out = append(out, Format{name, u})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("this purchase has no digital download")
	}
	return out, nil
}

// Fetch downloads format f to dst (an album comes as a zip) and returns what was saved:
// the audio files, extracted into dir when it is a zip, or the one file.
func (c *Client) Fetch(ctx context.Context, f Format, dir string) ([]string, error) {
	// The page's link is turned into a one-off file link by Bandcamp's "statdownload".
	stat := strings.Replace(f.URL, "/download/", "/statdownload/", 1)
	sep := "?"
	if strings.Contains(stat, "?") {
		sep = "&"
	}
	rnd := make([]byte, 6)
	_, _ = rand.Read(rnd)
	stat += sep + ".vrs=1&.rand=" + hex.EncodeToString(rnd)
	var r struct {
		Result string `json:"result"`
		URL    string `json:"download_url"`
		Msg    string `json:"error_message"`
	}
	resp, err := c.do(ctx, http.MethodGet, stat, nil)
	if err != nil {
		return nil, err
	}
	err = decodeCallback(resp, &r)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if r.Result != "ok" || r.URL == "" {
		return nil, fmt.Errorf("Bandcamp wouldn't give the download: %s", firstNonEmpty(r.Msg, r.Result))
	}
	resp, err = c.do(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bandcamp download: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "bc-*.part")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return unpack(tmp.Name(), dir, resp.Header.Get("Content-Disposition"))
}

var audioExt = map[string]bool{".flac": true, ".mp3": true, ".m4a": true, ".ogg": true, ".opus": true, ".wav": true, ".aiff": true, ".aif": true}

const maxExtracted = 2 << 30 // refuse a zip that unpacks to more than 2 GiB

// unpack extracts the audio files of a zip into dir (flat, safe names), or keeps a lone
// audio file under the name Bandcamp gave it.
func unpack(file, dir, disposition string) ([]string, error) {
	zr, err := zip.OpenReader(file)
	if err != nil { // not a zip: a single track
		name := "track"
		if i := strings.Index(disposition, "filename="); i >= 0 {
			name = strings.Trim(disposition[i+len("filename="):], `"; `)
			if u, e := url.QueryUnescape(name); e == nil {
				name = u
			}
		}
		name = safeName(name)
		if !audioExt[strings.ToLower(path.Ext(name))] {
			return nil, errors.New("Bandcamp sent something that isn't audio")
		}
		dst := uniquePath(dir, name)
		if err := os.Rename(file, dst); err != nil {
			return nil, err
		}
		return []string{dst}, nil
	}
	defer zr.Close()
	var out []string
	var total uint64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !audioExt[strings.ToLower(path.Ext(f.Name))] {
			continue
		}
		total += f.UncompressedSize64
		if total > maxExtracted {
			return nil, errors.New("the download is larger than expected")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		dst := uniquePath(dir, safeName(path.Base(f.Name)))
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			rc.Close()
			return nil, err
		}
		_, err = io.Copy(w, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		rc.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	if len(out) == 0 {
		return nil, errors.New("the download had no audio in it")
	}
	return out, nil
}

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

func safeName(s string) string {
	s = strings.TrimSpace(unsafeName.ReplaceAllString(s, "_"))
	s = strings.Trim(s, ". ")
	if s == "" {
		return "file"
	}
	if len(s) > 150 {
		ext := path.Ext(s)
		s = s[:150-len(ext)] + ext
	}
	return s
}

func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); err != nil {
			return p
		}
		ext := path.Ext(name)
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Normalize makes a Bandcamp item link comparable: lower-case host, no query or fragment,
// no trailing slash.
func Normalize(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname()) + strings.TrimRight(u.Path, "/")
}
