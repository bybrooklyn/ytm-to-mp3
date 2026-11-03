package deps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChecksumFor(t *testing.T) {
	l := "AAA111  yt-dlp\nbbb222 *yt-dlp.exe\nccc333  yt-dlp_linux\n"
	for name, want := range map[string]string{"yt-dlp": "aaa111", "yt-dlp.exe": "bbb222", "yt-dlp_linux": "ccc333", "nope": ""} {
		if got := checksumFor(l, name); got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}

func TestYtDlpAge(t *testing.T) {
	if _, ok := ytDlpAge("2026.08.19"); !ok {
		t.Error("should parse")
	}
	if _, ok := ytDlpAge("garbage"); ok {
		t.Error("should not parse")
	}
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestDownloadVerified(t *testing.T) {
	body := []byte("hello binary")
	mux := http.NewServeMux()
	mux.HandleFunc("/f", func(w http.ResponseWriter, _ *http.Request) { w.Write(body) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  other\n%s  f\n", strings.Repeat("0", 64), sum(body))
	})
	mux.HandleFunc("/single", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "%s  f.zip\n", sum(body)) })
	mux.HandleFunc("/bad", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "%s  f\n", strings.Repeat("a", 64)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	logf := func(string, ...any) {}
	dst := filepath.Join(t.TempDir(), "out")
	ctx := context.Background()

	if err := downloadVerified(ctx, srv.URL+"/f", srv.URL+"/sums", "f", dst, 0o644, logf, "x"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != string(body) {
		t.Fatalf("content %q", got)
	}
	if err := downloadVerified(ctx, srv.URL+"/f", srv.URL+"/single", "f", dst, 0o644, logf, ""); err == nil {
		// two fields with a different name must not be accepted
		t.Error("mismatched single-entry listing accepted")
	}
	dst2 := filepath.Join(t.TempDir(), "out")
	if err := downloadVerified(ctx, srv.URL+"/f", srv.URL+"/bad", "f", dst2, 0o644, logf, ""); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("want mismatch, got %v", err)
	}
	if fileExists(dst2) {
		t.Error("file kept after mismatch")
	}
	if err := downloadVerified(ctx, srv.URL+"/f", srv.URL+"/sums", "absent", dst2, 0o644, logf, ""); err == nil {
		t.Error("missing checksum entry should fail")
	}
}

func TestProgressIsCoarse(t *testing.T) {
	var lines int
	p := newProgress(func(string, ...any) { lines++ }, "x", 1000)
	for i := 0; i < 1000; i++ {
		p.Write([]byte{0})
	}
	if lines == 0 || lines > 5 {
		t.Errorf("progress lines = %d", lines)
	}
}

func TestStallAborts(t *testing.T) {
	old := stallTimeout
	stallTimeout = 100 * time.Millisecond
	defer func() { stallTimeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	err := downloadFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "o"), "", 0o644, nil, "")
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Errorf("want stall error, got %v", err)
	}
}

func TestEnsureJSNotFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	old := denoDownload
	denoDownload = func(context.Context, string, Logger) error { return fmt.Errorf("no deno build") }
	defer func() { denoDownload = old }()
	var msgs []string
	got := ensureJS(context.Background(), t.TempDir(), func(f string, a ...any) { msgs = append(msgs, fmt.Sprintf(f, a...)) })
	if len(got) != 0 {
		t.Errorf("want empty runtimes, got %v", got)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Warning") {
		t.Errorf("want one warning, got %v", msgs)
	}
}

func TestEnsureJSPrefersDenoOverNode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, exe("node")), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	old := denoDownload
	bin := t.TempDir()
	denoDownload = func(_ context.Context, b string, _ Logger) error {
		return os.WriteFile(filepath.Join(b, exe("deno")), []byte("x"), 0o755)
	}
	defer func() { denoDownload = old }()
	got := ensureJS(context.Background(), bin, func(string, ...any) {})
	if len(got) != 1 || !strings.HasPrefix(got[0], "deno:") {
		t.Errorf("got %v", got)
	}
}

func TestRedirectToHTTPRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tls.Close()
	old := httpClient
	c := tls.Client()
	c.CheckRedirect = old.CheckRedirect
	httpClient = c
	defer func() { httpClient = old }()
	err := downloadFile(context.Background(), tls.URL, filepath.Join(t.TempDir(), "o"), "", 0o644, nil, "")
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Errorf("got %v", err)
	}
}

func TestDownloadSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Write(make([]byte, 5000))
		w.(http.Flusher).Flush()
		w.Write(make([]byte, 5000))
	}))
	defer srv.Close()
	old := maxDownloadBytes
	maxDownloadBytes = 1000
	defer func() { maxDownloadBytes = old }()
	if err := downloadFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "o"), "", 0o644, nil, ""); err == nil {
		t.Error("want size error")
	}
	if err := writeExec(filepath.Join(t.TempDir(), "e"), strings.NewReader(strings.Repeat("a", 2000))); err == nil {
		t.Error("want extract size error")
	}
}

func TestVerifyManagedRemovesTampered(t *testing.T) {
	bin := t.TempDir()
	p := filepath.Join(bin, exe("yt-dlp"))
	os.WriteFile(p, []byte("good"), 0o755)
	recordHash(p)
	os.WriteFile(p, []byte("evil"), 0o755)
	verifyManaged(bin, func(string, ...any) {})
	if fileExists(p) {
		t.Error("tampered binary should be removed")
	}
}

func TestEnsureJSFallsBackToNode(t *testing.T) {
	dir := t.TempDir()
	name := exe("node")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	old := denoDownload
	denoDownload = func(context.Context, string, Logger) error { return fmt.Errorf("offline") }
	defer func() { denoDownload = old }()
	got := ensureJS(context.Background(), t.TempDir(), func(string, ...any) {})
	if len(got) != 1 || !strings.HasPrefix(got[0], "node:") {
		t.Errorf("got %v", got)
	}
}
