package garmin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const exchangePath = "/personalapitoken-service/personal-api/personal-api-key/token"

type fakeGarmin struct {
	srv        *httptest.Server
	exchanges  atomic.Int32
	dataCalls  atomic.Int32
	mu         sync.Mutex
	tokenFn    func(n int) string                                  // token body for nth exchange (1-based)
	exchStat   int                                                 // exchange status override
	dataFn     func(w http.ResponseWriter, r *http.Request, n int) // data handler
	lastAuth   string
	lastQuery  string
	exchAccept string
	dataAccept string
}

func newFake(t *testing.T) *fakeGarmin {
	t.Helper()
	f := &fakeGarmin{}
	f.tokenFn = func(n int) string { return fmt.Sprintf("tok%d.%s.sig\n", n, "e30") }
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == exchangePath {
			if r.Method != http.MethodPost {
				t.Errorf("exchange method %s", r.Method)
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "ApiKey ") {
				t.Errorf("exchange auth header wrong")
			}
			f.mu.Lock()
			f.exchAccept = r.Header.Get("Accept")
			f.mu.Unlock()
			// Mimic the real endpoint: it serves text/plain, so an Accept that
			// excludes text/plain and */* is answered with 406.
			if acc := r.Header.Get("Accept"); acc != "" && !strings.Contains(acc, "text/plain") && !strings.Contains(acc, "*/*") {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			n := int(f.exchanges.Add(1))
			time.Sleep(5 * time.Millisecond)
			f.mu.Lock()
			st, fn := f.exchStat, f.tokenFn
			f.mu.Unlock()
			if st != 0 {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write([]byte(fn(n)))
			return
		}
		n := int(f.dataCalls.Add(1))
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.lastQuery = r.URL.RawQuery
		f.dataAccept = r.Header.Get("Accept")
		fn := f.dataFn
		f.mu.Unlock()
		fn(w, r, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func jwtWithExp(exp int64) string {
	p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return "hdr." + p + ".sig"
}

func TestExchangeTrimsJWT(t *testing.T) {
	f := newFake(t)
	f.tokenFn = func(int) string { return "abc.def.ghi\n" }
	c := NewClient(f.srv.URL, f.srv.Client())
	q := url.Values{"a": {"1"}}
	resp, err := c.Get(context.Background(), "key-1", "/x/y", q)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastAuth != "Bearer abc.def.ghi" {
		t.Fatalf("auth = %q", f.lastAuth)
	}
	if f.lastQuery != "a=1" || resp.Status != 200 || resp.ContentType != "application/json" || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("resp %+v q=%q", resp, f.lastQuery)
	}
}

func TestJWTCachedUntilExpiry(t *testing.T) {
	f := newFake(t)
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f.tokenFn = func(int) string { return jwtWithExp(now.Add(time.Hour).Unix()) }
	c := NewClient(f.srv.URL, f.srv.Client(), WithClock(clock))
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.Get(ctx, "k", "/p", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.exchanges.Load(); n != 1 {
		t.Fatalf("exchanges = %d", n)
	}
	mu.Lock()
	now = now.Add(time.Hour - 61*time.Second)
	mu.Unlock()
	if _, err := c.Get(ctx, "k", "/p", nil); err != nil {
		t.Fatal(err)
	}
	if n := f.exchanges.Load(); n != 1 {
		t.Fatalf("exchanges = %d before refresh window", n)
	}
	mu.Lock()
	now = now.Add(2 * time.Second) // within 60s of exp
	mu.Unlock()
	if _, err := c.Get(ctx, "k", "/p", nil); err != nil {
		t.Fatal(err)
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d after refresh window", n)
	}
}

func TestJWTExpFallback23h(t *testing.T) {
	f := newFake(t)
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f.tokenFn = func(int) string { return "not-a-jwt" }
	c := NewClient(f.srv.URL, f.srv.Client(), WithClock(clock))
	ctx := context.Background()
	_, _ = c.Get(ctx, "k", "/p", nil)
	mu.Lock()
	now = now.Add(22 * time.Hour)
	mu.Unlock()
	_, _ = c.Get(ctx, "k", "/p", nil)
	if n := f.exchanges.Load(); n != 1 {
		t.Fatalf("exchanges = %d at 22h", n)
	}
	mu.Lock()
	now = now.Add(time.Hour) // 23h total, within 60s of fallback expiry
	mu.Unlock()
	_, _ = c.Get(ctx, "k", "/p", nil)
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d at 23h", n)
	}
}

func TestSingleFlightExchange(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.srv.URL, f.srv.Client())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get(context.Background(), "k", "/p", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.exchanges.Load(); n != 1 {
		t.Fatalf("exchanges = %d", n)
	}
}

func TestRetryOnceOn401(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte("fine"))
	}
	c := NewClient(f.srv.URL, f.srv.Client())
	resp, err := c.Get(context.Background(), "k", "/p", nil)
	if err != nil || string(resp.Body) != "fine" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if f.exchanges.Load() != 2 || f.dataCalls.Load() != 2 {
		t.Fatalf("exch=%d data=%d", f.exchanges.Load(), f.dataCalls.Load())
	}
	if f.lastAuth != "Bearer tok2.e30.sig" {
		t.Fatalf("retry used stale token: %q", f.lastAuth)
	}

}

// A data endpoint that answers 401 even with a freshly exchanged JWT says
// nothing about the key (the exchange that just succeeded proves it works;
// the endpoint may need a scope the key lacks): it is a plain HTTP 401
// status error, never ErrKeyRejected, and the fresh JWT stays cached.
func TestPersistentDataEndpoint401IsStatusError(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/scoped" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte("fine"))
	}
	c := NewClient(f.srv.URL, f.srv.Client())
	_, err := c.Get(context.Background(), "k", "/scoped", nil)
	if errors.Is(err, ErrKeyRejected) {
		t.Fatalf("err = %v, want a status error, not ErrKeyRejected", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want StatusError 401", err)
	}
	if f.exchanges.Load() != 2 || f.dataCalls.Load() != 2 {
		t.Fatalf("exch=%d data=%d, want one retry after one re-exchange", f.exchanges.Load(), f.dataCalls.Load())
	}
	// The JWT from the second exchange is valid and stays cached.
	resp, err := c.Get(context.Background(), "k", "/other", nil)
	if err != nil || string(resp.Body) != "fine" {
		t.Fatalf("other endpoint: resp=%+v err=%v", resp, err)
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d, want the fresh JWT reused", n)
	}
}

// Once a data endpoint has answered 401 to a fresh JWT, later 401s with that
// JWT fail at once: calls to an endpoint the key has no access to do not
// exchange the key again each time, nor drop the JWT the other calls use. A
// JWT that expires is still replaced, and the new one is checked afresh.
func TestScoped401DoesNotReexchange(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		if strings.HasPrefix(r.URL.Path, "/scoped") {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte("fine"))
	}
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	c := NewClient(f.srv.URL, f.srv.Client(), WithClock(clock))
	ctx := context.Background()
	scoped := func(path string) error {
		_, err := c.Get(ctx, "k", path, nil)
		var se *StatusError
		if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
			return fmt.Errorf("%s: err = %v, want StatusError 401", path, err)
		}
		return nil
	}
	if err := scoped("/scoped/1"); err != nil {
		t.Fatal(err)
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d, want the first call to retry once", n)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := scoped(fmt.Sprintf("/scoped/%d", i)); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.Get(ctx, "k", "/other", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d, want none after the first call", n)
	}
	if n := f.dataCalls.Load(); n != 2+20 {
		t.Fatalf("data calls = %d, want no retries", n)
	}
	if f.lastAuth != "Bearer tok2.e30.sig" {
		t.Fatalf("auth = %q, want the scoped JWT kept", f.lastAuth)
	}

	// The JWT expires (no exp claim: 23h fallback) and is replaced; the new
	// one is not scoped, so its first 401 is retried once more.
	mu.Lock()
	now = now.Add(23 * time.Hour)
	mu.Unlock()
	if err := scoped("/scoped/1"); err != nil {
		t.Fatal(err)
	}
	if n := f.exchanges.Load(); n != 4 {
		t.Fatalf("exchanges = %d after expiry, want a refresh and one retry", n)
	}
	if err := scoped("/scoped/1"); err != nil {
		t.Fatal(err)
	}
	if n := f.exchanges.Load(); n != 4 {
		t.Fatalf("exchanges = %d, want the new scoped JWT kept", n)
	}
}

