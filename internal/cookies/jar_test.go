package cookies

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jar = "# Netscape HTTP Cookie File\n" +
	".youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\tabc\n" +
	"#HttpOnly_.google.com\tTRUE\t/\tTRUE\t0\t__Secure-3PSID\tdef\n" +
	"accounts.google.com\tFALSE\t/\tTRUE\t0\tX\ty\n" +
	".evil.com\tTRUE\t/\tTRUE\t0\tbank\tsecret\n" +
	".notgoogle.com\tTRUE\t/\tTRUE\t0\tbank2\tsecret\n" +
	"#HttpOnly_.bank.com\tTRUE\t/\tTRUE\t0\ts\tsecret\n" +
	".i.ytimg.com\tTRUE\t/\tTRUE\t0\tz\t1\n"

func TestFilterJar(t *testing.T) {
	got := string(filterJar([]byte(jar)))
	for _, bad := range []string{"evil", "notgoogle", "bank", "secret"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept %q:\n%s", bad, got)
		}
	}
	for _, good := range []string{"# Netscape", "SAPISID", "#HttpOnly_.google.com", "accounts.google.com", "ytimg"} {
		if !strings.Contains(got, good) {
			t.Errorf("dropped %q:\n%s", good, got)
		}
	}
}

func TestWritePrivateMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cookies.txt")
	os.WriteFile(p, []byte("old"), 0o644)
	if err := writePrivate(p, filterJar([]byte(jar))); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if !LoggedIn(p) {
		t.Error("should be logged in")
	}
}
