package deps

import "testing"

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
