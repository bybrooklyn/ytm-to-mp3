package proton

import (
	"context"
	"errors"

	srp "github.com/ProtonMail/go-srp"
)

// ErrNeed2FA is returned by Login when a TOTP code is required; call Submit2FA next.
var ErrNeed2FA = errors.New("two-factor code required")

type authInfo struct {
	Modulus         string
	ServerEphemeral string
	Version         int
	Salt            string
	SRPSession      string
	TwoFA           struct {
		Enabled int
		TOTP    int
	} `json:"2FA"`
}

// Login signs in with SRP (the password never leaves this machine). If the account has
// 2FA it returns ErrNeed2FA after storing the half-open session; finish with Submit2FA.
func (c *Client) Login(ctx context.Context, username, password string) error {
	var info authInfo
	if err := c.do(ctx, "POST", "/auth/info", false, map[string]string{"Username": username}, &info); err != nil {
		return err
	}
	pw := []byte(password)
	defer func() { clear(pw) }() // don't leave the password lying around in memory longer than needed
	a, err := srp.NewAuth(info.Version, username, pw, info.Salt, info.Modulus, info.ServerEphemeral)
	if err != nil {
		return err
	}
	pr, err := a.GenerateProofs(2048)
	if err != nil {
		return err
	}
	var r struct {
		UID, AccessToken, RefreshToken string
		ServerProof                    string
		Scopes                         []string
		PasswordMode                   int
		TwoFA                          struct{ Enabled int } `json:"2FA"`
	}
	err = c.do(ctx, "POST", "/auth", false, map[string]any{
		"Username": username, "ClientEphemeral": b64(pr.ClientEphemeral),
		"ClientProof": b64(pr.ClientProof), "SRPSession": info.SRPSession,
	}, &r)
	if err != nil {
		return err
	}
	if b64(pr.ExpectedServerProof) != r.ServerProof {
		return errors.New("Proton's reply failed verification; aborting")
	}
	// (Two-password accounts only use the second password to unlock mail; VPN doesn't need it.)
	c.mu.Lock()
	keep := Session{}
	if c.Session != nil && c.Session.Username == username {
		keep = *c.Session // keep our WireGuard identity across logins
	}
	keep.Username, keep.UID, keep.AccessToken, keep.RefreshToken, keep.Scopes =
		username, r.UID, r.AccessToken, r.RefreshToken, r.Scopes
	c.Session = &keep
	c.mu.Unlock()
	if r.TwoFA.Enabled != 0 {
		if r.TwoFA.Enabled&1 == 0 {
			return errors.New("this account only has a security key (FIDO2) for 2FA, which mfetchy can't use; add an authenticator-app code in Proton account settings")
		}
		return ErrNeed2FA
	}
	return c.saveErr()
}

// Submit2FA completes a login that returned ErrNeed2FA.
func (c *Client) Submit2FA(ctx context.Context, code string) error {
	var r struct{ Scopes []string }
	if err := c.do(ctx, "POST", "/auth/2fa", true, map[string]string{"TwoFactorCode": code}, &r); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == 8002 {
			// Proton "jails" the login after too many wrong codes: it must start over.
			c.Logout()
			return errors.New("Proton reset this sign-in after wrong 2FA codes; sign in again in mfetchy's Settings")
		}
		return err
	}
	c.mu.Lock()
	c.Session.Scopes = r.Scopes
	c.mu.Unlock()
	return c.saveErr()
}
