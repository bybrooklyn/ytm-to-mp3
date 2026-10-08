// Package deps finds yt-dlp, ffmpeg/ffprobe and a JavaScript runtime, and
// downloads any that are missing into the user cache directory.
package deps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
)

// Tools are absolute paths to the external programs ytm drives.
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
const maxPathYtDlpAge = 90 * 24 * time.Hour

// Ensure locates or installs everything ytm needs.
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

	if t.YtDlp, err = ensureYtDlp(ctx, bin, logf); err != nil {
		return t, fmt.Errorf("yt-dlp: %w", err)
	}
	if t.FFmpeg, t.FFprobe, err = ensureFFmpeg(ctx, bin, logf); err != nil {
		return t, fmt.Errorf("ffmpeg: %w", err)
	}
	if t.JSRuntimes, err = ensureJS(ctx, bin, logf); err != nil {
		return t, fmt.Errorf("javascript runtime: %w", err)
	}
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
	out, err := exec.Command(path, "--version").Output()
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
	base := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
	sums, err := fetchString(ctx, base+"SHA2-256SUMS")
	if err != nil {
		return "", err
	}
	want := checksumFor(sums, asset)
	dst := filepath.Join(bin, exe("yt-dlp"))
	if err := downloadFile(ctx, base+asset, dst, want, 0o755); err != nil {
		return "", err
	}
	return dst, nil
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

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ytm-to-mp3")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func fetchString(ctx context.Context, url string) (string, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// downloadFile saves url to dst atomically, verifying sha256 when want is set.
func downloadFile(ctx context.Context, url, dst, want string, mode os.FileMode) error {
	resp, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("checksum mismatch for %s", url)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	_ = os.Remove(dst) // Windows can't rename over an existing file
	return os.Rename(tmp.Name(), dst)
}

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
	return ensureFFmpeg(ctx, bin, logf)
}
