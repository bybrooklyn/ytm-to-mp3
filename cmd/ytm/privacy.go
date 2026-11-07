package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
	"github.com/bybrooklyn/ytm-to-mp3/internal/tag"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
	"github.com/bybrooklyn/ytm-to-mp3/internal/vpn"
)

// providerAPIHost is the one host a provider's sign-in and server list come from ("" if
// it has none: a WireGuard config is just local files).
func providerAPIHost(id string) string {
	switch id {
	case provMullvad:
		return "https://api.mullvad.net"
	case provWireGuard:
		return ""
	}
	return "https://vpn-api.proton.me"
}

type lineKind int

const (
	lineNote lineKind = iota
	lineGood
	lineWarn
)

type privacyLine struct {
	kind lineKind
	text string
	fix  string
}

// privacyLines is what the self-check says about the VPN, private mode and the files that
// hold secrets. It makes no network calls.
func privacyLines(cfg config.Config, v vpnState, o opts) []privacyLine {
	var out []privacyLine
	label := providerLabel(providerID(cfg))
	switch {
	case !v.SignedIn:
		out = append(out, privacyLine{kind: lineNote, text: "VPN: none set up (optional). Downloads and lookups go out from your own IP address; set one up in Settings > VPN account"})
	case cfg.NoVPN || o.noVPN:
		out = append(out, privacyLine{kind: lineNote, text: fmt.Sprintf("VPN: %s (%s) is set up but turned off, so traffic is NOT tunnelled", label, v.Who)})
	default:
		out = append(out, privacyLine{kind: lineGood, text: fmt.Sprintf("VPN: %s (%s). Downloads, link lookups and metadata lookups are tunnelled; if the VPN can't connect ytm stops instead of using your IP (Settings > Test VPN shows each exit)", label, v.Who)})
		out = append(out, privacyLine{kind: lineNote, text: "DNS: names are looked up inside the tunnel. The only lookup outside it is a WireGuard config that names its server by host name; " + label + "'s sign-in and server-list requests go from your own IP"})
	}
	if cfg.Private {
		out = append(out, privacyLine{kind: lineGood, text: "private mode is on: no MusicBrainz or cover-art lookups"})
	} else {
		out = append(out, privacyLine{kind: lineNote, text: "private mode is off: tags are fixed with MusicBrainz (it sees artist and title); switch it on in Settings for none of that"})
	}
	for _, p := range looseSecretFiles() {
		out = append(out, privacyLine{kind: lineWarn, text: fmt.Sprintf("%s can be read by other users on this computer", tilde(p)), fix: "chmod 600 " + p})
	}
	return out
}

// looseSecretFiles lists secret files (and the folder that holds them) readable by other
// users. File modes mean nothing on Windows, where this reports nothing.
func looseSecretFiles() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var paths []string
	if p, err := proton.SessionPath(); err == nil {
		paths = append(paths, p)
	}
	paths = append(paths, config.MullvadPath(), config.AppleTokenPath(), config.AppleCookiesPath(), config.PremiumCookiesPath())
	if d := config.WireGuardDir(); d != "" {
		if ents, err := os.ReadDir(d); err == nil {
			for _, e := range ents {
				paths = append(paths, filepath.Join(d, e.Name()))
			}
		}
	}
	var loose []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o077 != 0 {
			loose = append(loose, p)
		}
	}
	return loose
}

// currentRun is the download run in progress, so work that follows it (tag fixing) can
// use the same VPN exits instead of connecting again.
var (
	runMu      sync.Mutex
	currentRun *runExits
)

func setCurrentRun(r *runExits) {
	runMu.Lock()
	currentRun = r
	runMu.Unlock()
}

func activeRun() *runExits {
	runMu.Lock()
	defer runMu.Unlock()
	return currentRun
}

// withTagNetwork points the MusicBrainz and cover-art lookups at the VPN for the duration
// of f when the VPN is meant to be on, so they never leave from your own IP. If the VPN
// can't connect it returns that error: no lookup is made.
func withTagNetwork(ctx context.Context, u *ui.UI, o opts, f func() error) error {
	cfg := config.Load()
	if !vpnWanted(cfg, o) {
		return f()
	}
	ex := activeRun()
	if ex == nil {
		ex = &runExits{ctx: ctx, u: u, o: o}
		defer ex.Close()
	}
	xs, err := ex.get()
	if err != nil {
		return err
	}
	if len(xs.Exits) == 0 {
		return fmt.Errorf("the VPN has no exit to look tags up through; not using your own IP (use --no-vpn to allow it)")
	}
	c, err := vpn.NewClient(xs.Exits[0].Proxy)
	if err != nil {
		return err
	}
	tag.Transport = c.Transport
	defer func() { tag.Transport = nil }()
	return f()
}
