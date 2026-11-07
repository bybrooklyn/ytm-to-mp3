package core

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/export"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// Version is mfetchy's version, set by the program at start (shown by the self-check).
var Version = "dev"

// The terminal palette, under the short names the pipeline code uses.
var (
	cDim    = ui.CDim
	cBold   = ui.CBold
	cAccent = ui.CAccent
	cGreen  = ui.CGreen
	cYellow = ui.CYellow
	cReset  = ui.CReset
)

// Tilde shortens a path under the home folder to ~/..., with native separators.
func Tilde(p string) string { return tilde(p) }

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return "~" + string(filepath.Separator) + rel // native separators, so Windows isn't ~/AppData\Roaming
		}
	}
	return p
}

// OrDefault is s, or d when s is empty.
func OrDefault(s, d string) string { return orDefault(s, d) }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

// FindExisting looks for a YouTube Music export lying around (downloads, desktop, ...).
func FindExisting() (string, bool) { return findExisting() }

func findExisting() (string, bool) {
	if p, ok := export.Find(time.Time{}); ok {
		return p, true
	}
	for _, d := range export.SearchDirs() {
		for _, n := range []string{"music library songs.csv", "Takeout"} {
			p := filepath.Join(d, n)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}
	return "", false
}

// Count is "1 song" / "3 songs".
func Count(n int, noun string) string { return count(n, noun) }

// FileExists reports whether p exists.
func FileExists(p string) bool { return fileExists(p) }

// warnBlock prints a multi-line warning: its first line marked "!", the rest dim under it.
func warnBlock(u *ui.UI, text string) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	u.Warn("%s", lines[0])
	for _, l := range lines[1:] {
		u.Styled("    " + ui.Dim(ui.Sanitize(l)))
	}
}
