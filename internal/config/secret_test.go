package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Secrets (tokens, cookies, account numbers, WireGuard keys) must end up 0600 in a 0700
// folder, even when an older, looser file is already there.
func TestWriteSecretTightensExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	d := filepath.Join(t.TempDir(), "ytm")
	p := filepath.Join(d, "token")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSecret(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestSavedSecretsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	// a token file left loose by an older version
	p := AppleTokenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	for name, save := range map[string]func() error{
		"apple token":   func() error { return SaveAppleToken("tok") },
		"apple cookies": func() error { return SaveAppleCookies("a=b") },
		"config":        func() error { return Config{Provider: "mullvad"}.Save() },
	} {
		if err := save(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, f := range []string{AppleTokenPath(), AppleCookiesPath(), path0(t)} {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", filepath.Base(f), st.Mode().Perm())
		}
	}
}

func path0(t *testing.T) string {
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProviderSettingRoundTripsAndOldFilesStillLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if err := (Config{Provider: "wireguard", Private: true}).Save(); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if c.Provider != "wireguard" || !c.Private {
		t.Fatalf("%+v", c)
	}
	p, _ := path()
	if err := os.WriteFile(p, []byte(`{"connections":4,"no_vpn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := Load(); c.Provider != "" || c.Private || c.Connections != 4 {
		t.Fatalf("an old config must keep working with defaults: %+v", c)
	}
}
