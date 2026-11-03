package deps

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Managed binaries get a "<name>.sha256" file written when they are installed.
// At most once a day (stamp file mtime) Ensure re-hashes them and deletes any
// that no longer match, so the normal install path fetches a clean copy.

const integrityStamp = ".integrity-checked"

var integrityInterval = 24 * time.Hour

func managedNames() []string {
	return []string{exe("yt-dlp"), exe("ffmpeg"), exe("ffprobe"), exe("deno")}
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recordHash stores the current SHA-256 of the binary next to it.
func recordHash(p string) {
	if sum, err := hashFile(p); err == nil {
		_ = os.WriteFile(p+".sha256", []byte(sum+"\n"), 0o644)
	}
}

// verifyManaged re-hashes managed binaries (at most once per integrityInterval)
// and removes any that were modified since install. Binaries with no recorded
// hash (installed by an older version) are recorded on first sight.
func verifyManaged(bin string, logf Logger) {
	stamp := filepath.Join(bin, integrityStamp)
	if st, err := os.Stat(stamp); err == nil && time.Since(st.ModTime()) < integrityInterval {
		return
	}
	for _, n := range managedNames() {
		p := filepath.Join(bin, n)
		if !fileExists(p) {
			continue
		}
		b, err := os.ReadFile(p + ".sha256")
		if err != nil {
			recordHash(p)
			continue
		}
		got, err := hashFile(p)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) != got {
			if logf != nil {
				logf("Managed %s changed since it was installed; re-downloading it.", n)
			}
			_ = os.Remove(p)
			_ = os.Remove(p + ".sha256")
		}
	}
	_ = os.WriteFile(stamp, []byte(time.Now().Format(time.RFC3339)), 0o644)
}
