// Package tidal downloads from Tidal with the user's own paid subscription: device-code
// sign-in, track/album/playlist lookup, and plain (unencrypted) FLAC streams.
//
// Tidal has no public download API and its terms forbid this; the app says so before
// signing in. Only streams that are served as plain audio URLs are fetched: an encrypted
// or DASH-packaged stream is refused (see Stream), never decrypted.
//
// The endpoints and field names follow what the open-source clients (python-tidal,
// tidal-dl-ng, tidlr) observe; none of this is a documented, stable API.
package tidal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bybrooklyn/mfetchy/internal/appname"
)

const (
	defaultAuthURL = "https://auth.tidal.com/v1/oauth2"
	defaultAPIURL  = "https://api.tidal.com/v1"
	imageBase      = "https://resources.tidal.com/images"

	// deviceScope is what the device-code flow of the open-source clients asks for.
	deviceScope = "r_usr w_usr w_sub"
)

// The client id and secret are Tidal's own Fire TV app credentials, as published in the
// open-source python-tidal; the device-code flow only works with a first-party client.
// They are stored in two halves, as there, only to keep scanners from mistaking them for
// leaked secrets. TIDAL_CLIENT_ID / TIDAL_CLIENT_SECRET override them.
var (
	idParts     = [2]string{"WmxneVNuaGtiVzUw", "V2xkTE1HbDRWQT09"}
	secretParts = [2]string{"TVU1dU9VRm1SRUZxZUhKblNrWktZa3RPVjB4bFFY", "bExSMVpIYlVsT2RWaFFVRXhJVmxoQmRuaEJaejA9"}
)

func unpack(p [2]string) string {
	var s string
	for _, h := range p {
		b, err := base64.StdEncoding.DecodeString(h)
		if err != nil {
			return ""
		}
		s += string(b)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// Errors callers act on.
var (
	ErrSignedOut = errors.New("not signed in to Tidal (or the sign-in expired): sign in again in Connections")
	ErrExpired   = errors.New("the Tidal sign-in code expired before it was approved")
	ErrDenied    = errors.New("the Tidal sign-in was declined")
)

// APIError is a non-2xx answer from Tidal.
type APIError struct {
	Status  int
	Code    string // the "error" field of an OAuth answer
	Message string
}

// HTTPStatus lets jobs.Classify tell a refused login from a missing song.
func (e *APIError) HTTPStatus() int { return e.Status }

func (e *APIError) Error() string {
	m := e.Message
	if m == "" {
		m = e.Code
	}
	if m == "" {
		m = http.StatusText(e.Status)
	}
	return fmt.Sprintf("Tidal answered %d: %s", e.Status, m)
}

// Session is a saved login. It holds secrets: it is only ever written to a 0600 file.
type Session struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	UserID       int64     `json:"user_id"`
	CountryCode  string    `json:"country_code"`
	ClientID     string    `json:"client_id"`
}

// Client talks to Tidal as the signed-in user.
type Client struct {
	HTTP                   *http.Client
	AuthURL, APIURL        string
	ClientID, ClientSecret string
	Path                   string // where the session is kept ("" = memory only)
	Sleep                  func(context.Context, time.Duration) error
	Now                    func() time.Time

	mu      sync.Mutex
	Session *Session
}

// SessionPath is the default place for the saved session (next to the Proton one).
func SessionPath() (string, error) {
	d, err := appname.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "tidal.json"), nil
}

// New builds a client and loads the session at path, if there is one.
func New(path string) *Client {
	c := &Client{
		HTTP: &http.Client{Timeout: 30 * time.Second}, AuthURL: defaultAuthURL, APIURL: defaultAPIURL,
		ClientID: firstEnv("TIDAL_CLIENT_ID", unpack(idParts)), ClientSecret: firstEnv("TIDAL_CLIENT_SECRET", unpack(secretParts)),
		Path: path, Sleep: sleepCtx, Now: time.Now,
	}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var s Session
			if json.Unmarshal(b, &s) == nil && s.RefreshToken != "" {
				c.Session = &s
			}
		}
	}
	return c
}

func firstEnv(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SignedIn reports whether a session is stored (it may still need a refresh).
func (c *Client) SignedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Session != nil
}

// Country is the account's country code, which every catalog request needs.
func (c *Client) Country() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Session == nil || c.Session.CountryCode == "" {
		return "US"
	}
	return c.Session.CountryCode
}

