package engine

import "testing"

func TestBigCover(t *testing.T) {
	cases := map[string]string{
		"https://lh3.googleusercontent.com/abc=w544-h544-p-l90-rj": "https://lh3.googleusercontent.com/abc=w1200-h1200-p-l90-rj",
		"https://i.ytimg.com/vi/x/hq.jpg":                          "https://i.ytimg.com/vi/x/hq.jpg",
		"https://lh3.googleusercontent.com/nosize":                 "https://lh3.googleusercontent.com/nosize",
	}
	for in, want := range cases {
		if got := bigCover(in); got != want {
			t.Errorf("bigCover(%q) = %q, want %q", in, got, want)
		}
	}
}
