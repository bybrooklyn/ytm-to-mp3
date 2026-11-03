package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/store"
)

const (
	chunkSize = 10 << 20 // googlevideo throttles unchunked downloads; ~10 MiB chunks stay fast
	fetchConn = 4        // parallel chunk connections per file
)

var errUnfetchable = &Error{kindTransient, "stream URL rejected"}

// contentLength returns the total size from the URL's clen parameter or the yt-dlp metadata.
func contentLength(r *Resolved) int64 {
	if u, err := url.Parse(r.URL); err == nil {
		if n, err := strconv.ParseInt(u.Query().Get("clen"), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return r.Size
}

// canFetch reports whether ytm can download the stream itself (plain HTTPS, known size).
func canFetch(r *Resolved) bool {
	n := contentLength(r)
	return r.URL != "" && (r.Protocol == "https" || r.Protocol == "http") && n > 0 && n <= maxFetchSize
}

// maxFetchSize caps the size we trust from clen / metadata; a bogus value would otherwise
// create a huge sparse file. Anything bigger goes through yt-dlp instead.
const maxFetchSize = 2 << 30

// fetch downloads r to dst using parallel ranged requests. onBytes is told about progress.
// A 403/404 means the URL is unusable (errUnfetchable); 429 is reported as throttling.
func fetch(ctx context.Context, client *http.Client, r *Resolved, dst string, onBytes func(int64), onThrottle func()) error {
	total := contentLength(r)
	if total <= 0 || total > maxFetchSize {
		return errUnfetchable
	}
	_ = os.Remove(dst) // stale file from an earlier attempt; the temp dir is private
	f, err := store.CreateExcl(dst, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(total); err != nil {
		return err
	}

	n := int((total + chunkSize - 1) / chunkSize)
	next := int64(-1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	fail := func(err error) { once.Do(func() { firstErr = err; cancel() }) }
	for w := 0; w < min(fetchConn, n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(atomic.AddInt64(&next, 1))
				if i >= n || ctx.Err() != nil {
					return
				}
				start := int64(i) * chunkSize
				end := min(start+chunkSize, total) - 1
				if err := fetchChunk(ctx, client, r, f, start, end, onBytes, onThrottle); err != nil {
					fail(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func chunkRequest(ctx context.Context, r *Resolved, start, end int64) (*http.Request, error) {
	u := r.URL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	// googlevideo wants the range as a query parameter (what browsers do); others get a header.
	if pu, perr := url.Parse(u); perr == nil && len(pu.Host) > 0 && isGoogleVideo(pu.Hostname()) {
		q := pu.Query()
		q.Set("range", fmt.Sprintf("%d-%d", start, end))
		pu.RawQuery = q.Encode()
		req.URL = pu
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	}
	return req, nil
}

// isGoogleVideo matches googlevideo.com and its subdomains only (not evilgooglevideo.com).
func isGoogleVideo(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
}

// stallAfter is how long a chunk may go without receiving a single byte before the attempt is
// abandoned. Some exits can resolve a video yet can't pull data from Google's CDN; waiting
// the full chunk timeout on each of them is what made whole runs take minutes.
var stallAfter = 10 * time.Second

// errStalled means this exit isn't delivering data; the track should move to another exit.
var errStalled = &Error{kindTransient, "stream stalled (no data from this exit)"}

func fetchChunk(ctx context.Context, client *http.Client, r *Resolved, f *os.File, start, end int64, onBytes func(int64), onThrottle func()) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		done, err := fetchAttempt(ctx, client, r, f, start, end, onBytes, onThrottle)
		if done {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// fetchAttempt is one try at one chunk. done=false means "worth retrying".
func fetchAttempt(ctx context.Context, client *http.Client, r *Resolved, f *os.File, start, end int64, onBytes func(int64), onThrottle func()) (done bool, err error) {
	// Each 10 MB chunk gets an overall deadline, and a stall watchdog that fires if no
	// bytes arrive for stallAfter.
	cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
	defer ccancel()
	var stalled atomic.Bool
	wd := time.AfterFunc(stallAfter, func() { stalled.Store(true); ccancel() })
	defer wd.Stop()
	progress := func(n int64) { wd.Reset(stallAfter); onBytes(n) }
	stall := func() (bool, error) { return true, errStalled }

	req, err := chunkRequest(cctx, r, start, end)
	if err != nil {
		return true, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if stalled.Load() {
			return stall()
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return false, &Error{kindTransient, err.Error()}
	}
	defer resp.Body.Close()
	wd.Reset(stallAfter) // headers arrived; now the body has to flow
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusTooManyRequests:
		onThrottle()
		return false, &Error{kindThrottled, "HTTP 429 from stream server"}
	case http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return true, fmt.Errorf("%w (HTTP %s)", errUnfetchable, resp.Status)
	default:
		return false, &Error{kindTransient, "stream server: " + resp.Status}
	}
	want := end - start + 1
	if resp.StatusCode == http.StatusOK && resp.ContentLength != want {
		// A plain 200 is only trustworthy when its length is exactly the range asked
		// for (googlevideo answers range= queries that way). Anything else means the
		// server ignored the range and is sending the file from the start: writing it
		// at this offset would silently corrupt the track.
		return true, fmt.Errorf("%w (HTTP 200 with length %d, wanted %d)", errUnfetchable, resp.ContentLength, want)
	}
	n, err := copyAt(f, resp.Body, start, want, progress)
	if err == nil && n == want {
		return true, nil
	}
	if stalled.Load() {
		return stall()
	}
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	return false, &Error{kindTransient, fmt.Sprintf("short read (%d/%d bytes)", n, want)}
}

// copyAt writes up to limit bytes of r into f at off, reporting progress.
func copyAt(f *os.File, r io.Reader, off, limit int64, onBytes func(int64)) (int64, error) {
	buf := make([]byte, 64<<10)
	var done int64
	for done < limit {
		n, err := r.Read(buf[:min(int64(len(buf)), limit-done)])
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], off+done); werr != nil {
				return done, werr
			}
			done += int64(n)
			onBytes(int64(n))
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return done, err
		}
	}
	return done, nil
}

// getFile downloads a small file (cover art).
func getFile(ctx context.Context, client *http.Client, rawurl, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cover: %s", resp.Status)
	}
	_ = os.Remove(dst)
	f, err := store.CreateExcl(dst, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 20<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
