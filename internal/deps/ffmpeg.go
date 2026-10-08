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
	// Prefer ours (known features), then PATH.
	mf, mp := filepath.Join(bin, exe("ffmpeg")), filepath.Join(bin, exe("ffprobe"))
	if fileExists(mf) && fileExists(mp) {
		return mf, mp, nil
	}
	if f, err := exec.LookPath("ffmpeg"); err == nil {
		if p, err := exec.LookPath("ffprobe"); err == nil {
			return f, p, nil
		}
	}
	logf("Downloading ffmpeg (one time, ~80 MB)...")
	if err := installFFmpeg(ctx, bin); err != nil {
		return "", "", fmt.Errorf("%w\nInstall ffmpeg yourself (https://ffmpeg.org/download.html) and re-run", err)
	}
	return mf, mp, nil
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

// ffmpegSources are the archives that contain ffmpeg and ffprobe per platform.
func ffmpegSources() ([]string, error) {
	const btbn = "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/"
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return []string{btbn + "ffmpeg-master-latest-linux64-gpl.tar.xz"}, nil
	case "linux/arm64":
		return []string{btbn + "ffmpeg-master-latest-linuxarm64-gpl.tar.xz"}, nil
	case "windows/amd64":
		return []string{btbn + "ffmpeg-master-latest-win64-gpl.zip"}, nil
	case "windows/arm64":
		return []string{btbn + "ffmpeg-master-latest-winarm64-gpl.zip"}, nil
	case "darwin/amd64", "darwin/arm64": // x86_64 builds; Rosetta runs them on Apple silicon
		return []string{"https://evermeet.cx/ffmpeg/getrelease/zip", "https://evermeet.cx/ffmpeg/getrelease/ffprobe/zip"}, nil
	}
	return nil, fmt.Errorf("no prebuilt ffmpeg for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func installFFmpeg(ctx context.Context, bin string) error {
	urls, err := ffmpegSources()
	if err != nil {
		return err
	}
	want := map[string]bool{exe("ffmpeg"): true, exe("ffprobe"): true}
	for _, u := range urls {
		tmp, err := os.CreateTemp(bin, ".ffdl-*")
		if err != nil {
			return err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		if err := downloadFile(ctx, u, tmp.Name(), "", 0o644); err != nil {
			return err
		}
		switch {
		case strings.HasSuffix(u, ".tar.xz"):
			err = untarXZ(tmp.Name(), bin, want)
		default:
			err = unzipPick(tmp.Name(), bin, want)
		}
		if err != nil {
			return err
		}
	}
	for n := range want {
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
