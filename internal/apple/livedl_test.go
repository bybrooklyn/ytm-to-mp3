package apple

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bybrooklyn/ytm-to-mp3/internal/browsercookies"
)

// TestLiveDownload performs one real Apple Music download using the
// media-user-token found in a browser. Skipped unless YTM_APPLE_DL=1.
func TestLiveDownload(t *testing.T) {
	if os.Getenv("YTM_APPLE_DL") != "1" {
		t.Skip("set YTM_APPLE_DL=1 to run the live Apple download test")
	}
	cookies, browser, err := browsercookies.FindAppleMusicCookies()
	if err != nil {
		t.Fatalf("FindAppleMusicCookies: %v", err)
	}
	token := browsercookies.AppleUserToken(cookies)
	jar := browsercookies.CookieHeader(cookies)
	t.Logf("using Apple login from %s (%d cookies)", browser, len(cookies))

	if err := SetProxy(os.Getenv("YTM_APPLE_PROXY")); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	ctx := context.Background()
	c, err := New(token, jar, "us", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tracks, err := c.ResolveURL(ctx, "https://music.apple.com/us/album/currents/1440838039")
	if err != nil {
		t.Fatalf("ResolveURL: %v", err)
	}
	tr := tracks[0]
	t.Logf("downloading %q by %q (id %s)", tr.Title, tr.Artist, tr.ID)

	dst := filepath.Join(t.TempDir(), "track.m4a")
	if err := c.Download(ctx, tr, dst); err != nil {
		t.Fatalf("Download: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output missing or empty: %v size=%d", err, infoSize(info))
	}
	// A decrypted MP4 begins with an ftyp box: bytes 4..8 are "ftyp".
	head := make([]byte, 12)
	f, _ := os.Open(dst)
	defer f.Close()
	_, _ = f.Read(head)
	if !bytes.Equal(head[4:8], []byte("ftyp")) {
		t.Fatalf("output is not an MP4 (head %q)", head)
	}
	t.Logf("OK: %d bytes, valid MP4 (ftyp) at %s", info.Size(), dst)
}

func infoSize(i os.FileInfo) int64 {
	if i == nil {
		return -1
	}
	return i.Size()
}
