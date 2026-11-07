package appname

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGetenvPrefersNewName(t *testing.T) {
	t.Setenv("YTM_THING", "old")
	if got := Getenv("THING"); got != "old" {
		t.Errorf("old name alone: %q", got)
	}
	t.Setenv("MFY_THING", "new")
	if got := Getenv("THING"); got != "new" {
		t.Errorf("new name should win: %q", got)
	}
	t.Setenv("MFY_THING", "")
	if v, ok := LookupEnv("THING"); !ok || v != "" {
		t.Errorf("a set-but-empty MFY_ variable still wins: %q %v", v, ok)
	}
	if _, ok := LookupEnv("NEVER_SET_X"); ok {
		t.Error("unset reported as set")
	}
}

func TestMigrateRenamesWholeDirectory(t *testing.T) {
	root := t.TempDir()
	oldP, newP := filepath.Join(root, ".ytm"), filepath.Join(root, ".mfetchy")
	write(t, filepath.Join(oldP, "state.db"), "db")
	write(t, filepath.Join(oldP, "state.db-wal"), "wal")
	write(t, filepath.Join(oldP, "state.db-shm"), "shm")
	write(t, filepath.Join(oldP, "replaced", "a", "b.mp3"), "audio")

	use, moved := Migrate(newP, oldP)
	if use != newP || !moved {
		t.Fatalf("use=%s moved=%v", use, moved)
	}
	if _, err := os.Lstat(oldP); err == nil {
		t.Error("old folder still there")
	}
	for p, want := range map[string]string{"state.db": "db", "state.db-wal": "wal", "state.db-shm": "shm", "replaced/a/b.mp3": "audio"} {
		if got := read(t, filepath.Join(newP, filepath.FromSlash(p))); got != want {
			t.Errorf("%s = %q", p, got)
		}
	}
	// running again is a no-op
	if use, moved := Migrate(newP, oldP); use != newP || moved {
		t.Errorf("second run: %s %v", use, moved)
	}
}

func TestMigrateBothPresentNewWins(t *testing.T) {
	root := t.TempDir()
	oldP, newP := filepath.Join(root, "ytm"), filepath.Join(root, "mfetchy")
	write(t, filepath.Join(oldP, "config.json"), "old")
	write(t, filepath.Join(newP, "config.json"), "new")
	use, moved := Migrate(newP, oldP)
	if use != newP || moved {
		t.Fatalf("use=%s moved=%v", use, moved)
	}
	if read(t, filepath.Join(newP, "config.json")) != "new" || read(t, filepath.Join(oldP, "config.json")) != "old" {
		t.Error("something was touched")
	}
}

func TestMigrateNothingThere(t *testing.T) {
	root := t.TempDir()
	newP := filepath.Join(root, "mfetchy")
	if use, moved := Migrate(newP, filepath.Join(root, "ytm")); use != newP || moved {
		t.Errorf("use=%s moved=%v", use, moved)
	}
	if _, err := os.Lstat(newP); err == nil {
		t.Error("created something from nothing")
	}
}

// When the rename cannot happen the old folder is used and nothing is lost.
func TestMigrateRenameFailureUsesOld(t *testing.T) {
	root := t.TempDir()
	oldP := filepath.Join(root, "ytm")
	write(t, filepath.Join(oldP, "config.json"), "keep me")
	// the new path's parent does not exist, so the rename fails
	newP := filepath.Join(root, "missing-parent", "mfetchy")
	use, moved := Migrate(newP, oldP)
	if use != oldP || moved {
		t.Fatalf("use=%s moved=%v", use, moved)
	}
	if read(t, filepath.Join(oldP, "config.json")) != "keep me" {
		t.Error("data lost")
	}
}

func TestStateDirMigratesOnce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".ytm", "state.db"), "db")
	got := StateDir(root)
	if got != filepath.Join(root, ".mfetchy") {
		t.Fatalf("got %s", got)
	}
	if read(t, filepath.Join(got, "state.db")) != "db" {
		t.Error("contents missing")
	}
	if !TakeMoved(root) || TakeMoved(root) {
		t.Error("moved flag should be reported exactly once")
	}
	if StateDir(root) != got {
		t.Error("second call changed the answer")
	}
}

func TestConfigAndCacheDirMigrate(t *testing.T) {
	cfg, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", t.TempDir())
	write(t, filepath.Join(cfg, "ytm", "config.json"), "c")
	write(t, filepath.Join(cache, "ytm", "yt-dlp"), "bin")
	cd, err := ConfigDir()
	if err != nil {
		t.Skip(err)
	}
	if cd != filepath.Join(cfg, "mfetchy") || read(t, filepath.Join(cd, "config.json")) != "c" {
		t.Skipf("platform does not honour XDG paths: %s", cd)
	}
	kd, _ := CacheDir()
	if kd != filepath.Join(cache, "mfetchy") || read(t, filepath.Join(kd, "yt-dlp")) != "bin" {
		t.Errorf("cache: %s", kd)
	}
}
