package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

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
	// Checked before permanentMarks (whose "is not available" would match them too).
	exitMarks = []string{
		// YouTube gives a flagged IP a stripped-down answer with no audio formats; the
		// same track resolves fine from another exit
		"requested format is not available",
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
	for _, m := range exitMarks {
		if strings.Contains(low, m) {
			return &Error{kindThrottled, msg}
		}
	}
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

type ytdlp struct {
	tools  deps.Tools
	cache  string
	proxy  string // SOCKS/HTTP proxy for this exit, or ""
	format string // target format, to pick the stream that copies straight into it
	// cookies, if set, is a YouTube Premium cookie file: only the upgrade pass uses one,
	// with the web_music client (the only one that still offers the Premium formats).
	cookies string
	// only, if set, replaces the format choice with no fallback (the Premium pass asks
	// for "774/141" and nothing else, so it can never quietly re-download 128k audio).
	only string
}

// selector is the yt-dlp format choice: the best stream of the target's own codec first,
// so the file is a remux (identical audio) rather than a re-encode. 774 (Opus ~256k) and
// 141 (AAC 256k) only exist for YouTube Premium; without it they are simply skipped.
func (y ytdlp) selector() string {
	if y.only != "" {
		return y.only
	}
	switch y.format {
	case "opus":
		return "774/251/bestaudio[acodec=opus]/bestaudio/best"
	case "m4a":
		return "141/140/bestaudio[acodec^=mp4a]/bestaudio/best"
	}
	return "bestaudio/best" // re-encoded anyway: take the best of anything
}

// baseArgs are the options every lookup and download shares. yt-dlp's default player
// clients are used: in testing (2026-11) every alternative either returned no usable stream
// or one Google refused. The proxy (which carries a password) goes through the
// environment, not argv, where other local users could read it with ps. See env().
func (y ytdlp) baseArgs() []string {
	a := []string{"--ignore-config", "--no-plugin-dirs", "--no-warnings", "--no-playlist", "--socket-timeout", "20"}
	for _, r := range y.tools.JSRuntimes {
		a = append(a, "--js-runtimes", r)
	}
	if y.cache != "" {
		a = append(a, "--cache-dir", y.cache)
	}
	if y.cookies != "" {
		a = append(a, "--cookies", y.cookies, "--extractor-args", "youtube:player_client=web_music,default")
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

// maxLine bounds one line of yt-dlp output (a -j info line can run to a few MB).
const maxLine = 32 << 20

// splitLines is bufio.ScanLines that also breaks on '\r' (progress updates).
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// runLines runs yt-dlp and hands each output line to onLine as it is printed (stdout lines
// with err=false, stderr lines with err=true), so callers can act before the process ends.
// It returns the tail of stderr for classify, and the exit error.
func (y ytdlp) runLines(ctx context.Context, onLine func(line []byte, stderr bool), args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, y.tools.YtDlp, args...)
	cmd.Env = y.env()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var (
		wg   sync.WaitGroup
		emu  sync.Mutex
		tail []string // last stderr lines, for the error message
	)
	read := func(r io.Reader, isErr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), maxLine)
		sc.Split(splitLines)
		for sc.Scan() {
			line := sc.Bytes()
			if isErr && !bytes.HasPrefix(line, []byte(progPrefix)) {
				emu.Lock()
				if tail = append(tail, string(line)); len(tail) > 50 {
					tail = tail[len(tail)-50:]
				}
				emu.Unlock()
			}
			if onLine != nil {
				onLine(line, isErr)
			}
		}
		io.Copy(io.Discard, r) // an over-long line: keep the pipe drained so yt-dlp can't block
	}
	wg.Add(2)
	go read(stdout, false)
	go read(stderr, true)
	wg.Wait() // all output read before Wait closes the pipes
	err = cmd.Wait()
	return strings.Join(tail, "\n"), err
}

// run runs yt-dlp and returns its stdout; a failure is classified from its stderr.
func (y ytdlp) run(ctx context.Context, args ...string) ([]byte, error) {
	var out bytes.Buffer // only the stdout reader writes it
	stderr, err := y.runLines(ctx, func(line []byte, isErr bool) {
		if !isErr {
			out.Write(line)
			out.WriteByte('\n')
		}
	}, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classify(stderr)
	}
	return out.Bytes(), nil
}

