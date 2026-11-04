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
	Jobs        int    `json:"jobs,omitempty"`        // parallel downloads; 0 = auto
	Countries   string `json:"countries,omitempty"`   // preferred exit country code; "" = auto/best
	// Premium turns on the optional YouTube Premium upgrade pass; its cookies are in
	// PremiumCookiesPath(). Never used for normal downloads.
	Premium bool `json:"premium,omitempty"`

	// What a library download also does. All on by default (the zero value), so they're
	// stored as "No...": off only when switched off in settings.
	NoAlbums       bool `json:"no_albums,omitempty"`        // complete every album you have a song from
	NoRetry        bool `json:"no_retry,omitempty"`         // retry songs that were unavailable before
	NoTagAfter     bool `json:"no_tag_after,omitempty"`     // fix the new songs' tags with MusicBrainz
	NoPremiumAfter bool `json:"no_premium_after,omitempty"` // run the Premium upgrade (when signed in)

	// Apple Music (optional, native AAC path). The media-user-token itself is a secret
	// and lives in AppleTokenPath(), not here.
	AppleStorefront string `json:"apple_storefront,omitempty"` // e.g. "us"; "" = us
	AppleLanguage   string `json:"apple_language,omitempty"`   // e.g. "en-US"; "" = storefront default
}

// AppleTokenPath is where ytm keeps the user's Apple Music media-user-token
// (mode 0600): the "login" for the native Apple Music download path.
func AppleTokenPath() string {
	d, err := dir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "apple-token")
}

// AppleCookiesPath is where ytm keeps the full Apple cookie jar (mode 0600),
// an HTTP Cookie header value; the web-playback endpoint needs more than the
// media-user-token alone.
func AppleCookiesPath() string {
	d, err := dir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "apple-cookies")
}

// AppleToken reads the saved Apple Music media-user-token, or "" if none.
func AppleToken() string {
	p := AppleTokenPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// AppleCookies reads the saved Apple cookie jar (Cookie header), or "".
func AppleCookies() string {
	p := AppleCookiesPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveAppleCookies writes the cookie jar (mode 0600), or removes it when empty.
func SaveAppleCookies(jar string) error {
	p := AppleCookiesPath()
	if p == "" {
		return fmt.Errorf("no config directory")
	}
	jar = strings.TrimSpace(jar)
	if jar == "" {
		err := os.Remove(p)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(jar+"\n"), 0o600)
}

// SaveAppleToken writes the media-user-token (mode 0600), or removes it when empty.
func SaveAppleToken(token string) error {
	p := AppleTokenPath()
	if p == "" {
		return fmt.Errorf("no config directory")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		err := os.Remove(p)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(token+"\n"), 0o600)
}

// PremiumCookiesPath is where ytm keeps its copy of the user's YouTube Premium cookies
// (mode 0600). Only the upgrade pass reads it.
func PremiumCookiesPath() string {
	d, err := dir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "premium-cookies.txt")
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

// DefaultFormat is the format when none is chosen: Opus, YouTube's own best audio, copied
// into the file untouched (every other format is a re-encode).
const DefaultFormat = "opus"

// DefaultOut is ~/Music, so libraries don't scatter across whatever folder you ran ytm from.
func DefaultOut() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Music")
	}
	return "Music"
}
