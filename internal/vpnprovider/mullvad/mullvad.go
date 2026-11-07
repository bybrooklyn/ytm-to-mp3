// Package mullvad is the Mullvad provider. Signing in is just the account number.
//
// API shape (checked against github.com/mullvad/mullvadvpn-app, mullvad-api, and the
// public relay list on 2026-11-06):
//
//	GET  /public/relays/wireguard/v2   no auth; locations + relays (the server list)
//	POST /auth/v1/token                {"account_number"} -> {"access_token","expiry"}
//	GET  /accounts/v1/devices          Bearer token; the account's registered keys
//	POST /accounts/v1/devices          {"pubkey","hijack_dns"} -> 201 {id,pubkey,ipv4_address,ipv6_address,...}
//	DELETE /accounts/v1/devices/{id}   -> 204
//
// An account holds at most 5 devices (WireGuard keys). Each simultaneous tunnel gets its
// own key (so tunnels never share one), registered on first use; the limit therefore caps
// how many tunnels mfetchy opens, counting devices other apps already hold.
//
// The account number is a secret: it is kept in a mode-0600 file, sent only in the body of
// requests to api.mullvad.net, and never written to logs or error messages.
package mullvad

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider"
)

// MaxDevices is how many WireGuard keys one Mullvad account may hold.
const MaxDevices = 5

const (
	defaultBase = "https://api.mullvad.net"
	defaultMeta = "https://ipv4.am.i.mullvad.net"
	dnsAddr     = "10.64.0.1" // Mullvad's in-tunnel resolver
)

var accountRe = regexp.MustCompile(`^[0-9]{16}$`)

// NormalizeAccount strips the spaces and dashes people type between the digit groups and
// checks the result looks like an account number.
func NormalizeAccount(s string) (string, error) {
	s = strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(s))
	if !accountRe.MatchString(s) {
		return "", errors.New("a Mullvad account number is 16 digits")
	}
	return s, nil
}

// APIError is a failure reported by Mullvad. It never contains the account number.
type APIError struct {
	Status int
	Code   string // e.g. MAX_DEVICES_REACHED
	Detail string
}

func (e *APIError) Error() string {
	switch e.Code {
	case "INVALID_ACCOUNT":
		return "Mullvad doesn't know that account number"
	case "MAX_DEVICES_REACHED":
		return fmt.Sprintf("your Mullvad account already has %d devices; remove one at mullvad.net/account, then try again", MaxDevices)
	case "PUBKEY_IN_USE":
		return "Mullvad says this key is already registered"
	case "THROTTLED", "":
		if e.Status == http.StatusTooManyRequests {
			return "Mullvad is rate limiting requests; wait a few minutes"
		}
	}
	s := fmt.Sprintf("Mullvad answered HTTP %d", e.Status)
	if e.Code != "" {
		s += " (" + e.Code + ")"
	}
	return s
}

// device is one key mfetchy registered (or is about to register) on the account.
type device struct {
	Slot    int    `json:"slot"`
	ID      string `json:"id,omitempty"` // empty until Mullvad has accepted the key
	Private string `json:"private"`      // base64 X25519
	Public  string `json:"public"`
	IPv4    string `json:"ipv4,omitempty"` // tunnel address Mullvad assigned this key
}

type state struct {
	Account string   `json:"account"`
	Devices []device `json:"devices,omitempty"`
}

// Provider talks to Mullvad. Path is the 0600 file holding the account and device keys.
type Provider struct {
	Path string
	Base string // API origin; overridden by tests
	Meta string // am.i.mullvad.net origin; overridden by tests
	HTTP *http.Client

	mu     sync.Mutex
	st     state
	token  string
	tokExp time.Time
	regMu  sync.Mutex // one registration at a time

	verified atomic.Bool // our keys were compared with the account's list this run
}

var _ vpnprovider.Provider = (*Provider)(nil)

// New returns a provider, loading a saved account from path if there is one.
func New(path string) *Provider {
	p := &Provider{Path: path, Base: defaultBase, Meta: defaultMeta, HTTP: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if path != "" {
		if b, err := readFile(path); err == nil {
			_ = json.Unmarshal(b, &p.st)
		}
	}
	return p
}

func (p *Provider) ID() string    { return "mullvad" }
func (p *Provider) Label() string { return "Mullvad" }
func (p *Provider) MaxSlots() int { return MaxDevices }

// SignedIn reports whether an account number is stored.
func (p *Provider) SignedIn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Account != ""
}

