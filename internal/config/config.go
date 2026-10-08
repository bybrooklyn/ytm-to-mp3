// Package config persists the few choices ytm remembers between runs.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	// Cookies is "", "yes" or "no". Empty means the user has not been asked yet.
	Cookies string `json:"cookies,omitempty"`
	Browser string `json:"browser,omitempty"`
}

func dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ytm"), nil
}

// CacheDir is where managed tools and the cookie jar live.
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

// Load returns the saved config, or a zero Config if none exists.
func Load() Config {
	var c Config
	if p, err := path(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &c)
		}
	}
	return c
}

// Save writes the config, ignoring failures: remembering is a convenience.
func (c Config) Save() {
	p, err := path()
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(p, b, 0o600)
}
