// Package config persists the few choices ytm remembers between runs.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	// Saved by `ytm setup`; command-line flags override them for one run.
	Out         string `json:"out,omitempty"`
	Format      string `json:"format,omitempty"`
	Connections int    `json:"connections,omitempty"` // VPN exits to use at once
	NoVPN       bool   `json:"no_vpn,omitempty"`      // sign-in is kept but VPN is off
}

func dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ytm"), nil
}

// CacheDir is where managed tools and the server cache live.
func CacheDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, "ytm")
	return p, os.MkdirAll(p, 0o700)
}

func path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Path is where the config file lives (for messages).
func Path() string {
	p, _ := path()
	return p
}

// Exists reports whether a config file has been saved yet.
func Exists() bool {
	p, err := path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Load returns the saved config, or a zero Config if none exists. A file that can't be
// parsed is reported through the error but still yields a usable zero Config.
func Load() Config {
	c, _ := LoadChecked()
	return c
}

// LoadChecked is Load plus the reason the file was ignored, if it was.
func LoadChecked() (Config, error) {
	var c Config
	p, err := path()
	if err != nil {
		return c, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c, nil // no file yet is normal
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s is not valid JSON (%v); fix or delete it", p, err)
	}
	return c, nil
}

// Save writes the config atomically.
func (c Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ExpandPath turns "~/x" into an absolute path under the home directory and makes
// relative paths absolute.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[1:])
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// DefaultOut is ~/Music, so libraries don't scatter across whatever folder you ran ytm from.
func DefaultOut() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Music")
	}
	return "Music"
}
