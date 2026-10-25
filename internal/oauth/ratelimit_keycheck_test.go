package oauth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

// newKeyCheck is newAZ with a global key-check ceiling of burst checks
// (refilling one per second) and generous per-IP, per-network and code
// limits, so only the key-check ceiling can refuse.
func newKeyCheck(t *testing.T, burst int) *azEnv {
	t.Helper()
	e := newAZ(t)
	e.srv.cfg.OAuth.RateLimit = config.RateLimit{
		Authorize:       config.Limit{PerMinute: 6000, Burst: 1000},
		KeyRejected:     config.Limit{PerMinute: 6000, Burst: 1000},
		AuthorizeGlobal: config.Limit{PerMinute: 6000, Burst: 1000},
		KeyCheckGlobal:  config.Limit{PerMinute: 60, Burst: burst},
	}
	e.srv.limits = newLimiters(e.srv.cfg.OAuth.RateLimit, e.srv.cfg.OAuth.CodeTTL, func() time.Time { return e.srv.now() })
	e.mux = http.NewServeMux()
	e.srv.RegisterRoutes(e.mux)
	return e
}

// Final-review item 4: every authorize POST that reaches the Garmin key check
// costs a Garmin call whatever the outcome, so all of them together are capped
// by an unkeyed ceiling. Beyond it the form answers 429 with Retry-After and
// the "try again" message, and Garmin is not called.
func TestKeyCheckCeilingBoundsGarminCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"rejected keys", garmin.ErrKeyRejected, http.StatusBadRequest},
		{"no verdict", &garmin.StatusError{Status: 500}, http.StatusBadGateway},
		{"accepted keys", nil, http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeyCheck(t, 3)
			e.val.err = tc.err
			sealed := hidden(t, e.get(e.query(nil)).Body.String())
			for i := range 3 { // distinct sources, none near its own limit
				if rec := e.postFrom(fmt.Sprintf("198.51.100.%d:1", i+1), goodForm(sealed)); rec.Code != tc.want {
					t.Fatalf("check %d = %d, want %d", i, rec.Code, tc.want)
				}
			}
			if n := len(e.val.got()); n != 3 {
				t.Fatalf("Garmin calls = %d, want 3", n)
			}
			rec := e.postFrom("198.51.100.99:1", goodForm(sealed))
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" ||
				!strings.Contains(rec.Body.String(), "Please wait a moment and try again") || rec.Header().Get("Location") != "" {
				t.Fatalf("beyond the ceiling = %d %v\n%s", rec.Code, rec.Header(), rec.Body)
			}
			if n := len(e.val.got()); n != 3 {
				t.Fatalf("Garmin calls = %d after the ceiling, want still 3", n)
			}
			// The ceiling refills.
			e.srv.now = func() time.Time { return fixedNow.Add(time.Second) }
			if rec := e.postFrom("198.51.100.100:1", goodForm(sealed)); rec.Code != tc.want {
				t.Fatalf("after a refill = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// Submissions refused before the key check (missing key, terms, oversized
// key) do not spend the key-check ceiling.
func TestKeyCheckCeilingNotSpentWithoutGarminCall(t *testing.T) {
	e := newKeyCheck(t, 1)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	for i := range 5 {
		form := goodForm(sealed)
		form.Del(keyField)
		if rec := e.postFrom(fmt.Sprintf("203.0.113.%d:1", i+1), form); rec.Code != http.StatusBadRequest {
			t.Fatalf("missing key %d = %d", i, rec.Code)
		}
	}
	if rec := e.postFrom("192.0.2.10:1", goodForm(sealed)); rec.Code != http.StatusSeeOther {
		t.Fatalf("first real check = %d, want 303\n%s", rec.Code, rec.Body)
	}
}

// The default ceiling is 300 per minute with a burst of 100, and it is off
// together with the other OAuth limits.
func TestKeyCheckCeilingDefaults(t *testing.T) {
	m := newLimiters(config.RateLimit{}, 0, time.Now)
	if m[keyCheckKey] == nil {
		t.Fatal("no default key-check ceiling")
	}
	allowed := 0
	for range 150 {
		if ok, _ := m[keyCheckKey].Allow(""); ok {
			allowed++
		}
	}
	if allowed != config.DefaultKeyCheckGlobalBurst {
		t.Errorf("default burst = %d, want %d", allowed, config.DefaultKeyCheckGlobalBurst)
	}
	if newLimiters(config.RateLimit{Disabled: true}, 0, time.Now) != nil {
		t.Error("rate_limit.disabled must turn the key-check ceiling off too")
	}
}
