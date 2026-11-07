package vpn

import (
	"context"
	"errors"
	"sync"
)

// keyRing hands each tunnel its own WireGuard identity. Providers route (Proton) or count
// (Mullvad's device limit) a key per tunnel: tunnels sharing one key knock each other out,
// so with six open most "stall" or can't connect at all. A tunnel holds its slot until it
// is closed.
type keyRing struct {
	mu   sync.Mutex
	get  func(ctx context.Context, slot int) (*Key, error)
	size int
	used []bool
	keys map[int]*Key
}

var errNoKeySlot = errors.New("all WireGuard keys are in use")

func newKeyRing(size, maxSlots int, get func(context.Context, int) (*Key, error)) *keyRing {
	size = min(max(size, 1), max(maxSlots, 1))
	return &keyRing{get: get, size: size, used: make([]bool, size), keys: map[int]*Key{}}
}

// seed records a key already fetched for slot (the one checked at startup).
func (r *keyRing) seed(slot int, k *Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[slot] = k
}

// acquire takes a free slot and returns its key, registering it with the provider the
// first time. release must be called when the tunnel using it is closed (or never came up).
func (r *keyRing) acquire(ctx context.Context) (k *Key, release func(), err error) {
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
