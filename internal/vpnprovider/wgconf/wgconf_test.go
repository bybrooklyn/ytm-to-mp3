package wgconf

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var (
	kPriv = base64.StdEncoding.EncodeToString(append(make([]byte, 31), 1))
	kPub1 = base64.StdEncoding.EncodeToString(append(make([]byte, 31), 2))
	kPub2 = base64.StdEncoding.EncodeToString(append(make([]byte, 31), 3))
	kPSK  = base64.StdEncoding.EncodeToString(append(make([]byte, 31), 4))
)

func conf(extra string) string {
	return "[Interface]\nPrivateKey = " + kPriv + "\nAddress = 10.8.0.2/32\nDNS = 10.8.0.1\n" + extra
}

func peer(endpoint string) string {
	return "\n[Peer]\nPublicKey = " + kPub1 + "\nAllowedIPs = 0.0.0.0/0\nEndpoint = " + endpoint + "\n"
}

func TestParseBasic(t *testing.T) {
	c, err := Parse(strings.NewReader(conf("MTU = 1280\n# a comment\n") + peer("203.0.113.5:51820") + "PersistentKeepalive = 25 # trailing\nPresharedKey = " + kPSK + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.MTU != 1280 || len(c.Addresses) != 1 || c.Addresses[0].String() != "10.8.0.2" || c.DNS[0].String() != "10.8.0.1" {
		t.Fatalf("interface %+v", c)
	}
	p := c.Peers[0]
	if p.Host != "203.0.113.5" || p.Port != 51820 || p.Preshared == nil || p.PublicKey[31] != 2 {
		t.Fatalf("peer %+v", p)
	}
}

func TestParseIPv6AndDNSLines(t *testing.T) {
	in := "[interface]\nprivatekey=" + kPriv + "\naddress = 10.8.0.2/32, fd00::2/128\nDNS = 10.8.0.1, 2606:4700::1111, internal.example\nDNS = 9.9.9.9\n" +
		"[PEER]\nPublicKey=" + kPub1 + "\nAllowedIPs=0.0.0.0/0, ::/0\nEndpoint=[2001:db8::1]:51821\n"
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Addresses) != 2 || !c.Addresses[1].Is6() {
		t.Fatalf("addresses %v", c.Addresses)
	}
	if len(c.DNS) != 3 { // the search domain is skipped, the second DNS line adds
		t.Fatalf("dns %v", c.DNS)
	}
	if c.Peers[0].Host != "2001:db8::1" || c.Peers[0].Port != 51821 {
		t.Fatalf("endpoint %+v", c.Peers[0])
	}
}

func TestParseMultiplePeersAndHostEndpoint(t *testing.T) {
	c, err := Parse(strings.NewReader(conf("") + peer("vpn.example.com:51820") +
		"\n[Peer]\nPublicKey = "+kPub2+"\nAllowedIPs = 0.0.0.0/0\nEndpoint = 198.51.100.7:443\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Peers) != 2 || c.Peers[0].Host != "vpn.example.com" || c.Peers[1].Port != 443 {
		t.Fatalf("%+v", c.Peers)
	}
}

func TestParseMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"no key":           "[Interface]\nAddress = 10.0.0.2/32\n" + peer("1.2.3.4:51820"),
		"bad key":          "[Interface]\nPrivateKey = nope\nAddress = 10.0.0.2/32\n" + peer("1.2.3.4:51820"),
		"short key":        "[Interface]\nPrivateKey = " + base64.StdEncoding.EncodeToString([]byte("short")) + "\nAddress = 10.0.0.2/32\n" + peer("1.2.3.4:51820"),
		"no address":       "[Interface]\nPrivateKey = " + kPriv + "\n" + peer("1.2.3.4:51820"),
		"bad address":      conf("") + "Address = banana\n" + peer("1.2.3.4:51820"),
		"no peer":          conf(""),
		"no endpoint":      conf("") + "[Peer]\nPublicKey = " + kPub1 + "\nAllowedIPs = 0.0.0.0/0\n",
		"no peer key":      conf("") + "[Peer]\nEndpoint = 1.2.3.4:51820\nAllowedIPs = 0.0.0.0/0\n",
		"bad endpoint":     conf("") + peer("justahost"),
		"bad port":         conf("") + peer("1.2.3.4:99999"),
		"split tunnel":     conf("") + "[Peer]\nPublicKey = " + kPub1 + "\nAllowedIPs = 10.0.0.0/8\nEndpoint = 1.2.3.4:51820\n",
		"no allowed":       conf("") + "[Peer]\nPublicKey = " + kPub1 + "\nEndpoint = 1.2.3.4:51820\n",
		"line w/o equals":  conf("garbage line\n") + peer("1.2.3.4:51820"),
		"unknown section":  conf("") + "[Wat]\n",
		"outside sections": "PrivateKey = " + kPriv + "\n",
		"bad mtu":          conf("MTU = 5\n") + peer("1.2.3.4:51820"),
		"bad allowedips":   conf("") + "[Peer]\nPublicKey = " + kPub1 + "\nAllowedIPs = lots\nEndpoint = 1.2.3.4:51820\n",
	}
	for name, in := range cases {
		_, err := Parse(strings.NewReader(in))
		if err == nil {
			t.Errorf("%s: parsed, want an error", name)
			continue
		}
		if strings.Contains(err.Error(), kPriv) || strings.Contains(err.Error(), "nope") {
			t.Errorf("%s: error repeats key material: %v", name, err)
		}
	}
}

