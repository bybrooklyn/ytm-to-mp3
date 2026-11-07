// Package export walks the user through getting their YouTube Music data from
// Google Takeout, then waits for the zip to appear.
package export

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/ui"
)

const takeoutURL = "https://takeout.google.com/"

// SearchDirs are where a finished export is likely to be.
func SearchDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{}
	// the real download folder first: it may be renamed or translated (~/Téléchargements)
	if d := os.Getenv("XDG_DOWNLOAD_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, xdgUserDirs(home)...)
	dirs = append(dirs, filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop"),
		filepath.Join(home, "Documents"), home, ".")
	seen := map[string]bool{}
	out := dirs[:0]
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil || seen[abs] {
			continue
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// xdgUserDirs reads ~/.config/user-dirs.dirs (Linux) for the download, desktop and
// documents folders.
func xdgUserDirs(home string) []string {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	b, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		for _, k := range []string{"XDG_DOWNLOAD_DIR=", "XDG_DESKTOP_DIR=", "XDG_DOCUMENTS_DIR="} {
			if v, ok := strings.CutPrefix(line, k); ok {
				v = strings.Trim(v, `"`)
				v = strings.ReplaceAll(v, "$HOME", home)
				if filepath.IsAbs(v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func isExport(name string) bool {
	n := strings.ToLower(name)
	if strings.HasSuffix(n, ".crdownload") || strings.HasSuffix(n, ".part") || strings.HasSuffix(n, ".download") || strings.HasSuffix(n, ".tmp") {
		return false
	}
	return strings.HasPrefix(n, "takeout") && strings.HasSuffix(n, ".zip")
}

// Find returns the newest Takeout zip modified after since (zero = any).
func Find(since time.Time) (string, bool) {
	type cand struct {
		path string
		mod  time.Time
	}
	var cs []cand
	for _, d := range SearchDirs() {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !isExport(e.Name()) {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().After(since) {
				abs, _ := filepath.Abs(filepath.Join(d, e.Name()))
				cs = append(cs, cand{abs, info.ModTime()})
			}
		}
	}
	if len(cs) == 0 {
		return "", false
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].mod.After(cs[j].mod) })
	return cs[0].path, true
}

// Guide explains how to request the export, opens the browser, and waits for the zip.
func Guide(ctx context.Context, u *ui.UI) (string, error) {
	u.Head("Get your library from Google Takeout · free, a few minutes; Google may email you when it's ready")
	for i, step := range []string{
		`On the page that opens, click "Deselect all".`,
		`Scroll to "YouTube and YouTube Music" and tick it.`,
		`Click "All YouTube data included", untick everything, then tick "music (library and uploads)" (and "playlists" if you want those). OK.`,
		`Click "Next step", keep "Export once" and ".zip", then "Create export".`,
		`When it's ready, download the zip into your Downloads folder.`,
	} {
		u.Styled(fmt.Sprintf("    %s %s", ui.Cyan(fmt.Sprintf("%d.", i+1)), step))
	}
	u.Hint("mfetchy notices the zip and carries on by itself. (Ctrl-C to stop waiting.)")
	started := time.Now().Add(-2 * time.Minute) // tolerate a clock/mtime skew
	if err := OpenBrowser(takeoutURL); err != nil {
		u.Note("Open this page in your browser: %s", takeoutURL)
	}
	u.Status("Waiting for the Takeout zip in your Downloads folder")
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastPath string
	var lastSize int64 = -1
	stable := 0
	for {
		select {
		case <-ctx.Done():
			u.ClearStatus()
			return "", ctx.Err()
		case <-t.C:
		}
		p, ok := Find(started)
		if !ok {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if p == lastPath && st.Size() == lastSize {
			stable++
		} else {
			lastPath, lastSize, stable = p, st.Size(), 0
		}
		if stable >= 2 { // size unchanged for ~4s: the browser has finished writing it
			u.ClearStatus()
			u.Good("Found %s · %s", p, Describe(p))
			return p, nil
		}
	}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	go cmd.Wait()
	return nil
}

// Describe summarises a found file so the caller can ask for confirmation
// before using it, e.g. "takeout-1.zip (12.3 MB, modified 2h ago)".
func Describe(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return filepath.Base(path)
	}
	return fmt.Sprintf("%s (%.1f MB, modified %s ago)", filepath.Base(path),
		float64(st.Size())/(1<<20), humanAge(time.Since(st.ModTime())))
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
