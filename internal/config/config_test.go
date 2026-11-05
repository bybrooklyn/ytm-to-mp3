package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadToleratesOldKeysAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir) // Windows
	p, err := path()         // where this OS keeps it (macOS: ~/Library/Application Support)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"out":"/m","format":"opus","cookies":"x","browser":"firefox","future":1}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadChecked()
	if err != nil || c.Out != "/m" || c.Format != "opus" {
		t.Fatalf("c=%+v err=%v", c, err)
	}
	c.Jobs, c.Countries = 8, "NL"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, _ := LoadChecked()
	if c2.Jobs != 8 || c2.Countries != "NL" {
		t.Fatalf("%+v", c2)
	}
}
