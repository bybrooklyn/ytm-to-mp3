package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bybrooklyn/mfetchy/internal/config"
)

func sandbox(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	t.Setenv("HOME", d)
	t.Setenv("USERPROFILE", d) // Windows finds the home folder through this, not HOME
	t.Setenv("AppData", d)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(d, "cache"))
	return d
}

func TestProviderChoiceDefaultsToProton(t *testing.T) {
	for in, want := range map[string]string{"": "proton", "proton": "proton", "mullvad": "mullvad", "wireguard": "wireguard", "bogus": "proton"} {
		if got := providerID(config.Config{Provider: in}); got != want {
			t.Errorf("providerID(%q) = %q, want %q", in, got, want)
		}
	}
	if providerLabel("mullvad") != "Mullvad" || providerLabel("") != "Proton VPN" {
		t.Fatal("labels")
	}
}

func TestCurrentVPNPicksTheChosenProvider(t *testing.T) {
	sandbox(t)
	if v := currentVPN(config.Config{}); v.SignedIn || v.Prov.ID() != "proton" {
		t.Fatalf("fresh install: %+v", v)
	}
	if v := currentVPN(config.Config{Provider: "mullvad"}); v.SignedIn || v.Prov.ID() != "mullvad" {
		t.Fatalf("mullvad, signed out: %+v", v)
	}
	if err := config.WriteSecret(config.MullvadPath(), []byte(`{"account":"1234567812345678"}`)); err != nil {
		t.Fatal(err)
	}
	v := currentVPN(config.Config{Provider: "mullvad"})
	if !v.SignedIn || v.Who != "account ending 5678" {
		t.Fatalf("mullvad, signed in: %+v", v)
	}
	if strings.Contains(v.Who, "1234") {
		t.Fatal("the account number leaks into the display name")
	}
	if !vpnWanted(config.Config{Provider: "mullvad"}, opts{}) {
		t.Fatal("a set-up VPN is wanted")
	}
	if vpnWanted(config.Config{Provider: "mullvad", NoVPN: true}, opts{}) || vpnWanted(config.Config{Provider: "mullvad"}, opts{noVPN: true}) {
		t.Fatal("NoVPN and --no-vpn turn it off")
	}
	// the other providers are untouched by Mullvad's sign-in
	if currentVPN(config.Config{}).SignedIn {
		t.Fatal("proton must not look signed in")
	}
}

func TestPrivacyLines(t *testing.T) {
	sandbox(t)
	text := func(ls []privacyLine) string {
		var b strings.Builder
		for _, l := range ls {
			b.WriteString(l.text + "\n")
		}
		return b.String()
	}
	none := text(privacyLines(config.Config{}, currentVPN(config.Config{}), opts{}))
	if !strings.Contains(none, "none set up") || !strings.Contains(none, "own IP") {
		t.Fatalf("no VPN: %s", none)
	}
	config.WriteSecret(config.MullvadPath(), []byte(`{"account":"1234567812345678"}`))
	cfg := config.Config{Provider: "mullvad", Private: true}
	on := text(privacyLines(cfg, currentVPN(cfg), opts{}))
	for _, want := range []string{"Mullvad", "tunnelled", "stops instead", "private mode is on"} {
		if !strings.Contains(on, want) {
			t.Errorf("missing %q in:\n%s", want, on)
		}
	}
	if strings.Contains(on, "1234567812345678") {
		t.Fatal("the account number is printed")
	}
	off := text(privacyLines(config.Config{Provider: "mullvad", NoVPN: true}, currentVPN(config.Config{Provider: "mullvad"}), opts{}))
	if !strings.Contains(off, "NOT tunnelled") {
		t.Fatalf("VPN off must be said plainly: %s", off)
	}
}

func TestLooseSecretFilesAreReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	sandbox(t)
	p := config.MullvadPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := looseSecretFiles(); len(got) != 1 || got[0] != p {
		t.Fatalf("loose = %v", got)
	}
	var warned bool
	for _, l := range privacyLines(config.Config{}, vpnState{Prov: currentVPN(config.Config{}).Prov}, opts{}) {
		if l.kind == lineWarn && strings.Contains(l.fix, "chmod 600") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("no warning with a fix for a world-readable secret")
	}
	os.Chmod(p, 0o600)
	if got := looseSecretFiles(); len(got) != 0 {
		t.Fatalf("after chmod: %v", got)
	}
}