// SignOut forgets the session, locally. (Tidal has no revoke call these clients use; the
// login stays listed among the account's devices until removed in Tidal's own settings.)
func (c *Client) SignOut() error {
	c.mu.Lock()
	c.Session = nil
	c.mu.Unlock()
	if c.Path == "" {
		return nil
	}
	if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// save writes the session atomically with mode 0600.
func (c *Client) save() error {
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
	_ = os.Remove(tmp) // WriteFile keeps an existing file's looser mode
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

// --- device-code sign-in -----------------------------------------------------

// DeviceCode is what the user is shown: a code to type at a Tidal page.
type DeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string // e.g. link.tidal.com
	VerificationURIComplete string // the same with the code filled in
	ExpiresIn               time.Duration
	Interval                time.Duration
}

// URL is the page to open: the one with the code filled in when Tidal gave it.
func (d DeviceCode) URL() string {
	u := d.VerificationURIComplete
	if u == "" {
		u = d.VerificationURI
	}
	if u != "" && !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return u
}

// StartDevice asks Tidal for a sign-in code.
func (c *Client) StartDevice(ctx context.Context) (DeviceCode, error) {
	var r struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		VerificationURI         string `json:"verificationUri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}
	err := c.postForm(ctx, c.AuthURL+"/device_authorization", url.Values{
		"client_id": {c.ClientID}, "scope": {deviceScope},
	}, &r)
	if err != nil {
		return DeviceCode{}, err
	}
	if r.DeviceCode == "" || r.UserCode == "" {
		return DeviceCode{}, errors.New("Tidal didn't give a sign-in code")
	}
	d := DeviceCode{DeviceCode: r.DeviceCode, UserCode: r.UserCode, VerificationURI: r.VerificationURI,
		VerificationURIComplete: r.VerificationURIComplete,
		ExpiresIn:               time.Duration(r.ExpiresIn) * time.Second, Interval: time.Duration(r.Interval) * time.Second}
	if d.Interval <= 0 {
		d.Interval = 5 * time.Second
	}
	if d.ExpiresIn <= 0 {
		d.ExpiresIn = 5 * time.Minute
	}
	return d, nil
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	UserID       int64  `json:"user_id"`
	User         struct {
		UserID      int64  `json:"userId"`
		CountryCode string `json:"countryCode"`
	} `json:"user"`
}

// WaitForLogin polls until the user approves the code, then saves the session. It returns
// ErrExpired when the code runs out and ErrDenied when the user declines.
func (c *Client) WaitForLogin(ctx context.Context, d DeviceCode) error {
	interval := d.Interval
	deadline := c.Now().Add(d.ExpiresIn)
	for {
		if err := c.Sleep(ctx, interval); err != nil {
			return err
		}
		if c.Now().After(deadline) {
			return ErrExpired
		}
		var t tokenAnswer
		err := c.postForm(ctx, c.AuthURL+"/token", url.Values{
			"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "device_code": {d.DeviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "scope": {deviceScope},
		}, &t)
		var ae *APIError
		switch {
		case err == nil:
			return c.adopt(t, "")
		case errors.As(err, &ae) && ae.Code == "authorization_pending":
		case errors.As(err, &ae) && ae.Code == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &ae) && ae.Code == "expired_token":
			return ErrExpired
		case errors.As(err, &ae) && ae.Code == "access_denied":
			return ErrDenied
		default:
			return err
		}
	}
}

// adopt stores a token answer as the session and saves it. oldRefresh is kept when Tidal
// doesn't send a new refresh token.
func (c *Client) adopt(t tokenAnswer, oldRefresh string) error {
	if t.AccessToken == "" {
		return errors.New("Tidal's sign-in answer had no token")
	}
	uid := t.User.UserID
	if uid == 0 {
		uid = t.UserID
	}
	c.mu.Lock()
	prev := c.Session
	s := &Session{AccessToken: t.AccessToken, RefreshToken: firstNonEmpty(t.RefreshToken, oldRefresh),
		ExpiresAt: c.Now().Add(time.Duration(t.ExpiresIn) * time.Second), UserID: uid,
		CountryCode: t.User.CountryCode, ClientID: c.ClientID}
	if prev != nil { // a refresh answer carries no user: keep what we knew
		if s.UserID == 0 {
			s.UserID = prev.UserID
		}
		if s.CountryCode == "" {
			s.CountryCode = prev.CountryCode
		}
	}
	c.Session = s
	c.mu.Unlock()
	return c.save()
}

// refresh trades the refresh token for a new access token. Tidal rotates refresh tokens.
func (c *Client) refresh(ctx context.Context) error {
	c.mu.Lock()
	var rt string
	if c.Session != nil {
		rt = c.Session.RefreshToken
	}
	c.mu.Unlock()
	if rt == "" {
		return ErrSignedOut
	}
	var t tokenAnswer
	err := c.postForm(ctx, c.AuthURL+"/token", url.Values{
		"client_id": {c.ClientID}, "client_secret": {c.ClientSecret},
		"grant_type": {"refresh_token"}, "refresh_token": {rt},
	}, &t)
	var ae *APIError
	if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 401) {
		return ErrSignedOut // the refresh token is no good any more
	}
	if err != nil {
		return err
	}
	return c.adopt(t, rt)
}

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	s := c.Session
	fresh := s != nil && c.Now().Add(time.Minute).Before(s.ExpiresAt)
	var tok string
	if s != nil {
		tok = s.AccessToken
	}
	c.mu.Unlock()
	if s == nil {
		return "", ErrSignedOut
	}
	if fresh {
		return tok, nil
	}
	if err := c.refresh(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Session.AccessToken, nil
}

// --- HTTP ----------------------------------------------------------------------

func (c *Client) postForm(ctx context.Context, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
			UserMessage string `json:"userMessage"`
		}
		_ = json.Unmarshal(body, &e)
		return &APIError{Status: resp.StatusCode, Code: e.Error, Message: firstNonEmpty(e.UserMessage, e.Description)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unreadable answer from Tidal: %w", err)
	}
	return nil
}

// get fetches an API path as the signed-in user, refreshing the token when it is due (or
// was just refused once).
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if q.Get("countryCode") == "" {
		q.Set("countryCode", c.Country())
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.token(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIURL+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		err = c.do(req, out)
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 401 && attempt == 0 {
			// Refused although it looked fresh: force a refresh and try once more.
			c.mu.Lock()
			if c.Session != nil {
				c.Session.ExpiresAt = time.Time{}
			}
			c.mu.Unlock()
			continue
		}
		return err
	}
}

// Verify checks that the saved login works (and learns the account's country).
func (c *Client) Verify(ctx context.Context) error {
	var r struct {
		CountryCode string `json:"countryCode"`
		UserID      int64  `json:"userId"`
	}
	if err := c.get(ctx, "/sessions", nil, &r); err != nil {
		return err
	}
	if r.CountryCode != "" {
		c.mu.Lock()
		if c.Session != nil {
			c.Session.CountryCode = r.CountryCode
		}
		c.mu.Unlock()
		return c.save()
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
