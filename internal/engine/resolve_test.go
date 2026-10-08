package engine

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		kind kind
		msg  string
	}{
		{"ERROR: [youtube] abc: Video unavailable. This video has been removed by the uploader", kindPermanent, "Video unavailable. This video has been removed by the uploader"},
		{"ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies", kindThrottled, "Sign in to confirm you’re not a bot. Use --cookies"},
		{"ERROR: unable to download video data: HTTP Error 429: Too Many Requests", kindThrottled, "unable to download video data: HTTP Error 429: Too Many Requests"},
		{"ERROR: [youtube] abc: Private video. Sign in if you've been granted access", kindPermanent, "Private video. Sign in if you've been granted access"},
		{"ERROR: [youtube] abc: Connection reset by peer", kindTransient, "Connection reset by peer"},
		{"", kindTransient, "yt-dlp failed"},
	}
	for _, c := range cases {
		e := classify(c.in)
		if e.Kind != c.kind || e.Msg != c.msg {
			t.Errorf("classify(%q) = %v %q; want %v %q", c.in, e.Kind, e.Msg, c.kind, c.msg)
		}
	}
}
