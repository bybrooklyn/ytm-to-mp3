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

	"github.com/bybrooklyn/ytm-to-mp3/internal/vpnprovider"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// WGKey is our WireGuard identity, derived from an Ed25519 key that Proton certifies.
type WGKey = vpnprovider.Key

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

// MaxSlots bounds how many identities ytm keeps (Proton plans allow at most 10 or so
// devices at once).
const MaxSlots = 16

// slotFields returns where slot's key and certificate time live in the session, creating
// the slot if needed. The caller holds c.mu.
func (c *Client) slotFields(slot int) (key *string, until *time.Time) {
	s := c.Session
	if slot == 0 {
		return &s.Key, &s.CertUntil
	}
	for len(s.ExtraKeys) < slot {
		s.ExtraKeys = append(s.ExtraKeys, SlotKey{})
	}
	e := &s.ExtraKeys[slot-1]
	return &e.Key, &e.CertUntil
}

// key returns slot's identity, creating it on first use. The caller holds c.mu.
func (c *Client) key(slot int) (*WGKey, *time.Time, error) {
	keyStr, until := c.slotFields(slot)
	if *keyStr == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		*keyStr = b64(priv.Seed())
		*until = time.Time{}
	}
	seed, err := base64.StdEncoding.DecodeString(*keyStr)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, nil, errors.New("the saved Proton key is damaged: sign out and in again in ytm's Settings")
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	return &WGKey{
		Private: X25519FromSeed(seed),
		PEM:     string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}, until, nil
}

// Identity returns our first WireGuard key; see IdentityAt.
func (c *Client) Identity(ctx context.Context) (*WGKey, error) { return c.IdentityAt(ctx, 0) }

// IdentityAt returns the WireGuard key for slot (one slot per simultaneous tunnel),
// creating it and registering it with Proton when needed, and again when Proton asks us
// to refresh it.
func (c *Client) IdentityAt(ctx context.Context, slot int) (*WGKey, error) {
	if slot < 0 || slot >= MaxSlots {
		return nil, errors.New("no such key slot")
	}
	c.mu.Lock()
	if c.Session == nil {
		c.mu.Unlock()
		return nil, errors.New("not signed in to Proton: sign in in ytm's Settings")
	}
	k, until, err := c.key(slot)
	fresh := err == nil && time.Now().Before(*until)
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
	next := time.Now().Add(3 * 24 * time.Hour)
	if r.RefreshTime > 0 {
		next = time.Unix(r.RefreshTime, 0)
	}
	c.mu.Lock()
	if c.Session != nil { // signed out meanwhile: nothing to record
		_, u := c.slotFields(slot)
		*u = next
	}
	c.mu.Unlock()
	c.save()
	return k, nil
}