func TestScriptHooksAreIgnored(t *testing.T) {
	c, err := Parse(strings.NewReader(conf("PostUp = rm -rf /\nPreDown = x\nTable = off\nFwMark = 1\nListenPort = 51820\nSaveConfig = true\n") + peer("1.2.3.4:51820")))
	if err != nil || c == nil {
		t.Fatalf("hooks must be skipped, not fail: %v", err)
	}
}

func writeConf(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProviderImportAndServers(t *testing.T) {
	src := t.TempDir()
	writeConf(t, src, "home server.conf", conf("")+peer("203.0.113.5:51820")+"\n[Peer]\nPublicKey = "+kPub2+"\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.6:51820\n")
	writeConf(t, src, "bad.conf", "garbage")
	writeConf(t, src, "notes.txt", "ignored")
	p := &Provider{Dir: filepath.Join(t.TempDir(), "wg")}
	if p.SignedIn() {
		t.Fatal("empty dir must not count as signed in")
	}
	n, err := p.Import(src)
	if err != nil || n != 1 {
		t.Fatalf("imported %d, err %v", n, err)
	}
	if !p.SignedIn() || p.Files() != 1 {
		t.Fatal("import not visible")
	}
	ents, _ := os.ReadDir(p.Dir)
	if len(ents) != 1 || ents[0].Name() != "home_server.conf" {
		t.Fatalf("files %v", ents)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(p.Dir, ents[0].Name()))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("imported config mode %v, want 0600 (it holds a private key)", st.Mode().Perm())
		}
		dst, _ := os.Stat(p.Dir)
		if dst.Mode().Perm() != 0o700 {
			t.Fatalf("config dir mode %v, want 0700", dst.Mode().Perm())
		}
	}
	sl, err := p.ServersInfo(context.Background())
	if err != nil || len(sl.Servers) != 2 || sl.MaxConnect != 1 {
		t.Fatalf("servers %+v err %v", sl, err)
	}
	if sl.Servers[0].Name != "home_server" || sl.Servers[1].Name != "home_server#2" {
		t.Fatalf("names %q %q", sl.Servers[0].Name, sl.Servers[1].Name)
	}
	cfg, err := p.Tunnel(sl.Servers[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "203.0.113.6:51820" || cfg.PeerKey[31] != 3 || cfg.PrivateKey[31] != 1 {
		t.Fatalf("tunnel %+v", cfg)
	}
	if id := p.AccountID(); id == "" || strings.Contains(id, kPriv) {
		t.Fatalf("account id %q", id)
	}
	if _, err := p.Import(filepath.Join(src, "bad.conf")); err == nil {
		t.Fatal("importing a bad file must fail")
	}
	if err := p.Remove(); err != nil {
		t.Fatal(err)
	}
	if p.SignedIn() {
		t.Fatal("still signed in after Remove")
	}
}

func TestImportNothing(t *testing.T) {
	p := &Provider{Dir: t.TempDir()}
	if _, err := p.Import(t.TempDir()); err == nil {
		t.Fatal("an empty folder is an error")
	}
	if _, err := p.Import(filepath.Join(t.TempDir(), "missing.conf")); err == nil {
		t.Fatal("a missing file is an error")
	}
}
