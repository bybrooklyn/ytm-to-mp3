package cookies

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// keepDomains are the only cookie domains ytm retains from the browser export.
var keepDomains = []string{"youtube.com", "google.com", "googlevideo.com", "ytimg.com", "gstatic.com"}

func domainKept(d string) bool {
	d = strings.ToLower(strings.TrimPrefix(d, "."))
	for _, k := range keepDomains {
		if d == k || strings.HasSuffix(d, "."+k) {
			return true
		}
	}
	return false
}

// filterJar keeps the Netscape header/comments and only cookie lines for the
// allowed domains (including "#HttpOnly_" lines for them).
func filterJar(in []byte) []byte {
	var out bytes.Buffer
	for _, line := range strings.Split(string(in), "\n") {
		line = strings.TrimRight(line, "\r")
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		body := line
		if strings.HasPrefix(body, "#HttpOnly_") {
			body = strings.TrimPrefix(body, "#HttpOnly_")
		} else if strings.HasPrefix(body, "#") {
			out.WriteString(line + "\n") // header / comment
			continue
		}
		f := strings.Split(body, "\t")
		if len(f) < 7 || !domainKept(f[0]) {
			continue
		}
		out.WriteString(line + "\n")
	}
	return out.Bytes()
}

// writePrivate writes b to dst via a temp file in the same directory, 0600.
func writePrivate(dst string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".cookies-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := os.Chmod(name, 0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Remove(dst) // Windows can't rename over an existing file
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
