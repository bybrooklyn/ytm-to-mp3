package soundcloud

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"
)

// TestLive resolves one public track against the real SoundCloud and reads the first 64 KiB
// of its audio. Opt-in: YTM_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("YTM_LIVE") != "1" {
		t.Skip("set YTM_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := New(nil)
	link := "https://soundcloud.com/forss/flickermood"
	ts, err := c.Resolve(ctx, link)
	if err != nil || len(ts) != 1 {
		t.Fatal(ts, err)
	}
	tr := ts[0]
	t.Logf("resolved %d track: %q by %q, album %q, %s, %.0fs, isrc %q, downloadable %v, policy %s",
		len(ts), tr.Title, tr.Artist, tr.Album, tr.Date, tr.Duration, tr.ISRC, tr.Downloadable, tr.Policy)

	var obj apiObject
	if err := c.getJSON(ctx, "/resolve", url.Values{"url": {link}}, &obj); err != nil {
		t.Fatal(err)
	}
	ch, err := pick(obj.Media.Transcodings)
	t.Logf("%d transcodings offered; chose %s %s %s (%v)", len(obj.Media.Transcodings), ch.t.Preset, ch.t.Format.Protocol, ch.t.Format.MimeType, err)

	fctx, stop := context.WithCancel(ctx)
	defer stop()
	var got int64
	_, err = c.Fetch(fctx, tr, t.TempDir()+"/live", func(g, _ int64) {
		got = g
		if g >= 64<<10 {
			stop()
		}
	})
	if got < 64<<10 || (err != nil && !errors.Is(err, context.Canceled)) {
		t.Fatalf("read %d bytes, err %v", got, err)
	}
	t.Logf("read %d bytes of audio, then stopped", got)
}
