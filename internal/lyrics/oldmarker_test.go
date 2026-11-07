package lyrics

import "testing"

// Files the previous version wrote carry "[by:ytm · ...]"; they are still ours.
func TestWrittenByUsKnowsTheOldName(t *testing.T) {
	for by, want := range map[string]bool{
		"mfetchy · LRCLIB": true,
		"ytm · LRCLIB":     true,
		"ytm · NetEase":    true,
		"someone else":     false,
		"":                 false,
	} {
		if got := WrittenByUs(by); got != want {
			t.Errorf("WrittenByUs(%q) = %v, want %v", by, got, want)
		}
	}
	_, h := ParseLRC("[ti:x]\n[by:ytm · LRCLIB]\n[00:01.00]hi\n")
	if !WrittenByUs(h.By) {
		t.Errorf("old header %q not recognised", h.By)
	}
}