// Resolve asks yt-dlp for the best audio stream and metadata of one video.
func (y ytdlp) Resolve(ctx context.Context, id string) (*Resolved, error) {
	if !model.ValidID(id) {
		return nil, &Error{kindPermanent, "invalid video id"}
	}
	args := append(y.baseArgs(), "-J", "-f", y.selector(), "https://www.youtube.com/watch?v="+id)
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
	// The upload date is when the video went up, not when the song came out: only a song
	// with no release date or year at all gets it.
	date := in.ReleaseDate
	if date == "" && in.ReleaseYear <= 0 {
		date = in.UploadDate
	}
	r := &Resolved{
		URL: in.URL, Headers: in.Headers, Ext: in.Ext, Acodec: in.Acodec, Protocol: in.Protocol,
		// Only an exact size is safe: an approximate one would truncate or over-read the
		// track. Without one (and without clen in the URL) yt-dlp downloads it instead.
		Size:  int64(in.Filesize),
		Title: firstNonEmpty(in.Track, in.Title), Album: in.Album, Year: in.ReleaseYear, Duration: in.Duration,
		Date: isoDate(date), Track: in.TrackNumber,
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
// onOK, if set, is called for each video the moment yt-dlp prints it, so its download can
// start while the rest of the batch (resolved one after another) is still going; the
// returned map includes those too.
func (y ytdlp) ResolveBatch(ctx context.Context, ids []string, onOK func(id string, r *Resolved)) (map[string]batchResult, error) {
	args := append(y.baseArgs(), "--ignore-errors", "-j", "-f", y.selector())
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !model.ValidID(id) {
			return nil, &Error{kindPermanent, "invalid video id"}
		}
		want[id] = true
		args = append(args, "https://www.youtube.com/watch?v="+id)
	}
	var mu sync.Mutex
	got := map[string]*Resolved{}
	sawOutput := false
	stderr, err := y.runLines(ctx, func(line []byte, isErr bool) {
		if isErr {
			return
		}
		id, r := parseInfoLine(line, want)
		if r == nil {
			return
		}
		mu.Lock()
		sawOutput = true
		_, dup := got[id]
		got[id] = r
		mu.Unlock()
		if !dup && onOK != nil {
			onOK(id, r)
		}
	}, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !sawOutput && strings.TrimSpace(stderr) == "" {
		return nil, classify("") // could not even run
	}
	res := parseBatch(ids, nil, stderr)
	for id, r := range got {
		res[id] = batchResult{res: r}
	}
	return res, nil
}

// parseInfoLine reads one `-j` line; nil unless it is an info object for a wanted id.
func parseInfoLine(line []byte, want map[string]bool) (string, *Resolved) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return "", nil
	}
	var in ytInfo
	if json.Unmarshal(line, &in) != nil || !want[in.ID] {
		return "", nil
	}
	return in.ID, resolvedFrom(in)
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
		if id, r := parseInfoLine(line, want); r != nil {
			res[id] = batchResult{res: r}
		}
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
// onProgress, if set, is told the bytes downloaded so far and the expected total (0 when
// yt-dlp doesn't know it yet).
func (y ytdlp) Download(ctx context.Context, id, dir string, onProgress func(got, total int64)) (string, error) {
	if !model.ValidID(id) {
		return "", &Error{kindPermanent, "invalid video id"}
	}
	args := append(y.baseArgs(), "-f", y.selector(), "--no-part", "--quiet",
		"--concurrent-fragments", "4", "--http-chunk-size", "10M",
		// --quiet hides the normal progress bar; ask for a machine-readable one instead
		"--progress", "--newline", "--progress-template", progTemplate,
		"-o", outputTemplate(dir, id), "--print", "after_move:filepath",
		"https://www.youtube.com/watch?v="+id)
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
			path = s // the after_move:filepath line
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
	return path, nil
}

// progPrefix marks ytm's own progress lines in yt-dlp's output.
const progPrefix = "ytmprog "

var progTemplate = "download:" + progPrefix +
	"%(progress.downloaded_bytes)s %(progress.total_bytes,progress.total_bytes_estimate)s"

// parseProgress reads one progress line ("ytmprog <got> <total>"; either may be NA).
func parseProgress(s string) (got, total int64, ok bool) {
	rest, found := strings.CutPrefix(s, progPrefix)
	if !found {
		return 0, 0, false
	}
	f := strings.Fields(rest)
	if len(f) != 2 {
		return 0, 0, true
	}
	num := func(v string) int64 {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 {
			return 0
		}
		return int64(n)
	}
	return num(f[0]), num(f[1]), true
}

// musicHit is one song result from a YouTube Music search.
type musicHit struct {
	ID, Title string
}

// SearchMusic looks query up on YouTube Music's song search and returns up to n results.
// It finds the current upload of a song whose old video id has been taken down.
func (y ytdlp) SearchMusic(ctx context.Context, query string, n int) ([]musicHit, error) {
	u := "https://music.youtube.com/search?q=" + url.QueryEscape(query) + "#songs"
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-warnings", "--flat-playlist",
		"--playlist-items", fmt.Sprintf("1:%d", n), "-J", "--", u}
	b, err := y.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var pl struct {
		Entries []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, &Error{kindTransient, "unreadable yt-dlp output"}
	}
	var out []musicHit
	for _, e := range pl.Entries {
		if model.ValidID(e.ID) {
			out = append(out, musicHit{ID: e.ID, Title: e.Title})
		}
	}
	return out, nil
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