// Who is how to show the account without revealing it: only the last four digits.
func (p *Provider) Who() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.st.Account) < 4 {
		return ""
	}
	return "account ending " + p.st.Account[len(p.st.Account)-4:]
}

// AccountID tags the account (a hash; the number itself never leaves the file).
func (p *Provider) AccountID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st.Account == "" {
		return ""
	}
	return "mv-" + vpnprovider.AccountHash(p.st.Account)
}

func (p *Provider) saveLocked() error {
	if p.Path == "" {
		return nil
	}
	b, err := json.MarshalIndent(p.st, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteSecret(p.Path, b)
}

// Login checks the account number with Mullvad and stores it. Keys are registered later,
// one per tunnel, when the tunnels are opened.
func (p *Provider) Login(ctx context.Context, account string) error {
	account, err := NormalizeAccount(account)
	if err != nil {
		return err
	}
	p.mu.Lock()
	prev := p.st
	p.st = state{Account: account}
	p.token = ""
	p.mu.Unlock()
	if _, err := p.accessToken(ctx); err != nil {
		p.mu.Lock()
		p.st = prev
		p.mu.Unlock()
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveLocked()
}

// Logout removes mfetchy's devices from the account (best effort; a device slot is precious,
// there are only 5) and forgets the account number and keys locally.
func (p *Provider) Logout(ctx context.Context) {
	p.mu.Lock()
	devs := append([]device(nil), p.st.Devices...)
	signed := p.st.Account != ""
	p.mu.Unlock()
	if signed {
		for _, d := range devs {
			if d.ID == "" {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = p.do(rctx, http.MethodDelete, "/accounts/v1/devices/"+d.ID, true, nil, nil, http.StatusNoContent)
			cancel()
		}
	}
	p.mu.Lock()
	p.st, p.token = state{}, ""
	p.mu.Unlock()
	if p.Path != "" {
		_ = removeFile(p.Path)
	}
}

// do sends one JSON request. want is the expected success status.
func (p *Provider) do(ctx context.Context, method, path string, authed bool, in, out any, want int) error {
	for try := 0; ; try++ {
		var body io.Reader
		if in != nil {
			b, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, p.Base+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "mfetchy") // nothing about this machine
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authed {
			tok, err := p.accessToken(ctx)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := p.HTTP.Do(req)
		if err != nil {
			return scrub(err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode == want {
			if out != nil && len(raw) > 0 {
				return json.Unmarshal(raw, out)
			}
			return nil
		}
		ae := &APIError{Status: resp.StatusCode}
		var env struct{ Code, Detail string }
		if json.Unmarshal(raw, &env) == nil {
			ae.Code, ae.Detail = env.Code, env.Detail
		}
		if authed && try == 0 && (resp.StatusCode == http.StatusUnauthorized || ae.Code == "INVALID_ACCESS_TOKEN") {
			p.mu.Lock()
			p.token = ""
			p.mu.Unlock()
			continue // the token expired early: fetch another and resend once
		}
		return ae
	}
}

// scrub keeps transport errors (which name the URL, never the body) short and secret-free.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return fmt.Errorf("can't reach Mullvad: %w", ue.Err)
	}
	return fmt.Errorf("can't reach Mullvad: %w", err)
}

// accessToken returns a valid API token for the stored account.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.token != "" && time.Now().Before(p.tokExp) {
		t := p.token
		p.mu.Unlock()
		return t, nil
	}
	account := p.st.Account
	p.mu.Unlock()
	if account == "" {
		return "", errors.New("not signed in to Mullvad: sign in in mfetchy's Settings")
	}
	var r struct {
		AccessToken string    `json:"access_token"`
		Expiry      time.Time `json:"expiry"`
	}
	if err := p.do(ctx, http.MethodPost, "/auth/v1/token", false, map[string]string{"account_number": account}, &r, http.StatusOK); err != nil {
		return "", err
	}
	if r.AccessToken == "" {
		return "", errors.New("Mullvad sent no access token")
	}
	exp := r.Expiry.Add(-time.Minute)
	if r.Expiry.IsZero() {
		exp = time.Now().Add(30 * time.Minute)
	}
	p.mu.Lock()
	p.token, p.tokExp = r.AccessToken, exp
	p.mu.Unlock()
	return r.AccessToken, nil
}

type deviceResp struct {
	ID          string `json:"id"`
	Pubkey      string `json:"pubkey"`
	IPv4Address string `json:"ipv4_address"`
}

// remoteDevices lists the keys registered on the account.
func (p *Provider) remoteDevices(ctx context.Context) ([]deviceResp, error) {
	var out []deviceResp
	err := p.do(ctx, http.MethodGet, "/accounts/v1/devices", true, nil, &out, http.StatusOK)
	return out, err
}

// reconcile matches our stored keys with what Mullvad lists: a key we sent but never
// recorded (a crash between the two) is adopted, a key removed from the account (at
// mullvad.net or by another app) is forgotten so it is registered again when needed.
// It returns how many of the account's devices are not ours.
func (p *Provider) reconcile(remote []deviceResp) (others int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	byKey := map[string]deviceResp{}
	for _, r := range remote {
		byKey[r.Pubkey] = r
	}
	ours := 0
	var keep []device
	for _, d := range p.st.Devices {
		r, ok := byKey[d.Public]
		switch {
		case ok:
			d.ID, d.IPv4 = r.ID, r.IPv4Address
			ours++
			keep = append(keep, d)
		case d.ID == "": // never registered: still ours to register
			keep = append(keep, d)
		}
	}
	p.st.Devices = keep
	_ = p.saveLocked()
	return len(remote) - ours
}

// IdentityAt returns the key for slot, generating and registering it on first use.
func (p *Provider) IdentityAt(ctx context.Context, slot int) (*vpnprovider.Key, error) {
	if slot < 0 || slot >= MaxDevices {
		return nil, errors.New("no such key slot")
	}
	p.regMu.Lock()
	defer p.regMu.Unlock()
	if !p.verified.Load() {
		// Once per run: drop keys that were removed from the account since (a tunnel on
		// such a key would just never handshake).
		if remote, err := p.remoteDevices(ctx); err == nil {
			p.reconcile(remote)
			p.verified.Store(true)
		} else if p.SignedIn() {
			return nil, err
		}
	}
	p.mu.Lock()
	if p.st.Account == "" {
		p.mu.Unlock()
		return nil, errors.New("not signed in to Mullvad: sign in in mfetchy's Settings")
	}
	idx := -1
	for i, d := range p.st.Devices {
		if d.Slot == slot {
			idx = i
		}
	}
	if idx < 0 {
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		p.st.Devices = append(p.st.Devices, device{Slot: slot,
			Private: base64.StdEncoding.EncodeToString(priv.Bytes()),
			Public:  base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())})
		idx = len(p.st.Devices) - 1
		// saved before registering: a crash in between leaves a key reconcile can adopt,
		// never one that is registered and lost
		_ = p.saveLocked()
	}
	d := p.st.Devices[idx]
	p.mu.Unlock()

	if d.IPv4 == "" {
		var r deviceResp
		err := p.do(ctx, http.MethodPost, "/accounts/v1/devices", true,
			map[string]any{"pubkey": d.Public, "hijack_dns": false}, &r, http.StatusCreated)
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) && ae.Code == "PUBKEY_IN_USE" {
				// registered earlier; the next server-list refresh adopts it
				return nil, errors.New("Mullvad already has this key; try again")
			}
			return nil, err
		}
		d.ID, d.IPv4 = r.ID, r.IPv4Address
		p.mu.Lock()
		for i := range p.st.Devices {
			if p.st.Devices[i].Slot == slot {
				p.st.Devices[i] = d
			}
		}
		err = p.saveLocked()
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return keyOf(d)
}

