package names

import "testing"

func TestPart(t *testing.T) {
	cases := map[string]string{
		"AC/DC":           "AC_DC",
		"What?":           "What_",
		"  spaced   out ": "spaced out",
		"trailing dot.":   "trailing dot",
		"CON":             "_CON",
		"nul.txt":         "_nul.txt",
		"":                "fallback",
		"...":             "fallback",
	}
	for in, want := range cases {
		if got := Part(in, "fallback"); got != want {
			t.Errorf("Part(%q) = %q, want %q", in, got, want)
		}
	}
}
