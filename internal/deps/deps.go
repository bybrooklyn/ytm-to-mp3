// Package deps finds yt-dlp, ffmpeg/ffprobe and a JavaScript runtime, and
// downloads any that are missing into the user cache directory.
package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/config"
)

// Tools are absolute paths to the external programs mfetchy drives.
type Tools struct {
	YtDlp   string
	FFmpeg  string
	FFprobe string
	// JSRuntimes are values for yt-dlp's --js-runtimes (e.g. "deno:/path/deno").
	JSRuntimes []string
}

// Logger receives progress messages.
type Logger func(format string, a ...any)

// maxPathYtDlpAge is how old a PATH-provided yt-dlp may be before we prefer a fresh managed copy.
const maxPathYtDlpAge = 30 * 24 * time.Hour

// Ensure locates or installs everything mfetchy needs.
func Ensure(ctx context.Context, logf Logger) (Tools, error) {
	cache, err := config.CacheDir()
	if err != nil {
		return Tools{}, err
	}
	bin := filepath.Join(cache, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return Tools{}, err
	}
	var t Tools
	verifyManaged(bin, logf)

	if t.YtDlp, err = ensureYtDlp(ctx, bin, logf); err != nil {
		return t, fmt.Errorf("yt-dlp: %w", err)
	}
	if t.FFmpeg, t.FFprobe, err = ensureFFmpeg(ctx, bin, logf); err != nil {
		return t, fmt.Errorf("ffmpeg: %w", err)
	}
	// A missing JS runtime is not fatal: yt-dlp still works for many videos.
	t.JSRuntimes = ensureJS(ctx, bin, logf)
	return t, nil
}

// Update forces the managed yt-dlp to be re-downloaded.
func Update(ctx context.Context, logf Logger) error {
	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	bin := filepath.Join(cache, "bin")
	_ = os.MkdirAll(bin, 0o755)
	p, err := installYtDlp(ctx, bin, logf)
	if err != nil {
		return err
	}
	logf("yt-dlp %s", ytDlpVersion(p))
	return nil
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ---- yt-dlp ----

var dateRe = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})`)

func ytDlpVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func ytDlpAge(v string) (time.Duration, bool) {
	m := dateRe.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	t, err := time.Parse("2006.01.02", m[0])
	if err != nil {
		return 0, false
	}
	return time.Since(t), true
}

func ensureYtDlp(ctx context.Context, bin string, logf Logger) (string, error) {
	managed := filepath.Join(bin, exe("yt-dlp"))
	// A managed copy refreshes itself weekly; yt-dlp breaks when YouTube changes.
	if st, err := os.Stat(managed); err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
		return managed, nil
	}
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		if age, ok := ytDlpAge(ytDlpVersion(p)); ok && age < maxPathYtDlpAge {
			return p, nil
		}
	}
	p, err := installYtDlp(ctx, bin, logf)
	if err != nil {
		if _, serr := os.Stat(managed); serr == nil { // offline: stale beats nothing
			return managed, nil
		}
		if sys, lerr := exec.LookPath("yt-dlp"); lerr == nil {
			return sys, nil
		}
		return "", err
	}
	return p, nil
}

func ytDlpAsset() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "yt-dlp_linux", nil
	case "linux/arm64":
		return "yt-dlp_linux_aarch64", nil
	case "darwin/amd64", "darwin/arm64":
		return "yt-dlp_macos", nil
	case "windows/amd64":
		return "yt-dlp.exe", nil
	case "windows/arm64":
		return "yt-dlp_arm64.exe", nil
	}
	return "", fmt.Errorf("no prebuilt yt-dlp for %s/%s; install it with your package manager", runtime.GOOS, runtime.GOARCH)
}

func installYtDlp(ctx context.Context, bin string, logf Logger) (string, error) {
	asset, err := ytDlpAsset()
	if err != nil {
		return "", err
	}
	logf("Downloading yt-dlp...")
	base := ytDlpBase
	dst := filepath.Join(bin, exe("yt-dlp"))
	// yt-dlp publishes SHA2-256SUMS next to every release.
	if err := downloadVerified(ctx, base+asset, base+"SHA2-256SUMS", asset, dst, 0o755, logf, "yt-dlp"); err != nil {
		return "", err
	}
	recordHash(dst)
	return dst, nil
}

var ytDlpBase = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"

// downloadVerified downloads url to dst and verifies it against the entry for
// name in the sha256sum-style listing at sumsURL. A missing listing or entry is
// an error: callers use downloadFile directly when upstream has no checksums.
func downloadVerified(ctx context.Context, url, sumsURL, name, dst string, mode os.FileMode, logf Logger, label string) error {
	sums, err := fetchString(ctx, sumsURL)
	if err != nil {
		return fmt.Errorf("fetching checksums: %w", err)
	}
	want := checksumFor(sums, name)
	if want == "" {
		// Single-file *.sha256sum: "<hex>" or "<hex>  <name>".
		if f := strings.Fields(sums); len(f) == 1 && len(f[0]) == 64 {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum for %s in %s", name, sumsURL)
	}
	return downloadFile(ctx, url, dst, want, mode, logf, label)
}

// checksumFor finds `<hex>  <name>` in a sha256sum-style listing.
func checksumFor(listing, name string) string {
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// ---- HTTP helpers ----

// The HTTP download helpers live in download.go.

func removeQuiet(p string) { _ = os.Remove(p) }

// EnsureMedia locates or installs only ffmpeg/ffprobe (enough for tagging).
func EnsureMedia(ctx context.Context, logf Logger) (ffmpeg, ffprobe string, err error) {
	cache, err := config.CacheDir()
	if err != nil {
		return "", "", err
	}
	bin := filepath.Join(cache, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return "", "", err
	}
	verifyManaged(bin, nil)
	return ensureFFmpeg(ctx, bin, logf)
}
