package main

import "testing"

func TestSingleVideoID(t *testing.T) {
	for link, want := range map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":                  "dQw4w9WgXcQ",
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ&si=abc":         "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?t=42":                            "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":                   "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":                    "dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLabcdefghi": "", // part of a playlist: expand it
		"https://www.youtube.com/playlist?list=PLabcdefghijklmnop":     "",
		"https://www.youtube.com/watch?v=../../etc":                    "",
		"https://evilyoutube.com/watch?v=dQw4w9WgXcQ":                  "",
		"https://www.youtube.com/@channel":                             "",
	} {
		got, ok := singleVideoID(link)
		if got != want || ok != (want != "") {
			t.Errorf("singleVideoID(%q) = %q, %v; want %q", link, got, ok, want)
		}
	}
}
