package vpn

import (
	"context"
	"errors"
	"sync"

	"github.com/bybrooklyn/ytm-to-mp3/internal/proton"
)

// keyRing hands each tunnel its own WireGuard identity. Proton routes a key's traffic only
// through the connection that handshook most recently, so tunnels sharing one key knock
// each other out: with six open, most "stall" or can't connect at all, and the ones that
// work change from minute to minute. A tunnel holds its slot until it is closed.
type keyRing struct {
	mu   sync.Mutex
	get  func(ctx context.Context, slot int) (*proton.WGKey, error)
	size int
	used []bool
	keys map[int]*proton.WGKey
}

var errNoKeySlot = errors.New("all WireGuard keys are in use")

func newKeyRing(size int, get func(context.Context, int) (*proton.WGKey, error)) *keyRing {
	size = min(max(size, 1), proton.MaxSlots)
	return &keyRing{get: get, size: size, used: make([]bool, size), keys: map[int]*proton.WGKey{}}
}

// seed records a key already fetched for slot (the one checked at startup).
func (r *keyRing) seed(slot int, k *proton.WGKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[slot] = k
}

// acquire takes a free slot and returns its key, registering it with Proton the first
// time. release must be called when the tunnel using it is closed (or never came up).
func (r *keyRing) acquire(ctx context.Context) (k *proton.WGKey, release func(), err error) {
	r.mu.Lock()
	slot := -1
	for i, u := range r.used {
		if !u {
			slot = i
			break
		}
	}
	if slot < 0 {
		r.mu.Unlock()
		return nil, nil, errNoKeySlot
	}
	r.used[slot] = true
	k = r.keys[slot]
	r.mu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			r.mu.Lock()
			r.used[slot] = false
			r.mu.Unlock()
		})
	}
	if k == nil {
		if k, err = r.get(ctx, slot); err != nil {
			release()
			return nil, nil, err
		}
		r.mu.Lock()
		r.keys[slot] = k
		r.mu.Unlock()
	}
	return k, release, nil
}
