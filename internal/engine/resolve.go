package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/model"
	"github.com/bybrooklyn/ytm-to-mp3/internal/names"
)

// kind classifies a failure so the pipeline knows whether to retry, back off, or give up.
type kind int

const (
	kindTransient kind = iota
	kindThrottled
	kindPermanent
)

// Error is a classified failure.
type Error struct {
	Kind kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func kindOf(err error) kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return kindTransient
}

var (
	throttleMarks = []string{
		"http error 429", "too many requests", "not a bot", "rate-limited", "rate limited",
		"try again later", "http error 403", "this content isn't available",
	}
	permanentMarks = []string{
		"video unavailable", "is unavailable", "private video", "has been removed", "is not available",
		"not available in your country", "blocked it", "copyright", "account associated",
		"has been terminated", "members-only", "premieres in", "live event will begin",
		"sign in to confirm your age", "age-restricted", "no longer available", "does not exist",
	}
	errPrefx = regexp.MustCompile(`^ERROR:\s*(\[[^\]]+\]\s*[\w-]+:\s*)?`)
)

// maxMsg bounds how much tool output ends up in errors, the store and the terminal.
const maxMsg = 400

func classify(stderr string) *Error {
	// yt-dlp stderr is attacker-influenced (titles, server replies) and can echo the proxy
	// URL: strip every escape sequence and credential before it is stored or shown.
	msg := names.Clean(stderr)
	if i := strings.LastIndex(msg, "ERROR:"); i >= 0 {
		msg = msg[i:]
	}
	msg = strings.TrimSpace(errPrefx.ReplaceAllString(strings.TrimSpace(msg), ""))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if r := []rune(msg); len(r) > maxMsg {
		msg = string(r[:maxMsg]) + "…"
	}
	if msg == "" {
		msg = "yt-dlp failed"
	}
	low := strings.ToLower(msg)
	for _, m := range permanentMarks {
		if strings.Contains(low, m) {
			return &Error{kindPermanent, msg}
		}
	}
	for _, m := range throttleMarks {
		if strings.Contains(low, m) {
			return &Error{kindThrottled, msg}
		}
	}
	return &Error{kindTransient, msg}
}

// Resolved is everything needed to fetch and tag one track.
type Resolved struct {
	URL      string
	Headers  map[string]string
	Ext      string
	Acodec   string
	Protocol string
	Size     int64
	Title    string
	Artists  []string
	Album    string
	Year     int
	Date     string // YYYY-MM-DD when YouTube knows the release or upload date
	Genre    string
	Track    int
	Duration float64
	Thumb    string
}

