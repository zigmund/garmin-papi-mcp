package garmin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	maxCacheEntries = 10000
	refreshSkew     = 60 * time.Second
	fallbackTTL     = 23 * time.Hour
)

// hashKey returns the cache key for an API key; the raw key is never stored.
func hashKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

type cacheEntry struct {
	jwt    string
	exp    time.Time
	stored time.Time
}

type flight struct {
	done chan struct{}
	jwt  string
	err  error
	// leaderCtxDone is set when the leader failed while its own context was
	// done; only then may followers retry instead of sharing the error.
	leaderCtxDone bool
}

// tokenCache is a bounded JWT cache with per-key single-flight exchange.
type tokenCache struct {
	mu       sync.Mutex
	max      int
	entries  map[string]cacheEntry
	inflight map[string]*flight
}

func newTokenCache(max int) *tokenCache {
	return &tokenCache{
		max:      max,
		entries:  make(map[string]cacheEntry),
		inflight: make(map[string]*flight),
	}
}

// put stores an entry, evicting expired entries first and then the oldest
// ones when the bound is exceeded. Caller holds no lock.
func (c *tokenCache) put(key, jwt string, exp, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, jwt, exp, now)
}

func (c *tokenCache) putLocked(key, jwt string, exp, now time.Time) {
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !e.exp.After(now) {
				delete(c.entries, k)
			}
		}
		for len(c.entries) >= c.max {
			var oldestK string
			var oldest time.Time
			first := true
			for k, e := range c.entries {
				if first || e.stored.Before(oldest) {
					oldestK, oldest, first = k, e.stored, false
				}
			}
			delete(c.entries, oldestK)
		}
	}
	c.entries[key] = cacheEntry{jwt: jwt, exp: exp, stored: now}
}

// invalidate drops the entry for key if it still holds the given jwt.
func (c *tokenCache) invalidate(key, jwt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && e.jwt == jwt {
		delete(c.entries, key)
	}
}

// get returns a valid cached JWT or runs exchange once per key, however many
// callers ask concurrently. force skips the cached value.
func (c *tokenCache) get(ctx context.Context, key string, force bool, now func() time.Time,
	exchange func(context.Context) (string, error)) (string, error) {
	for {
		c.mu.Lock()
		if !force {
			if e, ok := c.entries[key]; ok && now().Add(refreshSkew).Before(e.exp) {
				c.mu.Unlock()
				return e.jwt, nil
			}
		}
		if f, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if f.err == nil {
				return f.jwt, nil
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// The leader failed only because its own context ended; retry.
			if f.leaderCtxDone {
				force = false
				continue
			}
			return "", f.err
		}
		f := &flight{done: make(chan struct{})}
		c.inflight[key] = f
		c.mu.Unlock()

		return c.lead(ctx, key, f, now, exchange)
	}
}

// lead runs the exchange as the flight leader and always releases the flight,
// even if exchange panics (the panic is re-raised after release).
func (c *tokenCache) lead(ctx context.Context, key string, f *flight, now func() time.Time,
	exchange func(context.Context) (string, error)) (jwt string, err error) {
	finished := false
	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		if !finished {
			f.err = errExchangeAborted
		} else {
			f.jwt, f.err = jwt, err
			f.leaderCtxDone = err != nil && ctx.Err() != nil
			if err == nil {
				t := now()
				c.putLocked(key, jwt, jwtExpiry(jwt, t), t)
			}
		}
		c.mu.Unlock()
		close(f.done)
	}()
	jwt, err = exchange(ctx)
	finished = true
	return jwt, err
}

// jwtExpiry reads the exp claim from the JWT payload, falling back to now+23h.
func jwtExpiry(jwt string, now time.Time) time.Time {
	fallback := now.Add(fallbackTTL)
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return fallback
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return fallback
	}
	return time.Unix(int64(claims.Exp), 0)
}

var errExchangeAborted = errors.New("garmin: token exchange aborted")
