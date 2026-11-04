package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixedKeys []key

func (f fixedKeys) keys(string) []key { return f }

func TestKeyDerivation(t *testing.T) {
	// openssl kdf -keylen 16 -kdfopt digest:SHA1 -kdfopt pass:peanuts -kdfopt salt:saltysalt -kdfopt iter:N PBKDF2
	if got := hex.EncodeToString(cbcKey("peanuts", 1)); got != "fd621fe5a2b402539dfa147ca9272778" {
		t.Errorf("linux key %s", got)
	}
	if got := hex.EncodeToString(cbcKey("peanuts", 1003)); got != "d9a09d499b4e1b7461f28e67972c6dbd" {
		t.Errorf("mac key %s", got)
	}
}

// encrypt makes a blob the way Chromium does (for the tests).
func encrypt(t *testing.T, k key, prefix string, plain []byte) []byte {
	block, err := aes.NewCipher(k)
	if err != nil {
		t.Fatal(err)
	}
	if len(k) == 32 {
		g, _ := cipher.NewGCM(block)
		nonce := bytes.Repeat([]byte{7}, 12)
		return append(append([]byte(prefix), nonce...), g.Seal(nil, nonce, plain, nil)...)
	}
	n := aes.BlockSize - len(plain)%aes.BlockSize
	p := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, 16)).CryptBlocks(out, p)
	return append([]byte(prefix), out...)
}

