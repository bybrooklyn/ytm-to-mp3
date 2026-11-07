package httpfetch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// rangeServer serves data with Range support; cutAfter > 0 makes the first request die
// after that many bytes (a dropped connection).
func rangeServer(data []byte, etag string, cutAfter int, reqs *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqs.Add(1)
		w.Header().Set("ETag", etag)
		if cutAfter > 0 && n == 1 {
			w.Header().Set("Content-Length", "10000000") // promise more than is sent
			w.WriteHeader(200)
			w.Write(data[:cutAfter])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
}

func TestResumeAfterDroppedConnection(t *testing.T) {
	data := payload(300 << 10)
	var reqs atomic.Int32
	srv := rangeServer(data, `"v1"`, 100<<10, &reqs)
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "a.bin")
	if err := Download(context.Background(), srv.URL, dst, Options{Backoff: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, data) {
		t.Fatalf("file differs (%d vs %d bytes)", len(got), len(data))
	}
	if reqs.Load() != 2 {
		t.Errorf("requests = %d, want 2 (one cut, one resumed)", reqs.Load())
	}
	for _, p := range []string{dst + ".part", dst + ".part.meta"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
}

func TestCancelKeepsPartAndNextRunResumes(t *testing.T) {
	data := payload(300 << 10)
	var reqs atomic.Int32
	srv := rangeServer(data, `"v1"`, 0, &reqs)
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "a.bin")
	ctx, cancel := context.WithCancel(context.Background())
	err := Download(ctx, srv.URL, dst, Options{Progress: func(done, total int64) {
		if done >= 100<<10 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want cancelled", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a truncated file was left at the destination")
	}
	st, err := os.Stat(dst + ".part")
	if err != nil || st.Size() == 0 {
		t.Fatalf("no partial kept: %v", err)
	}
	if err := Download(context.Background(), srv.URL, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("resumed file differs")
	}
}

func TestChangedValidatorsStartOver(t *testing.T) {
	old, fresh := payload(200<<10), bytes.Repeat([]byte{9}, 250<<10)
	dst := filepath.Join(t.TempDir(), "a.bin")
	// A stale partial from "v1".
	os.WriteFile(dst+".part", old[:50<<10], 0o644)
	os.WriteFile(dst+".part.meta", []byte(`{"etag":"\"v1\"","total":204800}`), 0o644)
	var reqs atomic.Int32
	srv := rangeServer(fresh, `"v2"`, 0, &reqs) // the file is now v2
	defer srv.Close()
	if err := Download(context.Background(), srv.URL, dst, Options{Backoff: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, fresh) {
		t.Fatal("stale bytes were spliced into the new file")
	}
}

func TestCheckRejectsAndDiscards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>login</html>")) }))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "a.bin")
	err := Download(context.Background(), srv.URL, dst, Options{Check: func(b []byte) error {
		if !bytes.HasPrefix(b, []byte("fLaC")) {
			return errors.New("not a FLAC file")
		}
		return nil
	}})
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, p := range []string{dst, dst + ".part", dst + ".part.meta"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists", p)
		}
	}
}

func TestPermanentStatusNotRetried(t *testing.T) {
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reqs.Add(1); w.WriteHeader(404) }))
	defer srv.Close()
	err := Download(context.Background(), srv.URL, filepath.Join(t.TempDir(), "a"), Options{Backoff: time.Millisecond})
	if err == nil || reqs.Load() != 1 {
		t.Fatalf("err=%v reqs=%d", err, reqs.Load())
	}
}

func TestServerErrorRetriedThenOK(t *testing.T) {
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqs.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("hello"))
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "a")
	if err := Download(context.Background(), srv.URL, dst, Options{Backoff: time.Millisecond}); err != nil || reqs.Load() != 3 {
		t.Fatalf("err=%v reqs=%d", err, reqs.Load())
	}
}
