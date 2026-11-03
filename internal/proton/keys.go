package proton

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"time"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// WGKey is our WireGuard identity, derived from an Ed25519 key that Proton certifies.
type WGKey struct {
	Private [32]byte // X25519, clamped
	PEM     string   // Ed25519 public key (SPKI) sent to Proton
}

// X25519FromSeed converts an Ed25519 seed to a clamped X25519 private key (RFC 7748),
// which is what Proton's own client does to make the WireGuard key.
func X25519FromSeed(seed []byte) [32]byte {
	h := sha512.Sum512(seed)
	var k [32]byte
	copy(k[:], h[:32])
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return k
}

// key returns our identity, creating it on first use. The caller holds c.mu.
func (c *Client) key() (*WGKey, error) {
	s := c.Session
	if s.Key == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		s.Key = b64(priv.Seed())
		s.CertUntil = time.Time{}
	}
	seed, err := base64.StdEncoding.DecodeString(s.Key)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the saved Proton key is damaged; run: ytm logout, then ytm setup")
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &WGKey{
		Private: X25519FromSeed(seed),
		PEM:     string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}, nil
}

// Identity returns our WireGuard key, registering it with Proton when needed (and
// again when Proton asks us to refresh it).
func (c *Client) Identity(ctx context.Context) (*WGKey, error) {
	c.mu.Lock()
	if c.Session == nil {
		c.mu.Unlock()
		return nil, errors.New("not signed in: run ytm setup")
	}
	k, err := c.key()
	fresh := err == nil && time.Now().Before(c.Session.CertUntil)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if fresh {
		return k, nil
	}
	var r struct {
		RefreshTime    int64
		ExpirationTime int64
	}
	// Same request shape as Proton's own Linux app: the public key, a 7-day lifetime and
	// the feature flags. (No device name: it would only leak this computer's hostname.)
	err = c.do(ctx, "POST", "/vpn/v1/certificate", true, map[string]any{
		"ClientPublicKey": k.PEM,
		"Duration":        "10080 min",
		"Features":        map[string]any{"SplitTCP": false, "RandomNAT": false, "PortForwarding": false},
	}, &r)
	if err != nil {
		return nil, err
	}
	until := time.Now().Add(3 * 24 * time.Hour)
	if r.RefreshTime > 0 {
		until = time.Unix(r.RefreshTime, 0)
	}
	c.mu.Lock()
	c.Session.CertUntil = until
	c.mu.Unlock()
	c.save()
	return k, nil
}
