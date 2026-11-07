// Package httpfetch downloads one file over HTTP so that an interrupted or flaky run
// loses nothing: bytes go to a ".part" file, a retry or a later run resumes it with a Range
// request (when the server's validators still match), and the finished file is renamed into
// place only once it is complete. A truncated file is never left at the destination.
package httpfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/jobs"
)

// Options tune one download. The zero value is usable.
type Options struct {
	Client *http.Client // nil = http.DefaultClient (no overall timeout is applied to the body)
	Header http.Header  // extra request headers
	// Part is where the partial download lives (default dst + ".part"); its validators go in
	// Part + ".meta". Both are removed when the file completes.
	Part     string
	Progress func(done, total int64)
	// Check sees the first bytes of a fresh (not resumed) download and may reject it, e.g. a
	// login page where audio was expected. A rejected download is discarded, not resumed.
	Check func(first []byte) error
	// Attempts is how many tries a transient failure gets (default 4), with jittered
	// exponential backoff between them.
	Attempts int
	// Backoff is the first retry delay (default 1s); tests set it small.
	Backoff time.Duration
}

type meta struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lm,omitempty"`
	Total        int64  `json:"total,omitempty"`
}

// Download fetches url to dst. On context cancellation the .part file is kept so the next
// call continues from it.
func Download(ctx context.Context, url, dst string, o Options) error {
	if o.Part == "" {
		o.Part = dst + ".part"
	}
	if o.Attempts < 1 {
		o.Attempts = 4
	}
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if err := os.MkdirAll(filepath.Dir(o.Part), 0o755); err != nil {
		return err
	}
	var err error
	for attempt := 0; attempt < o.Attempts; attempt++ {
		if attempt > 0 {
			d := o.Backoff << (attempt - 1)
			d = d/2 + time.Duration(rand.Int63n(int64(d)))
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var retry bool
		retry, err = once(ctx, url, dst, o)
		if err == nil || !retry || ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// once makes one request. retry says whether trying again may help.
func once(ctx context.Context, url, dst string, o Options) (retry bool, err error) {
	var m meta
	var have int64
	if b, rerr := os.ReadFile(o.Part + ".meta"); rerr == nil && json.Unmarshal(b, &m) == nil {
		if st, serr := os.Stat(o.Part); serr == nil {
			have = st.Size()
		}
	}
	if have == 0 {
		m = meta{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	for k, v := range o.Header {
		req.Header[k] = v
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
		if m.ETag != "" {
			req.Header.Set("If-Range", m.ETag)
		} else if m.LastModified != "" {
			req.Header.Set("If-Range", m.LastModified)
		}
	}
	c := o.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()

	resume := false
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		start, total := contentRange(resp.Header.Get("Content-Range"))
		validators := (m.ETag == "" || m.ETag == resp.Header.Get("ETag")) &&
			(m.ETag != "" || m.LastModified == "" || m.LastModified == resp.Header.Get("Last-Modified"))
		if start == have && validators {
			resume = true
			if total > 0 {
				m.Total = total
			}
		} else {
			discard(o.Part)
			return true, errors.New("the server's copy changed; starting over")
		}
	case resp.StatusCode == http.StatusOK:
		have = 0 // a whole file (the server ignored Range, or the validators changed)
		m = meta{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Total: resp.ContentLength}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		discard(o.Part)
		return true, errors.New("the partial download no longer matches; starting over")
	default:
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		retryable := resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 429
		return retryable, &jobs.StatusError{Status: resp.StatusCode, Msg: msg}
	}

	flags := os.O_WRONLY | os.O_CREATE
	if resume {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		have = 0
	}
	f, err := os.OpenFile(o.Part, flags, 0o644)
	if err != nil {
		return false, err
	}
	if b, merr := json.Marshal(m); merr == nil {
		_ = os.WriteFile(o.Part+".meta", b, 0o644)
	}
	total := m.Total
	if total <= 0 && resp.ContentLength > 0 {
		total = have + resp.ContentLength
	}
	done := have
	buf := make([]byte, 128<<10)
	first := !resume
	for {
		if cerr := ctx.Err(); cerr != nil {
			f.Close() // keep the part for the next run
			return false, cerr
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if first {
				first = false
				if o.Check != nil {
					if cerr := o.Check(buf[:n]); cerr != nil {
						f.Close()
						discard(o.Part)
						return false, cerr
					}
				}
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return false, werr
			}
			done += int64(n)
			if o.Progress != nil {
				o.Progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close() // keep what arrived: the retry resumes from it
			return true, rerr
		}
	}
	if total > 0 && done != total {
		f.Close()
		return true, fmt.Errorf("%w: got %d of %d bytes", io.ErrUnexpectedEOF, done, total)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if done == 0 {
		discard(o.Part)
		return false, errors.New("the download was empty")
	}
	if err := os.Rename(o.Part, dst); err != nil {
		return false, err
	}
	_ = os.Remove(o.Part + ".meta")
	return false, nil
}

func discard(part string) {
	_ = os.Remove(part)
	_ = os.Remove(part + ".meta")
}

// contentRange parses "bytes START-END/TOTAL".
func contentRange(h string) (start, total int64) {
	h = strings.TrimPrefix(h, "bytes ")
	rng, tot, _ := strings.Cut(h, "/")
	s, _, _ := strings.Cut(rng, "-")
	start, _ = strconv.ParseInt(s, 10, 64)
	total, _ = strconv.ParseInt(tot, 10, 64)
	return start, total
}

// Sweep deletes partial downloads (".part" and ".part.meta" files) in dir that haven't been
// touched for maxAge, so abandoned ones don't pile up.
func Sweep(dir string, maxAge time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".part") || strings.HasSuffix(n, ".part.meta")) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}
