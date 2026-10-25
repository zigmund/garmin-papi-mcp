package e2e

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// These tests run the stack with the DEFAULT limits (per-token 60/min burst
// 20, 16 concurrent per token), the way real clients meet them.

func TestParallelToolCallsUnderDefaultLimits(t *testing.T) {
	cs := cases()
	bodies := map[string]string{}
	for _, c := range cs {
		for _, p := range c.paths {
			bodies[p] = smallBody(p)
		}
	}
	g := newFakeGarmin(t, bodies)
	g.delay = 50 * time.Millisecond // calls really overlap
	s := newStack(t, g.srv.URL, fakeAPIKey, false)

	const n = 12
	type res struct {
		tool   string
		status int
		out    callOutcome
		err    error
	}
	results := make([]res, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		c := cs[i%len(cs)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, _, out, err := s.callHTTP(c.tool, c.args)
			results[i] = res{c.tool, st, out, err}
		}()
	}
	close(start)
	wg.Wait()
	for _, r := range results {
		switch {
		case r.err != nil:
			t.Errorf("%s: transport error", r.tool)
		case r.status != http.StatusOK || r.out.protoErr:
			t.Errorf("%s: HTTP %d, protocol error %v", r.tool, r.status, r.out.protoErr)
		case r.out.isError:
			t.Errorf("%s: tool error", r.tool)
		}
	}
}

func TestExtrasBeyondConcurrencyGet429WithRetryAfter(t *testing.T) {
	const (
		total = 20
		slots = 16 // default limits.max_concurrent_per_token
	)
	path := "/fitnessage-service/fitnessage/v2/" + day
	g := newFakeGarmin(t, map[string]string{path: smallBody(path)})
	g.gate = make(chan struct{})
	g.entered = make(chan struct{}, total)
	s := newStack(t, g.srv.URL, fakeAPIKey, false)

	type res struct {
		status int
		retry  string
		out    callOutcome
		err    error
	}
	done := make(chan res, total)
	args := `{"date":"` + day + `"}`
	for i := 0; i < total; i++ {
		go func() {
			st, ra, out, err := s.callHTTP("get_fitness_age", args)
			done <- res{st, ra, out, err}
		}()
	}

	timeout := time.After(15 * time.Second)
	// The extras are refused at once, while the admitted calls still block.
	for i := 0; i < total-slots; i++ {
		select {
		case r := <-done:
			if r.err != nil || r.status != http.StatusTooManyRequests {
				close(g.gate)
				t.Fatalf("early answer: status %d err %v, want 429", r.status, r.err)
			}
			if r.retry == "" {
				t.Error("429 without Retry-After")
			}
		case <-timeout:
			close(g.gate)
			t.Fatal("timed out waiting for the 429 answers")
		}
	}
	for i := 0; i < slots; i++ {
		select {
		case <-g.entered:
		case <-timeout:
			close(g.gate)
			t.Fatal("timed out waiting for the admitted calls to reach Garmin")
		}
	}
	close(g.gate)
	for i := 0; i < slots; i++ {
		select {
		case r := <-done:
			if r.err != nil || r.status != http.StatusOK || r.out.protoErr || r.out.isError {
				t.Errorf("admitted call: status %d err %v", r.status, r.err)
			}
		case <-timeout:
			t.Fatal("timed out waiting for the admitted calls to finish")
		}
	}
	if n := g.hitCount(path); n != slots {
		t.Errorf("Garmin saw %d requests, want %d (the extras must not reach it)", n, slots)
	}
	// No slot leaked: a follow-up call is admitted and answered.
	st, _, out, err := s.callHTTP("get_fitness_age", args)
	if err != nil || st != http.StatusOK || out.protoErr || out.isError {
		t.Fatalf("follow-up call: status %d err %v", st, err)
	}
}
