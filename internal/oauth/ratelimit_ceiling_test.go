package oauth

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

// newCeiling is newAZ with a global authorize ceiling of burst codes
// (refilling one per second) and generous per-IP limits, so only the ceiling
// can refuse. Requests come from the given remote address.
func newCeiling(t *testing.T, burst int) (*azEnv, *bytes.Buffer) {
	t.Helper()
	e := newAZ(t)
	e.srv.cfg.OAuth.RateLimit = config.RateLimit{
		Authorize:       config.Limit{PerMinute: 6000, Burst: 1000},
		AuthorizeGlobal: config.Limit{PerMinute: 60, Burst: burst},
	}
	e.srv.limits = newLimiters(e.srv.cfg.OAuth.RateLimit, e.srv.cfg.OAuth.CodeTTL, func() time.Time { return e.srv.now() })
	var logs bytes.Buffer
	e.srv.log = slog.New(slog.NewTextHandler(&logs, nil))
	return e, &logs
}

func (e *azEnv) postFrom(remote string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, PathAuthorize, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// Garmin rejections, malformed forms and oversized input never yield a code,
// so they must not spend the global ceiling and lock the owner out.
func TestGlobalCeilingNotSpentByUselessSubmissions(t *testing.T) {
	e, _ := newCeiling(t, 3)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	remote := func(i int) string { return fmt.Sprintf("203.0.113.%d:1", i+1) }

	submit := func(i int, mod func(url.Values)) *httptest.ResponseRecorder {
		form := goodForm(sealed)
		mod(form)
		return e.postFrom(remote(i), form)
	}
	i := 0
	e.val.err = garmin.ErrKeyRejected
	for range 10 { // Garmin rejects the key
		i++
		if rec := submit(i, func(url.Values) {}); rec.Code != http.StatusBadRequest {
			t.Fatalf("rejection: %d", rec.Code)
		}
	}
	e.val.err = &garmin.StatusError{Status: 500}
	for range 10 { // Garmin gives no verdict
		i++
		if rec := submit(i, func(url.Values) {}); rec.Code != http.StatusBadGateway {
			t.Fatalf("no verdict: %d", rec.Code)
		}
	}
	e.val.err = nil
	calls := len(e.val.got())
	for range 10 { // missing key, garbage sealed request, over budget
		i++
		if rec := submit(i, func(v url.Values) { v.Del(keyField) }); rec.Code != http.StatusBadRequest {
			t.Fatalf("missing key: %d", rec.Code)
		}
		i++
		if rec := submit(i, func(v url.Values) { v.Set(requestField, "garbage") }); rec.Code != http.StatusBadRequest {
			t.Fatalf("garbage request: %d", rec.Code)
		}
		i++
		if rec := submit(i, func(v url.Values) { v.Set(keyField, strings.Repeat("x", 1<<16)) }); rec.Code == http.StatusSeeOther || rec.Code == http.StatusTooManyRequests {
			t.Fatalf("oversized: %d", rec.Code)
		}
	}
	if n := len(e.val.got()); n != calls {
		t.Fatalf("Garmin contacted %d times for malformed input", n-calls)
	}

	// The ceiling (burst 3) is untouched: the owner's submissions pass, and
	// exactly burst of them.
	for n := range 3 {
		if rec := e.postFrom("192.0.2.10:1", goodForm(sealed)); rec.Code != http.StatusSeeOther {
			t.Fatalf("owner submission %d: %d\n%s", n, rec.Code, rec.Body)
		}
	}
}

// Submissions that do get a code stay bounded by the ceiling: 429 with
// Retry-After and the same error page as the per-IP limit, a warn log, and
// recovery after a refill.
func TestGlobalCeilingBoundsIssuedCodes(t *testing.T) {
	e, logs := newCeiling(t, 5)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	var issued int
	var last *httptest.ResponseRecorder
	for i := range 50 { // many distinct sources, none near its own limit
		last = e.postFrom(fmt.Sprintf("198.51.100.%d:1", i), goodForm(sealed))
		if last.Code == http.StatusSeeOther {
			issued++
		}
	}
	if issued != 5 {
		t.Fatalf("issued = %d, want the global burst 5", issued)
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") != "1" ||
		!strings.Contains(last.Body.String(), "Too many requests") || last.Header().Get("Location") != "" {
		t.Fatalf("late refusal: %d %v\n%s", last.Code, last.Header(), last.Body)
	}
	if n := strings.Count(logs.String(), "global authorize ceiling reached"); n != 1 {
		t.Errorf("warn lines = %d, want 1 (rate limited)\n%s", n, logs.String())
	}
	e.srv.now = func() time.Time { return fixedNow.Add(time.Second) }
	if rec := e.postFrom("198.51.100.200:1", goodForm(sealed)); rec.Code != http.StatusSeeOther {
		t.Errorf("still limited after refill: %d", rec.Code)
	}
	if rec := e.postFrom("198.51.100.201:1", goodForm(sealed)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("refill must give one token only: %d", rec.Code)
	}
}

// A submission is charged against the ceiling once, and a rejected one does not
// refund or double-charge: N valid submissions with a ceiling of N all pass.
func TestGlobalCeilingChargedOncePerCode(t *testing.T) {
	e, _ := newCeiling(t, 4)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	for i := range 4 {
		if rec := e.postFrom(fmt.Sprintf("198.51.100.%d:1", i), goodForm(sealed)); rec.Code != http.StatusSeeOther {
			t.Fatalf("submission %d: %d", i, rec.Code)
		}
	}
	if rec := e.postFrom("198.51.100.99:1", goodForm(sealed)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth: %d, want 429", rec.Code)
	}
}

// Per-IP limits still bound garbage submissions (and the Garmin checks they
// trigger) even though the ceiling no longer counts them.
func TestPerIPLimitStillBoundsUselessSubmissions(t *testing.T) {
	e, _ := newCeiling(t, 100)
	e.val.err = garmin.ErrKeyRejected
	e.srv.cfg.OAuth.RateLimit.Authorize = config.Limit{PerMinute: 60, Burst: 2}
	e.srv.limits = newLimiters(e.srv.cfg.OAuth.RateLimit, e.srv.cfg.OAuth.CodeTTL, func() time.Time { return fixedNow })
	e.mux = http.NewServeMux()
	e.srv.RegisterRoutes(e.mux)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	var codes []int
	for range 4 {
		codes = append(codes, e.postFrom("203.0.113.1:1", goodForm(sealed)).Code)
	}
	if codes[0] != 400 || codes[1] != 400 || codes[2] != 429 || codes[3] != 429 {
		t.Fatalf("codes = %v, want 400 400 429 429", codes)
	}
}

// A valid submission refused by the ceiling keeps its per-IP token spent (no
// refund): once the ceiling refills, the same IP is refused by its own tier.
func TestGlobalCeilingRefusalKeepsPerIPToken(t *testing.T) {
	e, _ := newCeiling(t, 1)
	e.srv.cfg.OAuth.RateLimit.Authorize = config.Limit{PerMinute: 1, Burst: 1}
	now := fixedNow
	e.srv.now = func() time.Time { return now }
	e.srv.limits = newLimiters(e.srv.cfg.OAuth.RateLimit, e.srv.cfg.OAuth.CodeTTL, func() time.Time { return now })
	e.mux = http.NewServeMux()
	e.srv.RegisterRoutes(e.mux)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())

	// Drain the ceiling from another address.
	if rec := e.postFrom("198.51.100.1:1", goodForm(sealed)); rec.Code != http.StatusSeeOther {
		t.Fatalf("drain: %d", rec.Code)
	}
	const ip = "198.51.100.2:1"
	body := func(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Retry-After") }
	rec := e.postFrom(ip, goodForm(sealed))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("ceiling: %d", rec.Code)
	}
	ceilingWait := body(rec)

	// The ceiling refills one token per second; the per-IP tier (1 per minute)
	// has not, because the refused request kept its token spent.
	now = now.Add(2 * time.Second)
	rec = e.postFrom(ip, goodForm(sealed))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("per-IP tier: %d, want 429", rec.Code)
	}
	if got := body(rec); got == ceilingWait || got == "1" {
		t.Errorf("Retry-After = %q, want the per-IP wait (ceiling wait was %q)", got, ceilingWait)
	}
}
