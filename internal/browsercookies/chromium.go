package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Chromium-family cookie values are encrypted:
//
//	OS       prefix  cipher        key from
//	Linux    v10     AES-128-CBC   fixed password "peanuts" (PBKDF2-SHA1, 1 iteration)
//	Linux    v11     AES-128-CBC   KWallet / libsecret "<Product> Safe Storage" password
//	macOS    v10     AES-128-CBC   Keychain "<Product> Safe Storage" password (1003 iterations)
//	Windows  v10     AES-256-GCM   "Local State" key, unwrapped with DPAPI
//	Windows  v20     app-bound     not readable by other apps: not attempted
type chromiumDef struct {
	id, name string
	product  string   // "<product> Safe Storage" (keyring / Keychain entry)
	app      string   // libsecret "application" attribute
	linux    []string // relative to $HOME
	mac      []string // relative to ~/Library/Application Support
	win      []string // relative to %LOCALAPPDATA% ("roaming:" prefix: %APPDATA%)
}

var chromiumDefs = []chromiumDef{
	{"chrome", "Google Chrome", "Chrome", "chrome",
		[]string{".config/google-chrome", ".var/app/com.google.Chrome/config/google-chrome"},
		[]string{"Google/Chrome"}, []string{"Google/Chrome/User Data"}},
	{"chromium", "Chromium", "Chromium", "chromium",
		[]string{".config/chromium", ".var/app/org.chromium.Chromium/config/chromium", "snap/chromium/common/chromium"},
		[]string{"Chromium"}, []string{"Chromium/User Data"}},
	{"brave", "Brave", "Brave", "brave",
		[]string{".config/BraveSoftware/Brave-Browser", ".var/app/com.brave.Browser/config/BraveSoftware/Brave-Browser"},
		[]string{"BraveSoftware/Brave-Browser"}, []string{"BraveSoftware/Brave-Browser/User Data"}},
	{"vivaldi", "Vivaldi", "Vivaldi", "vivaldi",
		[]string{".config/vivaldi", ".var/app/com.vivaldi.Vivaldi/config/vivaldi"},
		[]string{"Vivaldi"}, []string{"Vivaldi/User Data"}},
	{"edge", "Microsoft Edge", "Microsoft Edge", "microsoft-edge",
		[]string{".config/microsoft-edge", ".var/app/com.microsoft.Edge/config/microsoft-edge"},
		[]string{"Microsoft Edge"}, []string{"Microsoft/Edge/User Data"}},
	{"opera", "Opera", "Opera", "opera",
		[]string{".config/opera", ".var/app/com.opera.Opera/config/opera"},
		[]string{"com.operasoftware.Opera"}, []string{"roaming:Opera Software/Opera Stable"}},
}

func (c *chromiumDef) roots(goos string, d dirs) []string {
	var out []string
	add := func(base, rel string) {
		if base != "" {
			out = append(out, filepath.Join(base, filepath.FromSlash(rel)))
		}
	}
	switch goos {
	case "darwin":
		for _, r := range c.mac {
			add(filepath.Join(d.home, "Library", "Application Support"), r)
		}
	case "windows":
		for _, r := range c.win {
			if rest, ok := strings.CutPrefix(r, "roaming:"); ok {
				add(d.roaming, rest)
			} else {
				add(d.local, r)
			}
		}
	default:
		for _, r := range c.linux {
			add(d.home, r)
		}
	}
	return out
}

// chromiumDBs is every cookie database under a user-data folder: Default, "Profile N" (and
// the root itself, where Opera keeps it); newer versions use Network/Cookies.
func chromiumDBs(root string) []string {
	dirs := []string{root}
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if n := e.Name(); e.IsDir() && (n == "Default" || strings.HasPrefix(n, "Profile ")) {
				dirs = append(dirs, filepath.Join(root, n))
			}
		}
	}
	var out []string
	for _, d := range dirs {
		for _, p := range []string{filepath.Join(d, "Network", "Cookies"), filepath.Join(d, "Cookies")} {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				out = append(out, p)
			}
		}
	}
	return out
}

