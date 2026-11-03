package deps

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

// jsRuntimeNames maps yt-dlp runtime names to executables to look for on PATH.
var jsRuntimeNames = []struct{ ytdlp, exe string }{
	{"deno", "deno"},
	{"node", "node"},
	{"bun", "bun"},
	{"quickjs", "qjs"},
	{"quickjs", "quickjs"},
}

// denoDownload fetches deno into bin; a variable so tests can stub it.
var denoDownload = downloadDeno

// ensureJS returns --js-runtimes values. yt-dlp uses a JavaScript runtime to
// solve YouTube's challenges; use whatever is installed, else try to fetch
// deno. Having none is NOT fatal (e.g. windows/arm64 has no deno build):
// yt-dlp still handles many videos, so we warn once and continue.
func ensureJS(ctx context.Context, bin string, logf Logger) []string {
	managed := filepath.Join(bin, exe("deno"))
	if fileExists(managed) {
		return []string{"deno:" + managed}
	}
	var found []string
	seen := map[string]bool{}
	for _, r := range jsRuntimeNames {
		if seen[r.ytdlp] {
			continue
		}
		if p, err := exec.LookPath(r.exe); err == nil {
			found = append(found, r.ytdlp+":"+p)
			seen[r.ytdlp] = true
		}
	}
	if len(found) > 0 {
		return found
	}
	if err := denoDownload(ctx, bin, logf); err != nil {
		logf("Warning: no JavaScript runtime (%v); install deno or node for best YouTube results. Continuing without one.", err)
		return nil
	}
	return []string{"deno:" + managed}
}

func downloadDeno(ctx context.Context, bin string, logf Logger) error {
	target, err := denoTarget()
	if err != nil {
		return err
	}
	logf("Downloading deno (JavaScript runtime for YouTube, one time)...")
	name := "deno-" + target + ".zip"
	base := denoBase
	tmp := filepath.Join(bin, ".deno.zip")
	defer removeQuiet(tmp)
	// deno publishes <asset>.sha256sum next to each release asset.
	if err := downloadVerified(ctx, base+name, base+name+".sha256sum", name, tmp, 0o644, logf, "deno"); err != nil {
		return err
	}
	if err := unzipPick(tmp, bin, map[string]bool{exe("deno"): true}); err != nil {
		return err
	}
	if !fileExists(filepath.Join(bin, exe("deno"))) {
		return fmt.Errorf("deno missing from downloaded archive")
	}
	return nil
}

var denoBase = "https://github.com/denoland/deno/releases/latest/download/"

func denoTarget() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "x86_64-unknown-linux-gnu", nil
	case "linux/arm64":
		return "aarch64-unknown-linux-gnu", nil
	case "darwin/amd64":
		return "x86_64-apple-darwin", nil
	case "darwin/arm64":
		return "aarch64-apple-darwin", nil
	case "windows/amd64":
		return "x86_64-pc-windows-msvc", nil
	}
	return "", fmt.Errorf("no prebuilt deno for %s/%s", runtime.GOOS, runtime.GOARCH)
}
