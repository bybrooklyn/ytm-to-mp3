// Package proton talks to Proton VPN's (undocumented) API: sign in, list servers,
// register a WireGuard key. It identifies itself as the official Linux app, which is
// what Proton's own Linux client does and avoids CAPTCHA challenges.
package proton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	baseURL    = "https://vpn-api.proton.me"
	appVersion = "linux-vpn@4.13.1"
	userAgent  = "ProtonVPN/4.13.1 (Linux; Ubuntu)"
)

// Session is what we keep after signing in. The password is never stored.
type Session struct {
	Username     string   `json:"username"`
	UID          string   `json:"uid"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	Scopes       []string `json:"scopes"`
	// Key is the base64 Ed25519 seed that backs our WireGuard identity.
	Key       string    `json:"key,omitempty"`
	CertUntil time.Time `json:"cert_refresh,omitempty"`
	Device    string    `json:"device,omitempty"`
}

// Client is an API client bound to a session file.
type Client struct {
	HTTP      *http.Client
	Base      string
	Path      string // where the session is saved ("" = don't save)
	mu        sync.Mutex
	refreshMu sync.Mutex
	Session   *Session
}

// New returns a client, loading a saved session from path if there is one.
func New(path string) *Client {
	c := &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Base: baseURL, Path: path}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var s Session
			if json.Unmarshal(b, &s) == nil && s.UID != "" {
				c.Session = &s
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
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.Session, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	return os.Rename(tmp, c.Path)
}

// SignedIn reports whether a session is stored.
func (c *Client) SignedIn() bool { return c.Session != nil }

// Logout forgets the session and key.
func (c *Client) Logout() {
	c.mu.Lock()
	c.Session = nil
	c.mu.Unlock()
	if c.Path != "" {
		_ = os.Remove(c.Path)
	}
}

// APIError is a failure reported by Proton.
type APIError struct {
	Status int
	Code   int
	Msg    string
}

func (e *APIError) Error() string {
	switch {
	case e.Code == 5003:
		return "Proton rejected this app version (ytm needs an update)"
	case e.Code == 9001:
		return "Proton wants a human check (CAPTCHA). Sign in once at account.protonvpn.com in a browser, then try again"
	case e.Code == 8002:
		return "wrong password or 2FA code"
	case e.Code == 2028:
		return "too many attempts, wait a few minutes"
	}
	if e.Msg != "" {
		return fmt.Sprintf("%s (code %d)", e.Msg, e.Code)
	}
	return fmt.Sprintf("HTTP %d (code %d)", e.Status, e.Code)
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

func (c *Client) once(ctx context.Context, method, path string, authed bool, in, out any, extraHeaders ...string) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-pm-appversion", appVersion)
	req.Header.Set("x-pm-apiversion", "3")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.protonmail.v1+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(extraHeaders); i += 2 {
		req.Header.Set(extraHeaders[i], extraHeaders[i+1])
	}
	if authed {
		c.mu.Lock()
		s := c.Session
		c.mu.Unlock()
		if s == nil {
			return errors.New("not signed in: run ytm setup")
		}
		req.Header.Set("x-pm-uid", s.UID)
		req.Header.Set("Authorization", "Bearer "+s.AccessToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var env struct {
		Code  int
		Error string
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode != 200 || (env.Code != 1000 && env.Code != 0 && env.Code != 1001) {
		return &APIError{Status: resp.StatusCode, Code: env.Code, Msg: strings.TrimSpace(env.Error)}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// refresh renews the access token. Refresh tokens rotate, so only one refresh may run at
// a time; if another caller already renewed it (the token differs from the one that got
// the 401) there is nothing to do.
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
	uid, rt := s.UID, s.RefreshToken
	c.mu.Unlock()

	var r struct {
		AccessToken, RefreshToken string
	}
	// Only x-pm-uid identifies the session here; the expired bearer token is not sent.
	err := c.once(ctx, "POST", "/auth/refresh", false, map[string]string{
		"UID": uid, "RefreshToken": rt, "ResponseType": "token",
		"GrantType": "refresh_token", "RedirectURI": "http://protonmail.ch", "State": "ytm",
	}, &r, "x-pm-uid", uid)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 401 || ae.Status == 422) {
			return errors.New("your Proton session expired; run: ytm setup")
		}
		return fmt.Errorf("couldn't refresh the Proton session (network?): %w", err)
	}
	c.mu.Lock()
	s.AccessToken, s.RefreshToken = r.AccessToken, r.RefreshToken
	c.mu.Unlock()
	return c.saveErr()
}