// key is an AES key for one scheme: 16 bytes = CBC (Linux/macOS), 32 = GCM (Windows).
type key []byte

// keySource supplies candidate keys for a blob prefix ("v10", "v11"); keys_*.go have the
// OS implementations, tests plug in fixed ones.
type keySource interface{ keys(prefix string) []key }

// cbcKey derives the AES-128 key from a Safe Storage password.
func cbcKey(password string, iterations int) key {
	k, _ := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), iterations, 16)
	return k
}

var errAppBound = errors.New("this browser version uses app-bound cookie encryption (Chrome/Edge/Brave 127+ on Windows), which other apps can't read; use a cookies.txt file or Firefox")

// decrypt decrypts one encrypted_value. stripHash: since cookie DB version 24 the plain
// value starts with a SHA-256 of the host.
func decrypt(enc []byte, ks keySource, stripHash bool) (string, error) {
	if len(enc) < 3 {
		return "", errors.New("short value")
	}
	prefix, body := string(enc[:3]), enc[3:]
	switch prefix {
	case "v10", "v11":
	case "v20":
		return "", errAppBound
	default:
		return "", errors.New("unknown encryption")
	}
	for _, k := range ks.keys(prefix) {
		if plain, ok := k.open(body); ok {
			if stripHash {
				if len(plain) < 32 {
					continue
				}
				plain = plain[32:]
			}
			if s := string(plain); utf8.ValidString(s) && safeField(s) {
				return s, nil
			}
		}
	}
	return "", errors.New("can't decrypt")
}

func (k key) open(body []byte) ([]byte, bool) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, false
	}
	if len(k) == 32 { // GCM: nonce (12) | ciphertext | tag (16)
		g, err := cipher.NewGCM(block)
		if err != nil || len(body) < 12+16 {
			return nil, false
		}
		out, err := g.Open(nil, body[:12], body[12:], nil)
		return out, err == nil
	}
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, false
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(out, body)
	n := int(out[len(out)-1]) // PKCS#7
	if n == 0 || n > aes.BlockSize || n > len(out) || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, false
	}
	return out[:len(out)-n], true
}

func readChromium(d db, ks keySource, hosts []string) ([]Cookie, error) {
	return withPrivateCopy(d.path, func(conn *sql.DB) ([]Cookie, error) { return chromiumCookies(conn, ks, hosts) })
}

// chromiumEpoch is 1601-01-01 in unix seconds; Chromium stores microseconds since then.
const chromiumEpoch = 11644473600

func chromiumCookies(conn *sql.DB, ks keySource, hosts []string) ([]Cookie, error) {
	version := 0
	var v string
	if conn.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&v) == nil {
		version, _ = strconv.Atoi(v)
	}
	rows, err := conn.Query(`SELECT host_key, path, is_secure, expires_utc, name, value, encrypted_value FROM cookies
		WHERE host_key IN ` + hostIn(hosts) + ` ORDER BY host_key DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cookie
	var decryptErr error
	seen := map[string]bool{}
	for rows.Next() {
		var c Cookie
		var secure int
		var expires int64
		var enc []byte
		if err := rows.Scan(&c.Domain, &c.Path, &secure, &expires, &c.Name, &c.Value, &enc); err != nil {
			return nil, err
		}
		if seen[c.Domain+"\x00"+c.Name] {
			continue
		}
		seen[c.Domain+"\x00"+c.Name] = true
		c.Secure = secure != 0
		if expires > 0 {
			c.Expires = expires/1_000_000 - chromiumEpoch
		}
		if c.Value == "" && len(enc) > 0 {
			if c.Value, err = decrypt(enc, ks, version >= 24); err != nil {
				decryptErr = err
				continue
			}
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 && decryptErr != nil {
		if errors.Is(decryptErr, errAppBound) {
			return nil, decryptErr
		}
		return nil, errors.New("couldn't decrypt the cookies (is the keyring/Keychain locked, or are you signed out?)")
	}
	return out, nil
}
