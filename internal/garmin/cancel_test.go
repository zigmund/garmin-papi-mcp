package garmin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Unix(1_800_000_000, 0) }

func TestFollowerCancelledLeaderUnaffected(t *testing.T) {
	c := newTokenCache(10)
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	exch := func(ctx context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return "jwt", nil
	}
	leaderRes := make(chan error, 1)
	go func() {
		_, err := c.get(context.Background(), "k", false, fixedNow, exch)
		leaderRes <- err
	}()
	<-started
	fctx, cancel := context.WithCancel(context.Background())
	folRes := make(chan error, 1)
	go func() {
		_, err := c.get(fctx, "k", false, fixedNow, exch)
		folRes <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-folRes; !errors.Is(err, context.Canceled) {
		t.Fatalf("follower err = %v", err)
	}
	close(release)
	if err := <-leaderRes; err != nil {
		t.Fatalf("leader err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestLeaderCancelledFollowerRetries(t *testing.T) {
	c := newTokenCache(10)
	started := make(chan struct{})
	var success, calls atomic.Int32
	leaderExch := func(ctx context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	folExch := func(ctx context.Context) (string, error) {
		calls.Add(1)
		success.Add(1)
		return "jwt", nil
	}
	lctx, cancel := context.WithCancel(context.Background())
	leaderRes := make(chan error, 1)
	go func() {
		_, err := c.get(lctx, "k", false, fixedNow, leaderExch)
		leaderRes <- err
	}()
	<-started
	folRes := make(chan error, 1)
	go func() {
		_, err := c.get(context.Background(), "k", false, fixedNow, folExch)
		folRes <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-leaderRes; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v", err)
	}
	if err := <-folRes; err != nil {
		t.Fatalf("follower err = %v", err)
	}
	if success.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("success=%d calls=%d", success.Load(), calls.Load())
	}
}

func TestClientTimeoutNoRetryStorm(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewClient(srv.URL, &http.Client{Timeout: 200 * time.Millisecond})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 5 * time.Millisecond)
			if _, err := c.Get(context.Background(), "k", "/p", nil); err == nil {
				t.Error("expected error")
			}
		}(i)
	}
	wg.Wait()
	if n := attempts.Load(); n != 1 {
		t.Fatalf("exchange attempts = %d, want 1", n)
	}
}

type panicOnceRT struct {
	n    atomic.Int32
	next http.RoundTripper
}

func (p *panicOnceRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if p.n.Add(1) == 1 {
		panic("boom-secret")
	}
	return p.next.RoundTrip(r)
}

func TestExchangePanicReleasesFlight(t *testing.T) {
	f := newFake(t)
	rt := &panicOnceRT{next: http.DefaultTransport}
	c := NewClient(f.srv.URL, &http.Client{Transport: rt})
	func() {
		defer func() { _ = recover() }()
		_, _ = c.Get(context.Background(), "k", "/p", nil)
	}()
	done := make(chan error, 1)
	go func() {
		_, err := c.Get(context.Background(), "k", "/p", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("flight stuck after panic")
	}
}