func keyOf(d device) (*vpnprovider.Key, error) {
	priv, err := base64.StdEncoding.DecodeString(d.Private)
	if err != nil || len(priv) != 32 {
		return nil, errors.New("the saved Mullvad key is damaged: sign out and in again in mfetchy's Settings")
	}
	pfx, err := netip.ParsePrefix(d.IPv4)
	if err != nil {
		return nil, errors.New("Mullvad sent no tunnel address for this key")
	}
	k := &vpnprovider.Key{Address: []netip.Addr{pfx.Addr()}}
	copy(k.Private[:], priv)
	return k, nil
}

// Tunnel builds the connection to s with k.
func (p *Provider) Tunnel(s vpnprovider.Server, k *vpnprovider.Key) (vpnprovider.TunnelConfig, error) {
	pub, err := base64.StdEncoding.DecodeString(s.PublicKey)
	if err != nil || len(pub) != 32 {
		return vpnprovider.TunnelConfig{}, errors.New("bad server key")
	}
	if len(k.Address) == 0 {
		return vpnprovider.TunnelConfig{}, errors.New("this key has no tunnel address")
	}
	port := s.Port
	if port == 0 {
		port = 51820
	}
	cfg := vpnprovider.TunnelConfig{
		Endpoint:   s.IP + ":" + strconv.Itoa(port),
		PrivateKey: k.Private,
		Addresses:  k.Address,
		DNS:        []netip.Addr{netip.MustParseAddr(dnsAddr)},
	}
	copy(cfg.PeerKey[:], pub)
	return cfg, nil
}

