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

// newKeyRejected is newAZ with the given per-network rejected-key budget and
// key-check ceiling, and generous per-IP and code limits.
func newKeyRejected(t *testing.T, rejected, keyCheck config.Limit) (*azEnv, string) {
	t.Helper()
	e := newAZ(t)
	e.srv.cfg.OAuth.RateLimit = config.RateLimit{
		Authorize:       config.Limit{PerMinute: 6000, Burst: 1000},
		AuthorizeGlobal: config.Limit{PerMinute: 6000, Burst: 1000},
		KeyCheckGlobal:  keyCheck,
		KeyRejected:     rejected,
	}
	e.srv.limits = newLimiters(e.srv.cfg.OAuth.RateLimit, e.srv.cfg.OAuth.CodeTTL, func() time.Time { return e.srv.now() })
	e.mux = http.NewServeMux()
	e.srv.RegisterRoutes(e.mux)
	return e, hidden(t, e.get(e.query(nil)).Body.String())
}

// Review item 4: junk keys from one /24 (IPv4) or /48 (IPv6) network spend
// that network's budget only. Once it is empty the network gets 429 with
// Retry-After and the "try again" form, without a Garmin call; other networks
// are unaffected.
func TestKeyRejectedBudgetPerNetwork(t *testing.T) {
	for _, tc := range []struct {
		name          string
		same, sibling func(i int) string // addresses in one network, and in another
	}{
		{"ipv4 /24",
			func(i int) string { return fmt.Sprintf("198.51.100.%d:1", i+1) },
			func(i int) string { return fmt.Sprintf("198.51.101.%d:1", i+1) }},
		{"ipv6 /48", // a distinct /64 and /56 every time
			func(i int) string { return fmt.Sprintf("[2001:db8:1:%x00::1]:1", i+1) },
			func(i int) string { return fmt.Sprintf("[2001:db8:2:%x00::1]:1", i+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, sealed := newKeyRejected(t, config.Limit{PerMinute: 1, Burst: 3}, config.Limit{PerMinute: 6000, Burst: 1000})
			e.val.err = garmin.ErrKeyRejected
			for i := range 3 {
				if rec := e.postFrom(tc.same(i), goodForm(sealed)); rec.Code != http.StatusBadRequest {
					t.Fatalf("rejection %d = %d, want 400", i, rec.Code)
				}
			}
			rec := e.postFrom(tc.same(9), goodForm(sealed))
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" ||
				!strings.Contains(rec.Body.String(), "Please wait a moment and try again") || hidden(t, rec.Body.String()) != sealed {
				t.Fatalf("network out of budget = %d %v\n%s", rec.Code, rec.Header(), rec.Body)
			}
			if n := len(e.val.got()); n != 3 {
				t.Fatalf("Garmin calls = %d, want 3", n)
			}
			for i := range 3 {
				if rec := e.postFrom(tc.sibling(i), goodForm(sealed)); rec.Code != http.StatusBadRequest {
					t.Fatalf("other network, rejection %d = %d, want 400", i, rec.Code)
				}
			}
			// The budget refills.
			e.srv.now = func() time.Time { return fixedNow.Add(time.Minute) }
			if rec := e.postFrom(tc.same(0), goodForm(sealed)); rec.Code != http.StatusBadRequest {
				t.Fatalf("after a refill = %d, want 400", rec.Code)
			}
		})
	}
}

// Only rejected keys spend the budget: accepted keys and checks without a
// verdict (Garmin down) give their charge back. The accepted trade-off: an
// honest user sharing a network whose budget junk keys emptied waits too.
func TestKeyRejectedBudgetSpentOnlyByRejections(t *testing.T) {
	e, sealed := newKeyRejected(t, config.Limit{PerMinute: 1, Burst: 1}, config.Limit{PerMinute: 6000, Burst: 1000})
	const ip = "192.0.2.7:1"
	for i := range 5 {
		if rec := e.postFrom(ip, goodForm(sealed)); rec.Code != http.StatusSeeOther {
			t.Fatalf("accepted key %d = %d, want 303", i, rec.Code)
		}
	}
	e.val.err = &garmin.StatusError{Status: 500}
	for i := range 5 {
		if rec := e.postFrom(ip, goodForm(sealed)); rec.Code != http.StatusBadGateway {
			t.Fatalf("no verdict %d = %d, want 502", i, rec.Code)
		}
	}
	e.val.err = garmin.ErrKeyRejected
	if rec := e.postFrom(ip, goodForm(sealed)); rec.Code != http.StatusBadRequest {
		t.Fatalf("first rejection = %d, want 400", rec.Code)
	}
	calls := len(e.val.got())
	e.val.err = nil
	if rec := e.postFrom("192.0.2.8:1", goodForm(sealed)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("good key from the drained network = %d, want 429", rec.Code)
	}
	if n := len(e.val.got()); n != calls {
		t.Fatalf("Garmin contacted %d times by a drained network", n-calls)
	}
}

// A network out of budget does not spend the global key-check ceiling, so
// users elsewhere can still sign in; and a check the ceiling refuses gives
// the network its charge back.
func TestKeyRejectedBudgetSparesKeyCheckCeiling(t *testing.T) {
	e, sealed := newKeyRejected(t, config.Limit{PerMinute: 1, Burst: 2}, config.Limit{PerMinute: 60, Burst: 3})
	e.val.err = garmin.ErrKeyRejected
	for i := range 20 { // the attacker's network: 2 checks, then 429s
		want := http.StatusTooManyRequests
		if i < 2 {
			want = http.StatusBadRequest
		}
		if rec := e.postFrom(fmt.Sprintf("203.0.113.%d:1", i+1), goodForm(sealed)); rec.Code != want {
			t.Fatalf("junk key %d = %d, want %d", i, rec.Code, want)
		}
	}
	e.val.err = nil
	if rec := e.postFrom("192.0.2.1:1", goodForm(sealed)); rec.Code != http.StatusSeeOther {
		t.Fatalf("user elsewhere = %d, want 303", rec.Code)
	}

	// The ceiling is now empty (2 junk checks + 1 accepted).
	e.val.err = garmin.ErrKeyRejected
	if rec := e.postFrom("198.51.100.1:1", goodForm(sealed)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("ceiling empty = %d, want 429", rec.Code)
	}
	// One second refills the ceiling but not the network (1 per minute): the
	// network's two checks are both still there, so its charge was given back.
	e.srv.now = func() time.Time { return fixedNow.Add(2 * time.Second) }
	for i := range 2 {
		if rec := e.postFrom("198.51.100.1:1", goodForm(sealed)); rec.Code != http.StatusBadRequest {
			t.Fatalf("check %d after the ceiling refilled = %d, want 400", i, rec.Code)
		}
	}
}

func TestNetworkKey(t *testing.T) {
	for ip, want := range map[string]string{
		"192.0.2.77":         "192.0.2.0/24",
		"192.0.2.1":          "192.0.2.0/24",
		"::ffff:192.0.2.77":  "192.0.2.0/24",
		"2001:db8:1:2:3::1":  "2001:db8:1::/48",
		"2001:db8:1:ffff::9": "2001:db8:1::/48",
		"fe80::1%eth0":       "fe80::/48",
		"not-an-ip":          "not-an-ip",
	} {
		if got := networkKey(ip); got != want {
			t.Errorf("networkKey(%q) = %q, want %q", ip, got, want)
		}
	}
}

// The default budget is 5 per minute with a burst of 10, and it is off
// together with the other OAuth limits.
func TestKeyRejectedBudgetDefaults(t *testing.T) {
	m := newLimiters(config.RateLimit{}, 0, time.Now)
	if m[keyRejectedKey] == nil {
		t.Fatal("no default rejected-key budget")
	}
	allowed := 0
	for range 50 {
		if ok, _ := m[keyRejectedKey].Allow("192.0.2.0/24"); ok {
			allowed++
		}
	}
	if allowed != config.DefaultKeyRejectedBurst {
		t.Errorf("default burst = %d, want %d", allowed, config.DefaultKeyRejectedBurst)
	}
	if newLimiters(config.RateLimit{Disabled: true}, 0, time.Now) != nil {
		t.Error("rate_limit.disabled must turn the rejected-key budget off too")
	}
}
