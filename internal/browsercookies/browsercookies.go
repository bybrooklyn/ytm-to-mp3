// Package browsercookies reads the user's YouTube cookies out of an installed browser
// (Firefox family: plain SQLite; Chromium family: encrypted values, with the key from the
// OS secret store) and writes them as a Netscape cookies.txt for yt-dlp. It is only used
// for the optional, experimental YouTube Premium upgrade, after the user asked for it.
//
// Ported from ytm.qt (src/auth). Everything except the three OS key lookups
// (keys_*.go) is plain Go covered by tests.
package browsercookies

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Cookie is one cookie in Netscape cookies.txt terms.
type Cookie struct {
	Domain  string // ".youtube.com" (a leading dot covers subdomains)
	Path    string
	Secure  bool
	Expires int64 // unix seconds; 0 = session cookie
	Name    string
	Value   string
}

// Browser is an installed browser with at least one cookie database.
type Browser struct {
	ID, Name string
	Note     string // shown to the user when something limits importing ("" if nothing)
	chromium *chromiumDef
	dbs      []db // most recently used first
}

type db struct {
	path string
	root string // user-data folder (Chromium keeps its Windows key in "Local State" there)
}

// youTubeHosts are the cookie hosts read. Only YouTube's: never Google account cookies.
var youTubeHosts = []string{".youtube.com", "youtube.com", ".music.youtube.com", "music.youtube.com"}

// loginCookies: one of these means the browser is signed in to YouTube.
var loginCookies = map[string]bool{"SAPISID": true, "__Secure-1PAPISID": true, "__Secure-3PAPISID": true}

// ErrNotSignedIn means the browser has YouTube cookies but no signed-in session.
var ErrNotSignedIn = errors.New("not signed in to YouTube in that browser")

// dirs are the user folders browser data lives under.
type dirs struct{ home, local, roaming string }

func currentDirs() (dirs, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dirs{}, false
	}
	return dirs{home: home, local: os.Getenv("LOCALAPPDATA"), roaming: os.Getenv("APPDATA")}, true
}

// Detect lists the supported browsers that have a cookie database, most recently used first.
func Detect() []Browser {
	d, ok := currentDirs()
	if !ok {
		return nil
	}
	return detect(runtime.GOOS, d)
}

func detect(goos string, d dirs) []Browser {
	var found []Browser
	for i := range chromiumDefs {
		def := &chromiumDefs[i]
		var dbs []db
		for _, root := range def.roots(goos, d) {
			for _, p := range chromiumDBs(root) {
				dbs = append(dbs, db{path: p, root: root})
			}
		}
		if len(dbs) > 0 {
			b := Browser{ID: def.id, Name: def.name, chromium: def, dbs: dbs}
			if goos == "windows" {
				b.Note = "close it first; Chrome, Edge and Brave 127+ lock their cookies away from other apps"
			}
			found = append(found, b)
		}
	}
	for _, def := range firefoxDefs {
		var dbs []db
		for _, root := range def.roots(goos, d) {
			for _, p := range firefoxDBs(root) {
				dbs = append(dbs, db{path: p, root: root})
			}
		}
		if len(dbs) > 0 {
			found = append(found, Browser{ID: def.id, Name: def.name, dbs: dbs})
		}
	}
	for i := range found {
		sortByMtime(found[i].dbs)
	}
	sort.SliceStable(found, func(i, j int) bool { return mtime(found[i].dbs[0].path).After(mtime(found[j].dbs[0].path)) })
	return found
}

// Import reads the signed-in YouTube cookies from b, trying its profiles newest first.
func (b Browser) Import() ([]Cookie, error) {
	var last error = errors.New("no cookie database")
	for _, d := range b.dbs {
		var cs []Cookie
		var err error
		if b.chromium != nil {
			cs, err = readChromium(d, newKeySource(b.chromium, d.root))
		} else {
			cs, err = readFirefox(d.path)
		}
		if err == nil {
			if err = requireLogin(cs); err == nil {
				return cs, nil
			}
		}
		last = err
	}
	return nil, fmt.Errorf("%s: %w", b.Name, last)
}

func requireLogin(cs []Cookie) error {
	for _, c := range cs {
		if loginCookies[c.Name] {
			return nil
		}
	}
	return ErrNotSignedIn
}

// WriteNetscape writes cs as a Netscape cookies.txt at path, readable only by the owner.
func WriteNetscape(path string, cs []Cookie) error {
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n# Written by ytm from a browser import. Keep it private.\n\n")
	for _, c := range cs {
		if !safeField(c.Domain) || !safeField(c.Path) || !safeField(c.Name) || !safeField(c.Value) || c.Name == "" {
			continue // a tab or newline would corrupt the file (and could forge other lines)
		}
		sub := "FALSE"
		if strings.HasPrefix(c.Domain, ".") {
			sub = "TRUE"
		}
		sec := "FALSE"
		if c.Secure {
			sec = "TRUE"
		}
		p := c.Path
		if p == "" {
			p = "/"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", c.Domain, sub, p, sec, max(c.Expires, 0), c.Name, c.Value)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func safeField(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// withPrivateCopy copies a cookie database (and its -wal/-shm/-journal) into a fresh
// owner-only folder, opens the copy read-only and removes it all afterwards: browsers keep
// the original locked, and the copy holds secrets.
func withPrivateCopy[T any](src string, f func(*sql.DB) (T, error)) (T, error) {
	var zero T
	dir, err := os.MkdirTemp("", "ytm-cookies-*") // 0700
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "cookies.sqlite")
	if err := copyFile(src, dst); err != nil {
		hint := ""
		if runtime.GOOS == "windows" {
			hint = " If the browser is running, close it and try again."
		}
		return zero, fmt.Errorf("can't read %s: %v.%s", src, err, hint)
	}
	for _, ext := range []string{"-wal", "-shm", "-journal"} {
		_ = copyFile(src+ext, dst+ext)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dst)+"?mode=ro")
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	return f(conn)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, 512<<20)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func hostIn() string { return "('" + strings.Join(youTubeHosts, "','") + "')" }

func mtime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func sortByMtime(dbs []db) {
	sort.SliceStable(dbs, func(i, j int) bool { return mtime(dbs[i].path).After(mtime(dbs[j].path)) })
}
