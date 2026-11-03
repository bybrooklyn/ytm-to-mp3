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
	return r.URL != "" && (r.Protocol == "https" || r.Protocol == "http") && contentLength(r) > 0
}

// fetch downloads r to dst using parallel ranged requests. onBytes is told about progress.
// A 403/404 means the URL is unusable (errUnfetchable); 429 is reported as throttling.
func fetch(ctx context.Context, client *http.Client, r *Resolved, dst string, onBytes func(int64), onThrottle func()) error {
	total := contentLength(r)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
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
	if pu, perr := url.Parse(u); perr == nil && len(pu.Host) > 0 && hasSuffix(pu.Host, "googlevideo.com") {
		q := pu.Query()
		q.Set("range", fmt.Sprintf("%d-%d", start, end))
		pu.RawQuery = q.Encode()
		req.URL = pu
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	}
	return req, nil
}

func hasSuffix(s, suffix string) bool { return strings.HasSuffix(s, suffix) }

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
		// A stalled stream must not hang a worker forever: each 10 MB chunk gets a deadline.
		cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
		defer ccancel()
		req, err := chunkRequest(cctx, r, start, end)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = &Error{kindTransient, err.Error()}
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
		case http.StatusTooManyRequests:
			resp.Body.Close()
			onThrottle()
			lastErr = &Error{kindThrottled, "HTTP 429 from stream server"}
			continue
		case http.StatusForbidden, http.StatusNotFound, http.StatusGone:
			resp.Body.Close()
			return errUnfetchable
		default:
			resp.Body.Close()
			lastErr = &Error{kindTransient, "stream server: " + resp.Status}
			continue
		}
		want := end - start + 1
		if resp.StatusCode == http.StatusOK && resp.ContentLength > want+1 {
			// Server ignored the range and is sending the whole file: not usable in chunks.
			resp.Body.Close()
			return errUnfetchable
		}
		n, err := copyAt(f, resp.Body, start, want, onBytes)
		resp.Body.Close()
		if err == nil && n == want {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = &Error{kindTransient, fmt.Sprintf("short read (%d/%d bytes)", n, want)}
	}
	return lastErr
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
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 20<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