type ytInfo struct {
	ID             string            `json:"id"`
	URL            string            `json:"url"`
	Ext            string            `json:"ext"`
	Acodec         string            `json:"acodec"`
	Protocol       string            `json:"protocol"`
	Filesize       float64           `json:"filesize"`
	FilesizeApprox float64           `json:"filesize_approx"`
	Headers        map[string]string `json:"http_headers"`
	Title          string            `json:"title"`
	Track          string            `json:"track"`
	Artist         string            `json:"artist"`
	Artists        []string          `json:"artists"`
	Album          string            `json:"album"`
	Channel        string            `json:"channel"`
	Uploader       string            `json:"uploader"`
	ReleaseYear    int               `json:"release_year"`
	ReleaseDate    string            `json:"release_date"`
	UploadDate     string            `json:"upload_date"`
	Genre          string            `json:"genre"`
	Genres         []string          `json:"genres"`
	TrackNumber    int               `json:"track_number"`
	Duration       float64           `json:"duration"`
	Thumbnail      string            `json:"thumbnail"`
	Thumbnails     []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"thumbnails"`
}

// playerClients are tried in turn on retries; a different client often gets past a block.
var playerClients = []string{"", "tv_simply", "tv", "web_safari", "mweb"}

type ytdlp struct {
	tools deps.Tools
	cache string
	proxy string // SOCKS/HTTP proxy for this exit, or ""
}

func (y ytdlp) baseArgs(clientIdx int) []string {
	a := []string{"--ignore-config", "--no-plugin-dirs", "--no-warnings", "--no-playlist", "--socket-timeout", "20"}
	for _, r := range y.tools.JSRuntimes {
		a = append(a, "--js-runtimes", r)
	}
	if y.cache != "" {
		a = append(a, "--cache-dir", y.cache)
	}
	// The proxy (which carries a password) goes through the environment, not argv, where
	// other local users could read it with ps. See env().
	if c := playerClients[clientIdx%len(playerClients)]; c != "" {
		a = append(a, "--extractor-args", "youtube:player_client="+c)
	}
	return a
}

// env is the process environment for yt-dlp, with this exit's proxy set the way yt-dlp
// (via urllib) reads system proxies.
func (y ytdlp) env() []string {
	if y.proxy == "" {
		return nil // inherit
	}
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		k := strings.ToLower(kv[:max(0, strings.IndexByte(kv, '='))])
		if k == "http_proxy" || k == "https_proxy" || k == "all_proxy" || k == "no_proxy" {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range []string{"http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		out = append(out, k+"="+y.proxy)
	}
	return append(out, "no_proxy=", "NO_PROXY=")
}

// runRaw runs yt-dlp and returns its stdout and stderr even when it exits non-zero (err is
// then the exit error); callers that tolerate partial failure use this.
func (y ytdlp) runRaw(ctx context.Context, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, y.tools.YtDlp, args...)
	cmd.Env = y.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.String(), err
}

func (y ytdlp) run(ctx context.Context, args ...string) ([]byte, error) {
	out, stderr, err := y.runRaw(ctx, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classify(stderr)
	}
	return out, nil
}

// Resolve asks yt-dlp for the best audio stream and metadata of one video.
func (y ytdlp) Resolve(ctx context.Context, id string, clientIdx int) (*Resolved, error) {
	if !model.ValidID(id) {
		return nil, &Error{kindPermanent, "invalid video id"}
	}
	args := append(y.baseArgs(clientIdx), "-J", "-f", "bestaudio/best", "https://www.youtube.com/watch?v="+id)
	b, err := y.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var in ytInfo
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, &Error{kindTransient, "unreadable yt-dlp output"}
	}
	return resolvedFrom(in), nil
}

func resolvedFrom(in ytInfo) *Resolved {
	r := &Resolved{
		URL: in.URL, Headers: in.Headers, Ext: in.Ext, Acodec: in.Acodec, Protocol: in.Protocol,
		// Only an exact size is safe: an approximate one would truncate or over-read the
		// track. Without one (and without clen in the URL) yt-dlp downloads it instead.
		Size:  int64(in.Filesize),
		Title: firstNonEmpty(in.Track, in.Title), Album: in.Album, Year: in.ReleaseYear, Duration: in.Duration,
		Date: isoDate(firstNonEmpty(in.ReleaseDate, in.UploadDate)), Track: in.TrackNumber,
		Genre: firstNonEmpty(in.Genre, strings.Join(in.Genres, "; ")),
		Thumb: bestThumb(in),
	}
	switch {
	case len(in.Artists) > 0:
		r.Artists = in.Artists
	case in.Artist != "":
		r.Artists = []string{in.Artist}
	default:
		if c := strings.TrimSuffix(firstNonEmpty(in.Channel, in.Uploader), " - Topic"); c != "" {
			r.Artists = []string{c}
		}
	}
	return r
}

// batchResult is the outcome for one id of a batch resolve.
type batchResult struct {
	res *Resolved
	err error
}

// ResolveBatch resolves several videos with a single yt-dlp process (one process start and
// one JS-runtime warm-up for all of them). The result has an entry per id it could attribute;
// ids missing from it had no recognisable output and should be resolved on their own.
// A non-nil error means the whole batch failed (cancelled, or yt-dlp could not run).
func (y ytdlp) ResolveBatch(ctx context.Context, ids []string, clientIdx int) (map[string]batchResult, error) {
	args := append(y.baseArgs(clientIdx), "--ignore-errors", "-j", "-f", "bestaudio/best")
	for _, id := range ids {
		if !model.ValidID(id) {
			return nil, &Error{kindPermanent, "invalid video id"}
		}
		args = append(args, "https://www.youtube.com/watch?v="+id)
	}
	out, stderr, err := y.runRaw(ctx, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && len(out) == 0 && strings.TrimSpace(stderr) == "" {
		return nil, classify("") // could not even run
	}
	return parseBatch(ids, out, stderr), nil
}

var errLineID = regexp.MustCompile(`^ERROR:\s*\[[^\]]+\]\s*([\w-]{11}):`)

// parseBatch maps yt-dlp's `-j --ignore-errors` output back to ids: one JSON object per
// line on stdout, one "ERROR: [youtube] <id>: ..." line per failed video on stderr.
func parseBatch(ids []string, stdout []byte, stderr string) map[string]batchResult {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	res := make(map[string]batchResult, len(ids))
	for _, line := range bytes.Split(stdout, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var in ytInfo
		if json.Unmarshal(line, &in) != nil || !want[in.ID] {
			continue
		}
		res[in.ID] = batchResult{res: resolvedFrom(in)}
	}
	var loose []string // error lines we couldn't tie to an id
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(names.Clean(line))
		if !strings.HasPrefix(line, "ERROR:") {
			continue
		}
		if m := errLineID.FindStringSubmatch(line); m != nil && want[m[1]] {
			if _, ok := res[m[1]]; !ok {
				res[m[1]] = batchResult{err: classify(line)}
			}
			continue
		}
		loose = append(loose, line)
	}
	// One video unaccounted for and one unattributed error: they belong together.
	var missing []string
	for _, id := range ids {
		if _, ok := res[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 1 && len(loose) == 1 {
		res[missing[0]] = batchResult{err: classify(loose[0])}
	}
	return res
}

// isoDate turns yt-dlp's YYYYMMDD into YYYY-MM-DD ("" if malformed).
func isoDate(d string) string {
	if len(d) != 8 {
		return ""
	}
	return d[:4] + "-" + d[4:6] + "-" + d[6:]
}

func bestThumb(in ytInfo) string {
	best, area := in.Thumbnail, 0
	for _, t := range in.Thumbnails {
		if a := t.Width * t.Height; a > area && !strings.HasSuffix(t.URL, ".webp") {
			best, area = t.URL, a
		}
	}
	return best
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// outputTemplate is the yt-dlp -o template for a download: a fixed "dl-" prefix plus the id,
// with '%' escaped in both the directory and the id so nothing is ever interpreted as a
// yt-dlp field expression.
func outputTemplate(dir, id string) string {
	esc := func(s string) string { return strings.ReplaceAll(s, "%", "%%") }
	return esc(filepath.Join(dir, "dl-"+id)) + ".%(ext)s"
}

// Download lets yt-dlp fetch the audio itself. This is the slower but most robust
// path, used when the direct stream URL can't be fetched by ytm.
func (y ytdlp) Download(ctx context.Context, id, dir string, clientIdx int) (string, error) {
	if !model.ValidID(id) {
		return "", &Error{kindPermanent, "invalid video id"}
	}
	args := append(y.baseArgs(clientIdx), "-f", "bestaudio/best", "--no-part", "--quiet",
		"--concurrent-fragments", "4", "--http-chunk-size", "10M",
		"-o", outputTemplate(dir, id), "--print", "after_move:filepath",
		"https://www.youtube.com/watch?v="+id)
	b, err := y.run(ctx, args...)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(p, '\n'); i >= 0 {
		p = strings.TrimSpace(p[i+1:])
	}
	if p == "" {
		return "", &Error{kindTransient, "yt-dlp produced no file"}
	}
	return p, nil
}

// Expand turns a playlist / album URL into tracks.
func (y ytdlp) Expand(ctx context.Context, url string) ([]model.Track, error) {
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-warnings", "--flat-playlist", "-J", "--", url}
	b, err := y.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var pl struct {
		ID      string `json:"id"`
		Entries []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, err
	}
	var out []model.Track
	for _, e := range pl.Entries {
		if model.ValidID(e.ID) {
			out = append(out, model.Track{ID: e.ID, Title: e.Title})
		}
	}
	if len(out) == 0 && model.ValidID(pl.ID) {
		out = append(out, model.Track{ID: pl.ID})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no videos found at %s", url)
	}
	return out, nil
}

// ExpandURL is the exported entry point for the CLI.
// proxy, if set, is the exit to look the link up through ("" = your own connection).
func ExpandURL(ctx context.Context, tools deps.Tools, proxy, url string) ([]model.Track, error) {
	return ytdlp{tools: tools, proxy: proxy}.Expand(ctx, url)
}
