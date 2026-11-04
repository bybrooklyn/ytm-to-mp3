// Package proton talks to Proton VPN's (undocumented) API: sign in, list servers,
// register a WireGuard key.
//
// The request flow mirrors Proton's own open-source Linux client (python-proton-core and
// python-proton-vpn-api-core): POST /auth/info, then POST /auth with SRP proofs, then
// /auth/2fa if needed, with the same kind of x-pm-appversion / User-Agent headers. The
// connection to vpn-api.proton.me is TLS-pinned to the same public keys those clients use.
package proton

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://vpn-api.proton.me"

// appID is what we tell Proton we are. Proton retires old app versions (error 5003), so
// several known-good identities are tried in turn and the one that works is remembered.
type appID struct{ Version, UA string }

func appIDs() []appID {
	if v := os.Getenv("YTM_APP_VERSION"); v != "" { // escape hatch if Proton retires them all
		ver, _, _ := strings.Cut(v[strings.IndexByte(v, '@')+1:], "+")
		return []appID{{v, "ProtonVPN/" + ver + " (Linux; ubuntu/24.04)"}}
	}
	arch := map[string]string{"amd64": "x86-64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	return []appID{
		// The official clients send linux-vpn-<cli|gui>@<proton-vpn-api-core version>+<arch>
		// (read from the installed client's session_holder.py; api-core 5.8.7 is accepted).
		{"linux-vpn-cli@5.8.7+" + arch, "ProtonVPN/1.1.0 (Linux; ubuntu/24.04)"},
		{"linux-vpn-gui@5.8.7+" + arch, "ProtonVPN/4.19.0 (Linux; Ubuntu)"},
		{"linux-vpn@4.19.0", "ProtonVPN/4.19.0 (Linux; Ubuntu)"},
	}
}

// pins are the SHA-256 SPKI hashes Proton's own clients pin for vpn-api.proton.me (the
// current leaf key plus backups), from python-proton-core's ProdEnvironment.
var pins = map[string]bool{
	"CT56BhOTmj5ZIPgb/xD5mH8rY3BLo/MlhP7oPyJUEDo=": true,
	"35Dx28/uzN3LeltkCBQ8RHK0tlNSa2kCpCRGNp34Gxc=": true,
	"qYIukVc63DEITct8sFT7ebIq5qsWmuscaIKeJx+5J5A=": true,
}

// verifyPin accepts the connection only if some certificate the server presented has a
// pinned public key, on top of the normal certificate-chain verification.
func verifyPin(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	for _, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		if pins[base64.StdEncoding.EncodeToString(sum[:])] {
			return nil
		}
	}
	return errors.New("Proton's TLS certificate doesn't match the keys Proton's apps pin (a proxy or attacker may be intercepting, or Proton rotated its keys). Set YTM_NO_PIN=1 to skip this check at your own risk")
}

