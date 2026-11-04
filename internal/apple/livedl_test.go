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
	if err := SetProxy(os.Getenv("YTM_APPLE_PROXY")); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	ctx := context.Background()

	// Pick the first browser whose Apple Music session Apple still accepts.
	var c *Client
	for _, b := range browsercookies.Detect() {
		cs, err := b.AppleMusicCookies()
		if err != nil {
			continue
		}
		tok := browsercookies.AppleUserToken(cs)
		if tok == "" {
			continue
		}
		cand, err := New(tok, browsercookies.CookieHeader(cs), "us", "")
		if err != nil {
			continue
		}
		if _, err := cand.Verify(ctx); err != nil {
			t.Logf("%s: login expired (%v)", b.Name, err)
			continue
		}
		t.Logf("using live Apple login from %s", b.Name)
		c = cand
		break
	}
	if c == nil {
		t.Skip("no live Apple Music session in any browser; sign in at music.apple.com first")
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
