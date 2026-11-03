package deps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// There is no flat client Timeout: big downloads on slow links are fine as
// long as bytes keep flowing. Instead connection setup and response headers
// are bounded, a read that makes no progress for stallTimeout is aborted, and
// each download has a generous overall cap.
var (
	httpClient = &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
	stallTimeout    = 60 * time.Second
	downloadTimeout = 2 * time.Hour
	fetchTimeout    = 2 * time.Minute
)

// stallBody cancels the request when no bytes arrive for d.
type stallBody struct {
	io.ReadCloser
	timer   *time.Timer
	cancel  context.CancelFunc
	stalled atomic.Bool
}

func (s *stallBody) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if n > 0 {
		s.timer.Reset(stallTimeout)
	}
	if err != nil && s.stalled.Load() {
		err = fmt.Errorf("download stalled (no data for %s)", stallTimeout)
	}
	return n, err
}

func (s *stallBody) Close() error {
	s.timer.Stop()
	err := s.ReadCloser.Close()
	s.cancel()
	return err
}

// get starts a GET whose body is guarded by stall detection and the overall
// timeout. Closing the body releases the resources.
func get(ctx context.Context, url string, overall time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, overall)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("User-Agent", "ytm-to-mp3")
	resp, err := httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	sb := &stallBody{ReadCloser: resp.Body, cancel: cancel}
	sb.timer = time.AfterFunc(stallTimeout, func() { sb.stalled.Store(true); cancel() })
	resp.Body = sb
	return resp, nil
}

func fetchString(ctx context.Context, url string) (string, error) {
	resp, err := get(ctx, url, fetchTimeout)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// progressWriter logs coarse byte progress: every 20% when the size is known,
// otherwise every 16 MiB, so non-interactive output stays short.
type progressWriter struct {
	logf  Logger
	label string
	total int64
	done  int64
	next  int64 // next threshold in bytes
	step  int64
}

func newProgress(logf Logger, label string, total int64) *progressWriter {
	if logf == nil || label == "" {
		return nil
	}
	p := &progressWriter{logf: logf, label: label, total: total, step: 16 << 20}
	if total > 0 {
		p.step = total / 5
		if p.step < 1 {
			p.step = 1
		}
	}
	p.next = p.step
	return p
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.done >= p.next && (p.total <= 0 || p.done < p.total) {
		if p.total > 0 {
			p.logf("  %s: %d%% (%.0f of %.0f MB)", p.label, p.done*100/p.total, mb(p.done), mb(p.total))
		} else {
			p.logf("  %s: %.0f MB", p.label, mb(p.done))
		}
		for p.next <= p.done {
			p.next += p.step
		}
	}
	return len(b), nil
}

// downloadFile saves url to dst atomically, verifying sha256 when want is set
// (empty want means the upstream publishes no checksum). A non-empty label
// enables coarse progress lines through logf.
func downloadFile(ctx context.Context, url, dst, want string, mode os.FileMode, logf Logger, label string) error {
	resp, err := get(ctx, url, downloadTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	w := []io.Writer{tmp, h}
	if p := newProgress(logf, label, resp.ContentLength); p != nil {
		w = append(w, p)
	}
	if _, err := io.Copy(io.MultiWriter(w...), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("checksum mismatch for %s", url)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	_ = os.Remove(dst) // Windows can't rename over an existing file
	return os.Rename(tmp.Name(), dst)
}