// Location asks Mullvad where this connection is (a single request to am.i.mullvad.net,
// which only sees the address mfetchy is about to hide).
func (p *Provider) Location(ctx context.Context) (vpnprovider.Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Meta+"/json", nil)
	if err != nil {
		return vpnprovider.Location{}, err
	}
	req.Header.Set("User-Agent", "mfetchy")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return vpnprovider.Location{}, err
	}
	defer resp.Body.Close()
	var r struct {
		Country   string  `json:"country"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r) != nil {
		return vpnprovider.Location{}, vpnprovider.ErrNoLocation
	}
	return vpnprovider.Location{Lat: r.Latitude, Long: r.Longitude, Country: r.Country}, nil
}

type relayList struct {
	Locations map[string]struct {
		Country   string  `json:"country"`
		City      string  `json:"city"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"locations"`
	WireGuard struct {
		Relays []struct {
			Hostname  string `json:"hostname"`
			Location  string `json:"location"`
			Active    bool   `json:"active"`
			IPv4      string `json:"ipv4_addr_in"`
			PublicKey string `json:"public_key"`
		} `json:"relays"`
	} `json:"wireguard"`
}

// ParseRelays turns the public relay list into servers: active relays only.
func ParseRelays(raw []byte) ([]vpnprovider.Server, error) {
	var rl relayList
	if err := json.Unmarshal(raw, &rl); err != nil {
		return nil, fmt.Errorf("unreadable Mullvad relay list: %w", err)
	}
	var out []vpnprovider.Server
	for _, r := range rl.WireGuard.Relays {
		if !r.Active || r.IPv4 == "" || r.PublicKey == "" || r.Hostname == "" {
			continue
		}
		loc := rl.Locations[r.Location]
		cc, _, _ := strings.Cut(r.Location, "-")
		out = append(out, vpnprovider.Server{
			Name: r.Hostname, Country: strings.ToUpper(cc), City: loc.City,
			IP: r.IPv4, Port: 51820, PublicKey: r.PublicKey, Status: 1,
			Lat: loc.Latitude, Long: loc.Longitude,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("Mullvad's relay list has no usable WireGuard servers")
	}
	return out, nil
}

// ServersInfo returns Mullvad's WireGuard relays and how many tunnels this account can
// still open: 5 devices, minus those other apps already hold.
func (p *Provider) ServersInfo(ctx context.Context) (vpnprovider.ServerList, error) {
	var out vpnprovider.ServerList
	remote, err := p.remoteDevices(ctx)
	if err != nil {
		return out, err
	}
	others := p.reconcile(remote)
	out.MaxConnect = MaxDevices - others
	if out.MaxConnect < 1 {
		return out, fmt.Errorf("all %d device slots on your Mullvad account are used by other devices; remove one at mullvad.net/account", MaxDevices)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Base+"/public/relays/wireguard/v2", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("User-Agent", "mfetchy")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return out, scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return out, &APIError{Status: resp.StatusCode}
	}
	out.Servers, err = ParseRelays(raw)
	return out, err
}