// A 401 on a data call followed by an exchange that Garmin refuses is a
// rejected key.
func TestData401ThenExchange401IsKeyRejected(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		f.mu.Lock()
		f.exchStat = 401 // the key was revoked after the JWT was minted
		f.mu.Unlock()
		w.WriteHeader(401)
	}
	c := NewClient(f.srv.URL, f.srv.Client())
	_, err := c.Get(context.Background(), "k", "/p", nil)
	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("err = %v, want ErrKeyRejected", err)
	}
	if f.exchanges.Load() != 2 || f.dataCalls.Load() != 1 {
		t.Fatalf("exch=%d data=%d", f.exchanges.Load(), f.dataCalls.Load())
	}
}

func TestExchange401IsKeyRejected(t *testing.T) {
	f := newFake(t)
	f.exchStat = 401
	c := NewClient(f.srv.URL, f.srv.Client())
	_, err := c.Get(context.Background(), "k", "/p", nil)
	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("err = %v", err)
	}
	if err := c.Validate(context.Background(), "k"); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("validate err = %v", err)
	}
}

func TestValidateForcesExchange(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.srv.URL, f.srv.Client())
	for i := 0; i < 2; i++ {
		if err := c.Validate(context.Background(), "k"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d", n)
	}
	if _, err := c.Get(context.Background(), "k", "/p", nil); err != nil {
		t.Fatal(err)
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Fatalf("Get after Validate should reuse token, exchanges = %d", n)
	}
}

