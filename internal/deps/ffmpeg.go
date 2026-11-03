package deps

import (
	"archive/tar"
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ulikunitz/xz"
)

func ensureFFmpeg(ctx context.Context, bin string, logf Logger) (string, string, error) {
	// Prefer ours (known features), then PATH; each tool is resolved on its own
	// so a PATH ffmpeg without ffprobe does not discard the ffmpeg we found.
	mf, mp := filepath.Join(bin, exe("ffmpeg")), filepath.Join(bin, exe("ffprobe"))
	f, p := "", ""
	if fileExists(mf) {
		f = mf
	} else if x := lookTool("ffmpeg"); x != "" {
		f = x
	}
	if fileExists(mp) {
		p = mp
	} else if x := lookTool("ffprobe"); x != "" {
		p = x
	}
	if f != "" && p != "" {
		return f, p, nil
	}
	missing := map[string]bool{}
	switch {
	case f == "" && p == "":
		missing[exe("ffmpeg")], missing[exe("ffprobe")] = true, true
	case f == "":
		logf("ffprobe found on PATH (%s) but ffmpeg is missing.", p)
		missing[exe("ffmpeg")] = true
	default:
		logf("ffmpeg found on PATH (%s) but ffprobe is missing.", f)
		missing[exe("ffprobe")] = true
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		// The only prebuilt macOS source (evermeet.cx) is x86_64-only.
		return "", "", fmt.Errorf("ffmpeg and ffprobe are required but not all were found (no native Apple silicon download available).\nInstall them with: brew install ffmpeg")
	}
	if len(missing) == 2 {
		logf("Downloading ffmpeg (one time, ~80 MB)...")
	} else {
		logf("Downloading the ffmpeg build to get the missing tool (one time, ~80 MB)...")
	}
	if err := installFFmpeg(ctx, bin, missing, logf); err != nil {
		return "", "", fmt.Errorf("%w\nInstall ffmpeg yourself (https://ffmpeg.org/download.html) and re-run", err)
	}
	if f == "" {
		f = mf
	}
	if p == "" {
		p = mp
	}
	return f, p, nil
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

// lookTool finds name on PATH, and on macOS also in the usual Homebrew prefixes
// (GUI-launched shells often lack them).
func lookTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			if p := filepath.Join(d, name); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

// ffSource is one downloadable archive. sums is the upstream checksum listing
// URL, or "" when upstream publishes none.
type ffSource struct {
	url, sums, name string
	provides        []string
}

// ffmpegSources are the archives that contain ffmpeg and ffprobe per platform.
func ffmpegSources() ([]ffSource, error) {
	// BtbN publishes checksums.sha256 (all assets) in every release.
	btbnSrc := func(name string) []ffSource {
		const base = "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/"
		return []ffSource{{url: base + name, sums: base + "checksums.sha256", name: name,
			provides: []string{exe("ffmpeg"), exe("ffprobe")}}}
	}
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return btbnSrc("ffmpeg-master-latest-linux64-gpl.tar.xz"), nil
	case "linux/arm64":
		return btbnSrc("ffmpeg-master-latest-linuxarm64-gpl.tar.xz"), nil
	case "windows/amd64":
		return btbnSrc("ffmpeg-master-latest-win64-gpl.zip"), nil
	case "windows/arm64":
		return btbnSrc("ffmpeg-master-latest-winarm64-gpl.zip"), nil
	case "darwin/amd64":
		// evermeet.cx publishes only detached PGP signatures (.sig), no
		// SHA-256 listing, and builds are Intel-only; so no checksum here.
		return []ffSource{
			{url: "https://evermeet.cx/ffmpeg/getrelease/zip", name: "ffmpeg", provides: []string{"ffmpeg"}},
			{url: "https://evermeet.cx/ffmpeg/getrelease/ffprobe/zip", name: "ffprobe", provides: []string{"ffprobe"}},
		}, nil
	case "darwin/arm64":
		return nil, fmt.Errorf("no native Apple silicon ffmpeg download (evermeet.cx is x86_64-only); run: brew install ffmpeg")
	}
	return nil, fmt.Errorf("no prebuilt ffmpeg for %s/%s", runtime.GOOS, runtime.GOARCH)
}

// installFFmpeg downloads archives and extracts only the tools in missing
// (names with exe() applied).
func installFFmpeg(ctx context.Context, bin string, missing map[string]bool, logf Logger) error {
	srcs, err := ffmpegSources()
	if err != nil {
		return err
	}
	for _, s := range srcs {
		need := map[string]bool{}
		for _, n := range s.provides {
			if missing[exe(strings.TrimSuffix(n, ".exe"))] {
				need[exe(strings.TrimSuffix(n, ".exe"))] = true
			}
		}
		if len(need) == 0 {
			continue
		}
		tmp, err := os.CreateTemp(bin, ".ffdl-*")
		if err != nil {
			return err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		if s.sums != "" {
			err = downloadVerified(ctx, s.url, s.sums, s.name, tmp.Name(), 0o644, logf, "ffmpeg")
		} else {
			err = downloadFile(ctx, s.url, tmp.Name(), "", 0o644, logf, s.name)
		}
		if err != nil {
			return err
		}
		if strings.HasSuffix(s.url, ".tar.xz") {
			err = untarXZ(tmp.Name(), bin, need)
		} else {
			err = unzipPick(tmp.Name(), bin, need)
		}
		if err != nil {
			return err
		}
	}
	for n := range missing {
		if !fileExists(filepath.Join(bin, n)) {
			return fmt.Errorf("%s missing from downloaded archive", n)
		}
	}
	return nil
}

func untarXZ(archive, dst string, want map[string]bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	xr, err := xz.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(xr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && want[path.Base(h.Name)] {
			if err := writeExec(filepath.Join(dst, path.Base(h.Name)), tr); err != nil {
				return err
			}
		}
	}
}

func unzipPick(archive, dst string, want map[string]bool) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		base := path.Base(f.Name)
		if f.FileInfo().IsDir() || !(want[base] || want[exe(base)]) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeExec(filepath.Join(dst, exe(strings.TrimSuffix(base, ".exe"))), rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeExec(dst string, r io.Reader) error {
	tmp := dst + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}
