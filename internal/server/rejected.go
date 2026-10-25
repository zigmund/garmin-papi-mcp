package server

import (
	"container/list"
	"context"
	"encoding/hex"
	"sync"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/seal"
)

// rejectedPurpose is the MAC purpose of the rejected-keys entries.
const rejectedPurpose = "rejected"

// maxRejectedKeys bounds the rejected-keys set.
const maxRejectedKeys = 10_000

// rejectedKeys remembers, for a while, the API keys Garmin rejected on a tool
// call. While a key is in the set the MCP endpoint answers 401 without asking
// Garmin again, and the refresh grant refuses it (oauth.RejectionSource), so
// the client is sent back to the authorize form instead of reviving a dead
// key. A successful key check on that form removes the key again.
//
// Entries are keyed by an HMAC of the key, never the key itself. The set is
// bounded: expired entries are dropped when seen, and when it is full the
// oldest entry makes room. It is safe for concurrent use.
type rejectedKeys struct {
	sealer *seal.Sealer
	ttl    time.Duration
	max    int
	now    func() time.Time

	mu  sync.Mutex
	m   map[string]*list.Element // value: *rejectedEntry
	lru *list.List               // front = most recently added
}

type rejectedEntry struct {
	mac string
	exp time.Time
}

func newRejectedKeys(s *seal.Sealer, ttl time.Duration, maxEntries int) *rejectedKeys {
	return &rejectedKeys{
		sealer: s, ttl: ttl, max: max(maxEntries, 1), now: time.Now,
		m: make(map[string]*list.Element), lru: list.New(),
	}
}

// keyMAC is the hex HMAC of apiKey for purpose under the sealer's current key.
func keyMAC(s *seal.Sealer, purpose, apiKey string) string {
	return hex.EncodeToString(s.MAC(purpose, []byte(apiKey)))
}

// Add marks apiKey as rejected until the TTL passes.
func (r *rejectedKeys) Add(apiKey string) {
	mac := keyMAC(r.sealer, rejectedPurpose, apiKey)
	exp := r.now().Add(r.ttl)
	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.m[mac]; ok {
		el.Value.(*rejectedEntry).exp = exp //nolint:forcetypeassert // only *rejectedEntry is stored
		r.lru.MoveToFront(el)
		return
	}
	for len(r.m) >= r.max {
		r.remove(r.lru.Back())
	}
	r.m[mac] = r.lru.PushFront(&rejectedEntry{mac: mac, exp: exp})
}

// Rejected reports whether apiKey is marked and the mark has not expired.
func (r *rejectedKeys) Rejected(apiKey string) bool {
	mac := keyMAC(r.sealer, rejectedPurpose, apiKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.m[mac]
	if !ok {
		return false
	}
	if !r.now().Before(el.Value.(*rejectedEntry).exp) { //nolint:forcetypeassert // only *rejectedEntry is stored
		r.remove(el)
		return false
	}
	return true
}

// Forget removes apiKey's mark (Garmin accepted the key again).
func (r *rejectedKeys) Forget(apiKey string) {
	mac := keyMAC(r.sealer, rejectedPurpose, apiKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.m[mac]; ok {
		r.remove(el)
	}
}

// Len returns the number of entries, expired ones not yet dropped included.
func (r *rejectedKeys) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

func (r *rejectedKeys) remove(el *list.Element) {
	delete(r.m, el.Value.(*rejectedEntry).mac) //nolint:forcetypeassert // only *rejectedEntry is stored
	r.lru.Remove(el)
}

// clearingValidator is the authorize form's key check: Garmin's verdict, and
// on success the key's rejected mark is removed, since it works again.
type clearingValidator struct {
	v interface {
		Validate(ctx context.Context, apiKey string) error
	}
	rejected *rejectedKeys
}

func (c clearingValidator) Validate(ctx context.Context, apiKey string) error {
	if err := c.v.Validate(ctx, apiKey); err != nil {
		return err
	}
	c.rejected.Forget(apiKey)
	return nil
}