// retryNow is the client clock of TestRateLimited429HasRetryAfter.
var retryNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func TestRateLimited429HasRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		hdr  string
		want time.Duration
	}{
		{"7", 7 * time.Second}, {"", 0}, {"soon", 0}, {"0", 0}, {"-5", 0},
		// Seconds are capped at one hour; a huge value must not overflow.
		{"3600", time.Hour}, {"7200", time.Hour}, {"1000000000000", time.Hour},
		{"99999999999999999999999", time.Hour},
		// The HTTP-date form, relative to the client's clock, also capped.
		{retryNow.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{retryNow.Add(48 * time.Hour).Format(http.TimeFormat), time.Hour},
		{retryNow.Add(-time.Minute).Format(http.TimeFormat), 0},
	} {
		f := newFake(t)
		f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
			if tc.hdr != "" {
				w.Header().Set("Retry-After", tc.hdr)
			}
			w.WriteHeader(429)
			_, _ = w.Write([]byte("secret-body"))
		}
		c := NewClient(f.srv.URL, f.srv.Client(), WithClock(func() time.Time { return retryNow }))
		_, err := c.Get(context.Background(), "k", "/p", nil)
		var rl *RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != tc.want {
			t.Fatalf("hdr %q: err = %v", tc.hdr, err)
		}
		if strings.Contains(err.Error(), "secret-body") {
			t.Fatal("body leaked")
		}
	}
}

func TestStatus5xxIsStatusError(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("secret-body"))
	}
	c := NewClient(f.srv.URL, f.srv.Client())
	_, err := c.Get(context.Background(), "k", "/p", nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-body") {
		t.Fatal("body leaked")
	}
}

func TestNoContentReturnsStatus204(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(204) }
	c := NewClient(f.srv.URL, f.srv.Client())
	resp, err := c.Get(context.Background(), "k", "/p", nil)
	if err != nil || resp.Status != 204 || len(resp.Body) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestCacheKeyedByHashNotKey(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.srv.URL, f.srv.Client())
	const key = "super-secret-api-key-123"
	if _, err := c.Get(context.Background(), key, "/p", nil); err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprintf("%+v", c); strings.Contains(s, key) {
		t.Fatal("raw key present in client dump")
	}
	if _, ok := c.cache.entries[hashKey(key)]; !ok {
		t.Fatal("entry not keyed by sha256(key)")
	}
	if _, ok := c.cache.entries[key]; ok {
		t.Fatal("raw key used as map key")
	}
}

func TestBodyLimit(t *testing.T) {
	f := newFake(t)
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		_, _ = w.Write(make([]byte, 8<<20+1))
	}
	c := NewClient(f.srv.URL, f.srv.Client())
	if _, err := c.Get(context.Background(), "k", "/p", nil); err == nil {
		t.Fatal("expected error for oversized body")
	}
	f.dataFn = func(w http.ResponseWriter, r *http.Request, n int) {
		_, _ = w.Write(make([]byte, 8<<20))
	}
	resp, err := c.Get(context.Background(), "k", "/p", nil)
	if err != nil || len(resp.Body) != 8<<20 {
		t.Fatalf("exact limit should pass: %v", err)
	}
}

func TestAcceptHeadersPerRequestKind(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.srv.URL, f.srv.Client())
	if err := c.Validate(context.Background(), "k"); err != nil {
		t.Fatalf("validate against 406-mimicking fake: %v", err)
	}
	if _, err := c.Get(context.Background(), "k", "/p", nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exchAccept != "text/plain, */*" {
		t.Fatalf("exchange Accept = %q", f.exchAccept)
	}
	if f.dataAccept != "application/json, */*;q=0.1" {
		t.Fatalf("data Accept = %q", f.dataAccept)
	}
}