func TestDecrypt(t *testing.T) {
	cbc, gcm := cbcKey("pw", 1), key(bytes.Repeat([]byte{3}, 32))
	if s, err := decrypt(encrypt(t, cbc, "v11", []byte("secret-value")), fixedKeys{cbcKey("wrong", 1), cbc}, false); err != nil || s != "secret-value" {
		t.Errorf("cbc: %q %v", s, err)
	}
	if s, err := decrypt(encrypt(t, gcm, "v10", []byte("g")), fixedKeys{gcm}, false); err != nil || s != "g" {
		t.Errorf("gcm: %q %v", s, err)
	}
	hash := sha256.Sum256([]byte(".youtube.com"))
	if s, err := decrypt(encrypt(t, cbc, "v10", append(hash[:], "after-hash"...)), fixedKeys{cbc}, true); err != nil || s != "after-hash" {
		t.Errorf("db v24 host hash: %q %v", s, err)
	}
	if _, err := decrypt([]byte("v20xxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), fixedKeys{cbc}, false); !errors.Is(err, errAppBound) {
		t.Errorf("v20: %v", err)
	}
	if _, err := decrypt(encrypt(t, cbc, "v11", []byte("x")), fixedKeys{cbcKey("other", 1)}, false); err == nil {
		t.Error("wrong key accepted")
	}
	if _, err := decrypt(encrypt(t, cbc, "v11", []byte("a\nb")), fixedKeys{cbc}, false); err == nil {
		t.Error("a value with a newline must be refused")
	}
}

func mkdb(t *testing.T, path string, stmts ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	c, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, s := range stmts {
		if _, err := c.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestFirefox(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.default", "cookies.sqlite")
	mkdb(t, p, `CREATE TABLE moz_cookies (host TEXT, path TEXT, isSecure INT, expiry INT, name TEXT, value TEXT)`,
		`INSERT INTO moz_cookies VALUES ('.youtube.com','/',1,1900000000,'SAPISID','good')`,
		`INSERT INTO moz_cookies VALUES ('.youtube.com','/',1,1900000000000,'SID','ms-expiry')`,
		`INSERT INTO moz_cookies VALUES ('.google.com','/',1,0,'SAPISID','google-only')`,
		`INSERT INTO moz_cookies VALUES ('music.youtube.com','/',0,0,'PREF','p')`)
	cs, err := readFirefox(p)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Cookie{}
	for _, c := range cs {
		got[c.Name] = c
	}
	if len(cs) != 3 || got["SAPISID"].Value != "good" || got["SID"].Expires != 1900000000 || got["PREF"].Domain != "music.youtube.com" {
		t.Errorf("%+v", cs)
	}
	if requireLogin(cs) != nil {
		t.Error("signed-in cookies refused")
	}
	if requireLogin([]Cookie{{Name: "PREF"}}) == nil {
		t.Error("signed-out cookies accepted")
	}
}

func TestChromium(t *testing.T) {
	k := cbcKey("pw", 1)
	hash := sha256.Sum256([]byte(".youtube.com"))
	blob := hex.EncodeToString(encrypt(t, k, "v11", append(hash[:], "decrypted"...)))
	p := filepath.Join(t.TempDir(), "Default", "Network", "Cookies")
	mkdb(t, p, `CREATE TABLE meta (key TEXT, value TEXT)`, `INSERT INTO meta VALUES ('version','24')`,
		`CREATE TABLE cookies (host_key TEXT, path TEXT, is_secure INT, expires_utc INT, name TEXT, value TEXT, encrypted_value BLOB)`,
		`INSERT INTO cookies VALUES ('.youtube.com','/',1,13400000000000000,'__Secure-3PAPISID','',X'`+blob+`')`,
		`INSERT INTO cookies VALUES ('.youtube.com','/',0,0,'PLAIN','plain',X'')`,
		`INSERT INTO cookies VALUES ('.google.com','/',1,0,'SID','no',X'')`)
	cs, err := readChromium(db{path: p}, fixedKeys{k})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Cookie{}
	for _, c := range cs {
		got[c.Name] = c
	}
	if len(cs) != 2 || got["__Secure-3PAPISID"].Value != "decrypted" || got["PLAIN"].Value != "plain" {
		t.Fatalf("%+v", cs)
	}
	if e := got["__Secure-3PAPISID"].Expires; e != 13400000000-chromiumEpoch {
		t.Errorf("expiry %d", e)
	}
	if _, err := readChromium(db{path: p}, fixedKeys{cbcKey("wrong", 1)}); err != nil {
		t.Errorf("one undecryptable cookie must not fail the rest: %v", err)
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	touch := func(rel string) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, nil, 0o600)
	}
	touch(".config/chromium/Default/Cookies")
	touch(".config/BraveSoftware/Brave-Browser/Profile 1/Network/Cookies")
	touch(".mozilla/firefox/abc.default/cookies.sqlite")
	bs := detect("linux", dirs{home: home})
	ids := map[string]bool{}
	for _, b := range bs {
		ids[b.ID] = true
	}
	if len(bs) != 3 || !ids["chromium"] || !ids["brave"] || !ids["firefox"] {
		t.Fatalf("%+v", bs)
	}
	if len(detect("linux", dirs{home: filepath.Join(home, "nothing")})) != 0 {
		t.Error("nothing installed should find nothing")
	}
	// Windows: Opera lives under %APPDATA%, the rest under %LOCALAPPDATA%
	for _, def := range chromiumDefs {
		if def.id == "opera" {
			if r := def.roots("windows", dirs{local: `L`, roaming: `R`}); len(r) != 1 || !strings.HasPrefix(r[0], "R") {
				t.Errorf("opera windows root %v", r)
			}
		}
	}
}

func TestWriteNetscape(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.txt")
	err := WriteNetscape(p, []Cookie{
		{Domain: ".youtube.com", Path: "/", Secure: true, Expires: 1900000000, Name: "SAPISID", Value: "v"},
		{Domain: "music.youtube.com", Name: "PREF", Value: "p"},
		{Domain: ".youtube.com", Name: "EVIL", Value: "x\n.google.com\tTRUE\t/\tTRUE\t0\tSID\tstolen"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.HasPrefix(s, "# Netscape HTTP Cookie File") ||
		!strings.Contains(s, ".youtube.com\tTRUE\t/\tTRUE\t1900000000\tSAPISID\tv\n") ||
		!strings.Contains(s, "music.youtube.com\tFALSE\t/\tFALSE\t0\tPREF\tp\n") {
		t.Errorf("format:\n%s", s)
	}
	if strings.Contains(s, "EVIL") || strings.Contains(s, "stolen") {
		t.Error("a value with a newline must be dropped, not written")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
}
