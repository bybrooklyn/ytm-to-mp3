// Package vpn gives ytm several network "exits" to spread downloads across.
//
// An Exit is just a SOCKS5 proxy URL. It can be one you supply (--proxy) or one
// ytm builds itself from WireGuard configs (--vpn): on Linux each config gets its
// own network namespace, so only ytm's traffic uses it and nothing leaks.
package vpn

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Exit is one way out to the internet. An empty Proxy means the normal connection.
type Exit struct {
	Name  string
	Proxy string // e.g. socks5h://user:pass@127.0.0.1:1080 ; "" = direct
}

// Direct is the machine's normal connection.
var Direct = Exit{Name: "direct"}

// ConfDir is where `ytm vpn add` keeps WireGuard configs.
func ConfDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ytm", "vpn"), nil
}

// Proxies turns "url,url" lists into exits.
func Proxies(list []string) ([]Exit, error) {
	var out []Exit
	for _, item := range list {
		for _, p := range strings.Split(item, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !strings.Contains(p, "://") {
				p = "socks5h://" + p
			}
			if !strings.HasPrefix(p, "socks5://") && !strings.HasPrefix(p, "socks5h://") &&
				!strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
				return nil, fmt.Errorf("unsupported proxy %q (use socks5://host:port or http://host:port)", p)
			}
			out = append(out, Exit{Name: "proxy:" + hostOf(p), Proxy: p})
		}
	}
	return out, nil
}

func hostOf(p string) string {
	p = p[strings.Index(p, "://")+3:]
	if i := strings.LastIndexByte(p, '@'); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// ConfFiles expands files and directories into a sorted list of .conf paths.
// With no paths it uses the saved ConfDir.
func ConfFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		d, err := ConfDir()
		if err != nil {
			return nil, err
		}
		paths = []string{d}
	}
	var out []string
	for _, item := range paths {
		for _, p := range strings.Split(item, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			st, err := os.Stat(p)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if !st.IsDir() {
				out = append(out, p)
				continue
			}
			m, _ := filepath.Glob(filepath.Join(p, "*.conf"))
			out = append(out, m...)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no WireGuard configs found; add some with: ytm vpn add <file.conf>...")
	}
	return out, nil
}

// Add copies WireGuard configs into ConfDir (private, 0600).
func Add(files []string) ([]string, error) {
	d, err := ConfDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, err
	}
	var added []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return added, err
		}
		if _, err := ParseWG(string(b)); err != nil {
			return added, fmt.Errorf("%s: %w", f, err)
		}
		name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)) + ".conf"
		dst := filepath.Join(d, name)
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return added, err
		}
		added = append(added, dst)
	}
	return added, nil
}
