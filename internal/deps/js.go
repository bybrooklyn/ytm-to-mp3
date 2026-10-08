package deps

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ensureJS returns --js-runtimes values. yt-dlp needs a JavaScript runtime to
// solve YouTube's challenges; use whatever is installed, else fetch deno.
func ensureJS(ctx context.Context, bin string, logf Logger) ([]string, error) {
	managed := filepath.Join(bin, exe("deno"))
	if fileExists(managed) {
		return []string{"deno:" + managed}, nil
	}
	var found []string
	for _, name := range []string{"deno", "bun", "node"} {
		if p, err := exec.LookPath(name); err == nil {
			found = append(found, name+":"+p)
		}
	}
	if len(found) > 0 {
		return found, nil
	}
	target, err := denoTarget()
	if err != nil {
		return nil, err
	}
	logf("Downloading deno (JavaScript runtime for YouTube, one time)...")
	url := "https://github.com/denoland/deno/releases/latest/download/deno-" + target + ".zip"
	tmp := filepath.Join(bin, ".deno.zip")
	if err := downloadFile(ctx, url, tmp, "", 0o644); err != nil {
		return nil, err
	}
	defer removeQuiet(tmp)
	if err := unzipPick(tmp, bin, map[string]bool{exe("deno"): true}); err != nil {
		return nil, err
	}
	if !fileExists(managed) {
		return nil, fmt.Errorf("deno missing from downloaded archive")
	}
	return []string{"deno:" + managed}, nil
}

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
	return "", fmt.Errorf("no prebuilt deno for %s/%s; install node or deno", runtime.GOOS, runtime.GOARCH)
}
