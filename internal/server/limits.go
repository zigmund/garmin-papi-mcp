package server

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/httplog"
	"github.com/zigmund/garmin-papi-mcp/internal/ratelimit"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
)

// limitLogInterval bounds how often a limit being hit is logged above debug.
const limitLogInterval = time.Minute

// grantPurpose is the MAC purpose of the per-grant limiter key.
const grantPurpose = "token"

// Limits enforces config.ProxyLimits on MCP requests. A nil limiter or slot
// pool means that limit is off.
type Limits struct {
	cfg      config.ProxyLimits
	log      *slog.Logger
	now      func() time.Time
	clientIP func(*http.Request) string

	ip       []*ratelimit.Limiter // /32 or /64, /56, /48 tiers; nil when off
	token    *ratelimit.Limiter
	perToken *ratelimit.Concurrency
	global   *ratelimit.Concurrency
}

// newLimits builds the limiters for cfg. Zero values mean the defaults (see
// config.ProxyLimits.Resolved). clientIP resolves the client for the per-IP
// limit; nil means loopback-only trust (httplog.ClientIP).
func newLimits(cfg config.ProxyLimits, log *slog.Logger, clientIP func(*http.Request) string) *Limits {
	if log == nil {
		log = slog.Default()
	}
	if clientIP == nil {
		clientIP = httplog.ClientIP
	}
	l := &Limits{cfg: cfg.Resolved(), log: log, now: time.Now, clientIP: clientIP}
	c := l.cfg
	mk := func(lim config.Limit, scale int) *ratelimit.Limiter {
		return ratelimit.New(lim.PerMinute*scale, lim.Burst*scale, ratelimit.WithClock(func() time.Time { return l.now() }))
	}
	if c.IP.PerMinute > 0 {
		// Same tiers as the OAuth endpoints: an IPv6 client is also charged
		// against its /56 and /48 with proportionally larger budgets.
		l.ip = []*ratelimit.Limiter{mk(c.IP, 1), mk(c.IP, ratelimit.Scale56), mk(c.IP, ratelimit.Scale48)}
	}
	if c.Token.PerMinute > 0 {
		l.token = mk(c.Token, 1)
	}
	if c.MaxConcurrentPerToken > 0 {
		l.perToken = ratelimit.NewConcurrency(c.MaxConcurrentPerToken)
	}
	if c.MaxInFlight > 0 {
		l.global = ratelimit.NewConcurrency(c.MaxInFlight)
	}
	return l
}

// maxBody returns the request body cap in bytes.
func (l *Limits) maxBody() int64 { return int64(l.cfg.MaxRequestBody) }

// chargeIP takes one token per tier for the client IP, before the bearer token
// is opened, so garbage and unauthenticated floods are rejected cheaply. It
// returns the keys to refund if a later check refuses the request or once the
// request has authenticated (see mcpGuard). On denial it writes the 429.
func (l *Limits) chargeIP(w http.ResponseWriter, r *http.Request) (keys []string, ok bool) {
	if l.ip == nil {
		return nil, true
	}
	ip := l.clientIP(r)
	keys = ratelimit.Keys(ip)
	ok, wait := ratelimit.Charge(l.ip, keys)
	if ok {
		return keys, true
	}
	// Client IP only: never the query, headers or body.
	l.log.Debug("mcp rate limit exceeded", "limit", "ip", "remote", ip, "retry_after", wait.String())
	// The denial is counted by the tier that refused it: every tier is
	// reported, so a client rotating through /64s, refused only by its /56
	// or /48, shows up as well.
	for i, lim := range l.ip {
		l.reportDenied(ipTiers[i], lim)
	}
	tooMany(w, wait)
	return nil, false
}

// ipTiers names the per-IP tiers for the log, in the order of l.ip and
// ratelimit.Keys: the address (IPv4) or its /64 (IPv6), then the IPv6 /56
// and /48.
var ipTiers = [...]string{"ip", "ip/56", "ip/48"}

func (l *Limits) refundIP(keys []string) {
	for i := range min(len(l.ip), len(keys)) {
		l.ip[i].Refund(keys[i])
	}
}

// admit runs the checks that need the authenticated grant, in this order:
// per-grant rate, per-grant concurrency, global in-flight cap. Each later
// check that refuses the request gives back what the earlier ones took (the
// IP tokens passed in as ipKeys included), so a refused request costs nothing
// and never holds a slot. On success the returned release must be called when
// the response has finished. On denial admit has written the response. The
// guard calls it before reading the body, so the slots also bound the bodies
// being read at once (see mcpGuard).
func (l *Limits) admit(w http.ResponseWriter, ipKeys []string, grant string) (release func(), ok bool) {
	if l.token != nil {
		if ok, wait := l.token.Allow(grant); !ok {
			l.refundIP(ipKeys)
			l.log.Debug("mcp rate limit exceeded", "limit", "token", "retry_after", wait.String())
			l.reportDenied("token", l.token)
			tooMany(w, wait)
			return nil, false
		}
	}
	undo := func() {
		if l.token != nil {
			l.token.Refund(grant)
		}
		l.refundIP(ipKeys)
	}
	var relTok, relGlobal func()
	if l.perToken != nil {
		var ok bool
		if relTok, ok = l.perToken.Acquire(grant); !ok {
			undo()
			l.log.Debug("mcp concurrency limit exceeded", "limit", "token_concurrency")
			l.reportConcurrencyDenied("token_concurrency", l.perToken)
			tooMany(w, time.Second)
			return nil, false
		}
	}
	if l.global != nil {
		var ok bool
		if relGlobal, ok = l.global.Acquire(""); !ok {
			if relTok != nil {
				relTok()
			}
			undo()
			l.log.Debug("mcp in-flight cap reached", "limit", "max_in_flight")
			l.reportConcurrencyDenied("max_in_flight", l.global)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "server busy", http.StatusServiceUnavailable)
			return nil, false
		}
	}
	return func() {
		if relGlobal != nil {
			relGlobal()
		}
		if relTok != nil {
			relTok()
		}
	}, true
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", retryAfterSeconds(wait))
	http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
}

// reportDenied logs, at most once a minute per limiter and with a count only
// (the clients are not identified), that a rate limit refused requests.
func (l *Limits) reportDenied(name string, lim *ratelimit.Limiter) {
	if n := lim.TakeDenied(l.now(), limitLogInterval); n > 0 {
		l.log.Warn("mcp rate limit refused requests", "limit", name, "denied_since_last_log", n)
	}
}

func (l *Limits) reportConcurrencyDenied(name string, c *ratelimit.Concurrency) {
	if n := c.TakeDenied(l.now(), limitLogInterval); n > 0 {
		l.log.Warn("mcp concurrency limit refused requests", "limit", name, "denied_since_last_log", n)
	}
}

// retryAfterSeconds renders d as whole seconds, rounded up, at least 1.
func retryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}

// grantKey identifies a grant (one Personal API key) for the per-token limits
// without using the key itself: the hex MAC of the key under the sealer's
// current key. It is stable across access token refreshes and independent of
// client_id, so registering more OAuth clients (dynamic client registration
// is open) does not buy a fresh budget for the same key. It is held only in
// memory and never logged.
func grantKey(s *seal.Sealer, apiKey string) string {
	return keyMAC(s, grantPurpose, apiKey)
}