func newHTTP() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if os.Getenv("YTM_NO_PIN") == "" {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, VerifyPeerCertificate: verifyPin}
	}
	return &http.Client{Transport: tr, Timeout: 45 * time.Second,
		// never follow a redirect with our credentials/tokens attached
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Session is what we keep after signing in. The password is never stored.
type Session struct {
	Username     string   `json:"username"`
	UID          string   `json:"uid"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	Scopes       []string `json:"scopes"`
	// Key is the base64 Ed25519 seed that backs our WireGuard identity (slot 0).
	Key       string    `json:"key,omitempty"`
	CertUntil time.Time `json:"cert_refresh,omitzero"`
	// ExtraKeys are the identities for slots 1, 2, ...: one per simultaneous tunnel.
	// Proton routes a key's traffic only through its most recent connection, so tunnels
	// sharing a key knock each other out.
	ExtraKeys []SlotKey `json:"extra_keys,omitempty"`
	AppIdx    int       `json:"app_idx,omitempty"` // which appIDs() entry Proton accepted
}

// SlotKey is one extra WireGuard identity and when its certificate needs refreshing.
type SlotKey struct {
	Key       string    `json:"key"`
	CertUntil time.Time `json:"cert_refresh,omitzero"`
}

// Client is an API client bound to a session file.
type Client struct {
	HTTP      *http.Client
	Base      string
	Path      string // where the session is saved ("" = don't save)
	mu        sync.Mutex
	refreshMu sync.Mutex
	Session   *Session
	appIdx    int
	// MaxConnect is how many devices the plan allows at once (0 = unknown); set by Servers.
	MaxConnect int
}

// New returns a client, loading a saved session from path if there is one.
func New(path string) *Client {
	c := &Client{HTTP: newHTTP(), Base: baseURL, Path: path}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var s Session
			if json.Unmarshal(b, &s) == nil && s.UID != "" {
				c.Session = &s
				c.appIdx = s.AppIdx
			}
		}
	}
	return c
}

// SessionPath is the default location of the saved session.
func SessionPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ytm", "proton.json"), nil
}

// save writes the session atomically (temp file + rename) so a crash can't lose the
// WireGuard seed or a rotated refresh token.
func (c *Client) save() {
	if err := c.saveErr(); err != nil {
		fmt.Fprintln(os.Stderr, "ytm: warning: couldn't save your Proton session:", err)
	}
}

func (c *Client) saveErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Path == "" || c.Session == nil {
		return nil
	}
	c.Session.AppIdx = c.appIdx
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.Session, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	_ = os.Remove(tmp) // WriteFile keeps an existing file's looser mode
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

// SignedIn reports whether a session is stored.
func (c *Client) SignedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Session != nil
}

// Revoke ends the session on Proton's side too (best effort), then forgets it locally.
// A 401 means it was already gone, which is fine.
func (c *Client) Revoke(ctx context.Context) {
	if c.SignedIn() {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = c.once(rctx, "DELETE", "/auth", true, nil, nil)
		cancel()
	}
	c.Logout()
}

// Logout forgets the session and key locally.
func (c *Client) Logout() {
	c.mu.Lock()
	c.Session = nil
	c.mu.Unlock()
	if c.Path != "" {
		_ = os.Remove(c.Path)
		_ = os.Remove(c.Path + ".tmp")
	}
}

// longHex matches token-like strings so debug output never echoes session identifiers.
var longHex = regexp.MustCompile(`[A-Za-z0-9_\-]{24,}`)

// APIError is a failure reported by Proton.
type APIError struct {
	Status     int
	Code       int
	Msg        string        // Proton's own message
	RetryAfter time.Duration // from the Retry-After header, if sent
	// Human verification (code 9001): what Proton offers and the token it issued.
	HVMethods []string
	HVToken   string
}

// RateLimited reports whether Proton is telling us to slow down. Retrying makes it worse.
func (e *APIError) RateLimited() bool {
	return !e.BadVersion() && (e.Status == 429 || e.Status == 503 || e.Code == 2028)
}

// BadVersion reports whether Proton rejected the app version we identify as. Proton
// signals this with code 5003/5001/5005, but also sometimes with another code and only
// the message ("This version of the app is no longer supported").
func (e *APIError) BadVersion() bool {
	if e.Code == 5003 || e.Code == 5001 || e.Code == 5005 {
		return true
	}
	m := strings.ToLower(e.Msg)
	return strings.Contains(m, "no longer supported") || strings.Contains(m, "update to continue")
}

// NeedsHuman reports whether Proton wants a CAPTCHA / email / SMS check first.
func (e *APIError) NeedsHuman() bool { return e.Code == 9001 }

func (e *APIError) Error() string {
	var hint string
	switch {
	case e.BadVersion():
		hint = "Proton rejected the app version ytm identifies as (ytm needs an update; or set YTM_APP_VERSION)"
	case e.NeedsHuman():
		hint = "Proton wants a human check (" + strings.Join(e.HVMethods, ", ") + ") before it lets this app sign in. " +
			"Sign in once at https://account.protonvpn.com in a browser on this same network, then run ytm setup again"
	case e.Code == 8002:
		hint = "wrong password or 2FA code"
	case e.Code == 2028:
		hint = "Proton says this account or address is temporarily blocked from signing in"
		if e.RetryAfter > 0 {
			hint += fmt.Sprintf("; try again in about %d minutes", int(e.RetryAfter.Minutes())+1)
		} else {
			hint += "; wait a while (an hour or more) before trying again"
		}
	case e.RateLimited():
		hint = "Proton is rate limiting requests. Stop retrying and wait"
		if e.RetryAfter > 0 {
			hint += fmt.Sprintf(" about %d minutes", int(e.RetryAfter.Minutes())+1)
		} else {
			hint += " a few minutes"
		}
	}
	// always show what Proton actually said, so a wrong guess above can be spotted
	detail := fmt.Sprintf("HTTP %d, code %d", e.Status, e.Code)
	if e.Msg != "" {
		detail = fmt.Sprintf("Proton says %q; %s", e.Msg, detail)
	}
	if hint != "" {
		return hint + " (" + detail + ")"
	}
	return detail
}

// do sends a JSON request. out may be nil. It refreshes an expired session once.
func (c *Client) do(ctx context.Context, method, path string, authed bool, in, out any) error {
	c.mu.Lock()
	stale := ""
	if c.Session != nil {
		stale = c.Session.AccessToken
	}
	c.mu.Unlock()
	err := c.once(ctx, method, path, authed, in, out)
	var ae *APIError
	if authed && errors.As(err, &ae) && ae.Status == 401 && stale != "" {
		if rerr := c.refresh(ctx, stale); rerr != nil {
			return rerr
		}
		return c.once(ctx, method, path, authed, in, out)
	}
	return err
}

// once sends one request. If Proton rejects our app version it moves to the next known
// identity and resends (that rejection happens before anything is processed, so it can't
// count as a sign-in attempt).
func (c *Client) once(ctx context.Context, method, path string, authed bool, in, out any) error {
	ids := appIDs()
	for {
		c.mu.Lock()
		idx := min(c.appIdx, len(ids)-1)
		c.mu.Unlock()
		err := c.send(ctx, ids[idx], method, path, authed, in, out)
		var ae *APIError
		if errors.As(err, &ae) && ae.BadVersion() && idx+1 < len(ids) {
			c.mu.Lock()
			c.appIdx = idx + 1
			c.mu.Unlock()
			continue
		}
		return err
	}
}

func (c *Client) send(ctx context.Context, id appID, method, path string, authed bool, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-pm-appversion", id.Version)
	req.Header.Set("User-Agent", id.UA)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authed {
		c.mu.Lock()
		s := c.Session
		var uid, tok string
		if s != nil {
			uid, tok = s.UID, s.AccessToken
		}
		c.mu.Unlock()
		if s == nil {
			return errors.New("not signed in: run ytm setup")
		}
		req.Header.Set("x-pm-uid", uid)
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var env struct {
		Code    int
		Error   string
		Details struct {
			HumanVerificationMethods []string
			HumanVerificationToken   string
			Methods                  []string
		}
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode == 200 && (env.Code == 1000 || env.Code == 0 || env.Code == 1001) {
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	ae := &APIError{Status: resp.StatusCode, Code: env.Code, Msg: strings.TrimSpace(env.Error),
		HVMethods: append(env.Details.HumanVerificationMethods, env.Details.Methods...), HVToken: env.Details.HumanVerificationToken}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		ae.RetryAfter = time.Duration(s) * time.Second
	}
	if os.Getenv("YTM_DEBUG") != "" {
		// Never print request bodies (they hold SRP proofs / tokens). Replies can carry
		// identifiers, so long token-like strings are masked.
		body := longHex.ReplaceAllString(strings.TrimSpace(string(raw[:min(len(raw), 400)])), "<redacted>")
		fmt.Fprintf(os.Stderr, "ytm debug: %s %s [%s] -> HTTP %d %s\n", method, path, id.Version, resp.StatusCode, body)
	}
	return ae
}

// refresh renews the access token. Refresh tokens rotate, so only one refresh may run at
// a time; if another caller already renewed it (the token differs from the one that got
// the 401) there is nothing to do. The body matches python-proton-core.
func (c *Client) refresh(ctx context.Context, stale string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.Lock()
	s := c.Session
	if s == nil {
		c.mu.Unlock()
		return errors.New("not signed in: run ytm setup")
	}
	if s.AccessToken != stale {
		c.mu.Unlock()
		return nil
	}
	rt := s.RefreshToken
	c.mu.Unlock()

	var r struct {
		AccessToken, RefreshToken string
		Scopes                    []string
	}
	err := c.once(ctx, "POST", "/auth/refresh", true, map[string]string{
		"ResponseType": "token", "GrantType": "refresh_token",
		"RefreshToken": rt, "RedirectURI": "http://protonmail.ch",
	}, &r)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 401 || ae.Status == 422) {
			return errors.New("your Proton session expired; run: ytm setup")
		}
		return fmt.Errorf("couldn't refresh the Proton session (network?): %w", err)
	}
	c.mu.Lock()
	s.AccessToken, s.RefreshToken = r.AccessToken, r.RefreshToken
	if len(r.Scopes) > 0 {
		s.Scopes = r.Scopes
	}
	c.mu.Unlock()
	return c.saveErr()
}
