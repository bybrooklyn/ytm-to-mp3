//go:build !windows

package browsercookies

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// storeKeys derives keys from the browser's "Safe Storage" password, read from the OS
// secret store with its own command-line tool (no cgo): libsecret's secret-tool and
// KWallet's kwallet-query on Linux, the Keychain's `security` on macOS. Reading may pop
// up the usual "allow access" prompt; that is the user's call.
type storeKeys struct {
	def    *chromiumDef
	cached []key
	loaded bool
}

func newKeySource(def *chromiumDef, _ string) keySource { return &storeKeys{def: def} }

func (s *storeKeys) keys(prefix string) []key {
	peanuts := cbcKey("peanuts", 1)
	linux := runtime.GOOS != "darwin"
	if linux && prefix == "v10" {
		return []key{peanuts} // Linux "basic" store: a fixed password
	}
	if !s.loaded {
		s.loaded = true
		iters := 1
		if !linux {
			iters = 1003
		}
		for _, pw := range s.passwords(linux) {
			s.cached = append(s.cached, cbcKey(pw, iters))
		}
	}
	keys := append([]key(nil), s.cached...)
	if linux {
		keys = append(keys, peanuts)
	}
	return keys
}

func (s *storeKeys) passwords(linux bool) []string {
	service := s.def.product + " Safe Storage"
	var cmds [][]string
	if linux {
		cmds = [][]string{
			{"secret-tool", "lookup", "application", s.def.app},
			{"kwallet-query", "-r", service, "-f", s.def.product + " Keys", "kdewallet"},
		}
	} else {
		cmds = [][]string{{"security", "find-generic-password", "-w", "-s", service, "-a", s.def.product}}
	}
	var out []string
	for _, c := range cmds {
		if pw := run(c); pw != "" {
			out = append(out, pw)
		}
	}
	return out
}

// run returns a tool's trimmed output, or "" if it isn't installed, fails or has nothing.
func run(argv []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // a wallet may ask the user
	defer cancel()
	b, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return ""
	}
	s := strings.TrimRight(string(b), "\r\n")
	if strings.ContainsAny(s, "\n\x00") || strings.HasPrefix(s, "Failed") {
		return "" // not a password (kwallet-query prints errors on stdout)
	}
	return s
}
