package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Review item 8: a denial is counted by the tier that refused it, so the
// warning must read every tier. A client rotating through the /64s of one
// /56 is refused by the /56 tier only, and must show up in the log.
func TestIPDenialsReportedPerTier(t *testing.T) {
	var logs syncBuf
	var ip string
	l := newLimits(limitsConfig(t, "limits: { ip: { per_minute: 1, burst: 1 } }\n"),
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
		func(*http.Request) string { return ip })
	charge := func() int {
		rec := httptest.NewRecorder()
		l.chargeIP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
		return rec.Code
	}

	// Rotating /64s: each is fresh, the shared /56 (burst 4) runs out.
	for i := range 4 {
		ip = fmt.Sprintf("2001:db8:0:%x::1", i)
		if code := charge(); code != http.StatusOK {
			t.Fatalf("/64 number %d = %d, want 200", i, code)
		}
	}
	ip = "2001:db8:0:4::1"
	if code := charge(); code != http.StatusTooManyRequests {
		t.Fatalf("fifth /64 of the /56 = %d, want 429", code)
	}
	if got := logs.String(); !strings.Contains(got, "mcp rate limit refused requests") || !strings.Contains(got, "limit=ip/56") {
		t.Errorf("a /56 denial is not reported:\n%s", got)
	}

	// The finest tier is still reported as "ip".
	ip = "192.0.2.1"
	charge()
	if code := charge(); code != http.StatusTooManyRequests {
		t.Fatalf("second IPv4 request = %d, want 429", code)
	}
	if got := logs.String(); !strings.Contains(got, "limit=ip ") {
		t.Errorf("an address denial is not reported:\n%s", got)
	}
}
