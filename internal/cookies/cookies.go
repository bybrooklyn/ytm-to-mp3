// Package cookies obtains the user's YouTube login cookies from an installed
// browser (after asking), so downloads are authenticated and less throttled.
package cookies

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/deps"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// Browser is an installed browser whose cookies yt-dlp can read.
type Browser struct {
	Name string // yt-dlp name
	Dir  string // profile data directory used for detection
}

// browserDirs lists, per OS, the data directory that proves a browser is installed.
func browserDirs() []Browser {
	home, _ := os.UserHomeDir()
	j := filepath.Join
	switch runtime.GOOS {
	case "windows":
		local, roaming := os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")
		return []Browser{
			{"firefox", j(roaming, "Mozilla", "Firefox", "Profiles")},
			{"edge", j(local, "Microsoft", "Edge", "User Data")},
			{"chrome", j(local, "Google", "Chrome", "User Data")},
			{"brave", j(local, "BraveSoftware", "Brave-Browser", "User Data")},
			{"vivaldi", j(local, "Vivaldi", "User Data")},
			{"opera", j(roaming, "Opera Software", "Opera Stable")},
			{"chromium", j(local, "Chromium", "User Data")},
		}
	case "darwin":
		s := j(home, "Library", "Application Support")
		return []Browser{
			{"safari", j(home, "Library", "Cookies", "Cookies.binarycookies")},
			{"chrome", j(s, "Google", "Chrome")},
			{"firefox", j(s, "Firefox", "Profiles")},
			{"edge", j(s, "Microsoft Edge")},
			{"brave", j(s, "BraveSoftware", "Brave-Browser")},
			{"vivaldi", j(s, "Vivaldi")},
			{"opera", j(s, "com.operasoftware.Opera")},
			{"chromium", j(s, "Chromium")},
		}
	default:
		c := j(home, ".config")
		return []Browser{
			{"firefox", j(home, ".mozilla", "firefox")},
			{"firefox", j(home, "snap", "firefox", "common", ".mozilla", "firefox")},
			{"firefox", j(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox")},
			{"chrome", j(c, "google-chrome")},
			{"chromium", j(c, "chromium")},
			{"brave", j(c, "BraveSoftware", "Brave-Browser")},
			{"edge", j(c, "microsoft-edge")},
			{"vivaldi", j(c, "vivaldi")},
			{"opera", j(c, "opera")},
		}
	}
}

// Detect returns the installed browsers, de-duplicated, in preference order.
func Detect() []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range browserDirs() {
		if _, err := os.Stat(b.Dir); err == nil && !seen[b.Name] {
			seen[b.Name] = true
			out = append(out, b.Name)
		}
	}
	return out
}

// Result of Setup.
type Result struct {
	File    string // cookies.txt path, "" when running anonymously
	Browser string
}

const maxAge = 12 * time.Hour

// Setup asks (once) whether to use browser cookies, then exports them. It
// returns an empty Result, not an error, when the user declines or export fails.
func Setup(ctx context.Context, u *ui.UI, cfg *config.Config, tools deps.Tools, disabled bool) Result {
	if disabled || cfg.Cookies == "no" {
		return Result{}
	}
	cache, err := config.CacheDir()
	if err != nil {
		return Result{}
	}
	file := filepath.Join(cache, "cookies.txt")
	if cfg.Cookies == "yes" && fresh(file) && LoggedIn(file) {
		return Result{File: file, Browser: cfg.Browser}
	}

	browsers := Detect()
	if len(browsers) == 0 {
		return Result{}
	}
	if cfg.Cookies == "" {
		if !u.Interactive() {
			return Result{}
		}
		u.Printf("Downloads are faster and hit fewer limits when logged in to YouTube.")
		u.Printf("ytm can read your YouTube login cookies from your browser. They stay on this")
		u.Printf("computer and are only sent to YouTube. (Detected: %s)", strings.Join(browsers, ", "))
		if !u.Confirm("Use your browser login?", true) {
			cfg.Cookies = "no"
			cfg.Save()
			return Result{}
		}
		if len(browsers) > 1 {
			browsers = []string{u.Ask(fmt.Sprintf("Which browser has your YouTube login? [%s] ", browsers[0]), browsers[0])}
		}
	} else if cfg.Browser != "" {
		browsers = append([]string{cfg.Browser}, browsers...)
	}

	var lastErr error
	tried := map[string]bool{}
	for _, b := range browsers {
		if tried[b] {
			continue
		}
		tried[b] = true
		if err := export(ctx, tools, b, file); err != nil {
			lastErr = err
			continue
		}
		if !LoggedIn(file) {
			lastErr = fmt.Errorf("not logged in to YouTube in %s", b)
			continue
		}
		cfg.Cookies, cfg.Browser = "yes", b
		cfg.Save()
		u.Printf("Using your %s login.", b)
		return Result{File: file, Browser: b}
	}
	if lastErr != nil {
		u.Printf("Couldn't read cookies (%v). Continuing without a login.", lastErr)
		if runtime.GOOS == "windows" {
			u.Printf("Tip: newer Chrome/Edge lock their cookies; Firefox works reliably.")
		}
	}
	return Result{}
}

func fresh(p string) bool {
	st, err := os.Stat(p)
	return err == nil && time.Since(st.ModTime()) < maxAge
}

func export(ctx context.Context, tools deps.Tools, browser, file string) error {
	_ = os.Remove(file)
	args := []string{"--ignore-config", "--cookies-from-browser", browser, "--cookies", file,
		"--skip-download", "--simulate", "--no-warnings", "--no-playlist", "--quiet"}
	for _, r := range tools.JSRuntimes {
		args = append(args, "--js-runtimes", r)
	}
	args = append(args, "https://www.youtube.com/watch?v=jNQXAC9IVRw")
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, tools.YtDlp, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // yt-dlp writes the jar even if the probe video fails
	if st, err := os.Stat(file); err != nil || st.Size() < 100 {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "ERROR:"); i >= 0 {
			msg = msg[i:]
		}
		if msg == "" {
			msg = "no cookies exported"
		}
		return fmt.Errorf("%s: %s", browser, firstLine(msg))
	}
	return os.Chmod(file, 0o600)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// LoggedIn reports whether a Netscape cookie file holds a Google login.
func LoggedIn(file string) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("SAPISID")) || bytes.Contains(b, []byte("__Secure-3PSID"))
}

// Copy makes a private copy so concurrent yt-dlp processes don't race rewriting one jar.
func Copy(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}
