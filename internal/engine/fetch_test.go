package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func resolvedFor(url string, size int) *Resolved {
	return &Resolved{URL: url, Protocol: "https", Size: int64(size)}
}

// A plain 200 whose length is exactly the range asked for is what googlevideo sends for
// range= queries, and must be accepted (rejecting it forced every track onto slow yt-dlp).
func TestFetchAccepts200WithExactRange(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body) // 200, never 206
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "x.src")
	if err := fetch(context.Background(), srv.Client(), resolvedFor(srv.URL, len(body)), dst, func(int64) {}, func() {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, body) {
		t.Fatal("content mismatch")
	}
}

// A server that ignores Range and streams the whole file must not be written at an offset.
func TestFetchRejectsServerIgnoringRange(t *testing.T) {
	body := bytes.Repeat([]byte("b"), 12<<20) // more than one chunk
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "x.src")
	err := fetch(context.Background(), srv.Client(), resolvedFor(srv.URL, len(body)), dst, func(int64) {}, func() {})
	if !errors.Is(err, errUnfetchable) {
		t.Fatalf("want errUnfetchable, got %v", err)
	}
}

// An exit that accepts the request but never sends data must be abandoned after stallAfter,
// not after the 2-minute chunk timeout.
func TestFetchStallIsDetectedFast(t *testing.T) {
	old := stallAfter
	stallAfter = 300 * time.Millisecond
	defer func() { stallAfter = old }()
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	dst := filepath.Join(t.TempDir(), "x.src")
	start := time.Now()
	err := fetch(context.Background(), srv.Client(), resolvedFor(srv.URL, 2<<20), dst, func(int64) {}, func() {})
	if !errors.Is(err, errStalled) {
		t.Fatalf("want errStalled, got %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v to notice the stall", d)
	}
}
