package browsercookies

import (
	"database/sql"
	"os"
	"path/filepath"
)

// Firefox-family browsers keep cookies unencrypted in <profile>/cookies.sqlite.
type firefoxDef struct {
	id, name string
	linux    []string // relative to $HOME
	mac      []string // relative to ~/Library/Application Support
	win      []string // relative to %APPDATA%
}

var firefoxDefs = []firefoxDef{
	{"firefox", "Firefox",
		[]string{".mozilla/firefox", ".var/app/org.mozilla.firefox/.mozilla/firefox", "snap/firefox/common/.mozilla/firefox"},
		[]string{"Firefox/Profiles"}, []string{"Mozilla/Firefox/Profiles"}},
	{"zen", "Zen", []string{".zen", ".var/app/app.zen_browser.zen/.zen"}, []string{"zen/Profiles"}, []string{"zen/Profiles"}},
	{"librewolf", "LibreWolf", []string{".librewolf", ".var/app/io.gitlab.librewolf-community/.librewolf"},
		[]string{"LibreWolf/Profiles"}, []string{"LibreWolf/Profiles"}},
	{"waterfox", "Waterfox", []string{".waterfox"}, []string{"Waterfox/Profiles"}, []string{"Waterfox/Profiles"}},
}

func (f firefoxDef) roots(goos string, d dirs) []string {
	var rel []string
	base := d.home
	switch goos {
	case "darwin":
		rel, base = f.mac, filepath.Join(d.home, "Library", "Application Support")
	case "windows":
		rel, base = f.win, d.roaming
	default:
		rel = f.linux
	}
	if base == "" {
		return nil
	}
	out := make([]string, 0, len(rel))
	for _, r := range rel {
		out = append(out, filepath.Join(base, filepath.FromSlash(r)))
	}
	return out
}

// firefoxDBs is cookies.sqlite of every profile under root.
func firefoxDBs(root string) []string {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		p := filepath.Join(root, e.Name(), "cookies.sqlite")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}

func readFirefox(path string) ([]Cookie, error) {
	return withPrivateCopy(path, firefoxCookies)
}

func firefoxCookies(conn *sql.DB) ([]Cookie, error) {
	rows, err := conn.Query(`SELECT host, path, isSecure, expiry, name, value FROM moz_cookies
		WHERE host IN ` + hostIn() + ` ORDER BY host DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cookie
	seen := map[string]bool{}
	for rows.Next() {
		var c Cookie
		var secure int
		if err := rows.Scan(&c.Domain, &c.Path, &secure, &c.Expires, &c.Name, &c.Value); err != nil {
			return nil, err
		}
		if seen[c.Domain+"\x00"+c.Name] {
			continue
		}
		seen[c.Domain+"\x00"+c.Name] = true
		c.Secure = secure != 0
		if c.Expires > 1e11 { // newer Firefox stores milliseconds
			c.Expires /= 1000
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
