package config

import "fmt"

// Defaults of the MCP endpoint limits. They are modest: each token
// fronts one person's Garmin account, and Garmin rate-limits the API itself.
const (
	DefaultProxyIPPerMinute    = 300
	DefaultProxyIPBurst        = 60
	DefaultProxyTokenPerMinute = 60
	DefaultProxyTokenBurst     = 20
	DefaultMaxConcurrentPerTok = 8
	DefaultMaxInFlight         = 1024
	DefaultMaxRequestBody      = 10 << 20 // bytes

	// MinMaxRequestBody is the smallest accepted max_request_body.
	MinMaxRequestBody = 1 << 10
)

// Disable is the value that switches an individual proxy limit off. Zero or
// unset means the default.
const Disable = -1

// ProxyLimits configures the limits on MCP endpoint requests (not the OAuth
// endpoints; those are oauth.rate_limit).
//
// Zero or unset values mean the defaults. -1 disables the individual limit;
// Disabled switches all of them off (the body cap stays). Other negative
// values are errors.
type ProxyLimits struct {
	Disabled bool `yaml:"disabled"`

	// IP is the per-client-IP token bucket, applied before the token is opened.
	IP Limit `yaml:"ip"`
	// Token is the per-grant token bucket (a grant is one Personal API key,
	// shared by all OAuth clients; refreshing does not change it).
	Token Limit `yaml:"token"`
	// MaxConcurrentPerToken caps in-flight requests per grant, an open SSE
	// stream counting as one until it ends.
	MaxConcurrentPerToken int `yaml:"max_concurrent_per_token"`
	// MaxInFlight caps in-flight requests across the MCP endpoint (and sets
	// Transport.MaxConnsPerHost). Beyond it requests get 503.
	MaxInFlight int `yaml:"max_in_flight"`
	// MaxRequestBody caps the request body in bytes (413 beyond). It cannot be
	// disabled.
	MaxRequestBody int `yaml:"max_request_body"`
}

// Resolved returns l with every zero value replaced by its default. Disabled
// limits are -1 (all of them with Disabled set); MaxRequestBody is never off.
func (l ProxyLimits) Resolved() ProxyLimits {
	fill := func(v *int, def int) {
		switch {
		case l.Disabled && *v >= Disable: // invalid negatives survive for validate
			*v = Disable
		case *v == 0:
			*v = def
		}
	}
	fillLimit := func(lim *Limit, perMinute, burst int) {
		fill(&lim.PerMinute, perMinute)
		if lim.PerMinute == Disable {
			if lim.Burst >= 0 {
				lim.Burst = 0
			}
		} else if lim.Burst == 0 {
			lim.Burst = burst
		}
	}
	fillLimit(&l.IP, DefaultProxyIPPerMinute, DefaultProxyIPBurst)
	fillLimit(&l.Token, DefaultProxyTokenPerMinute, DefaultProxyTokenBurst)
	fill(&l.MaxConcurrentPerToken, DefaultMaxConcurrentPerTok)
	fill(&l.MaxInFlight, DefaultMaxInFlight)
	if l.MaxRequestBody == 0 {
		l.MaxRequestBody = DefaultMaxRequestBody
	}
	return l
}

// validate reports problems with the raw (unresolved) section.
func (l ProxyLimits) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf("limits."+format, a...)) }
	for _, b := range []struct {
		name string
		l    Limit
	}{{"ip", l.IP}, {"token", l.Token}} {
		if b.l.PerMinute < Disable {
			add("%s.per_minute: must be positive, or -1 to disable", b.name)
		}
		if b.l.Burst < 0 {
			add("%s.burst: must not be negative", b.name)
		}
	}
	if l.MaxConcurrentPerToken < Disable {
		add("max_concurrent_per_token: must be positive, or -1 to disable")
	}
	if l.MaxInFlight < Disable {
		add("max_in_flight: must be positive, or -1 to disable")
	}
	if l.MaxRequestBody < 0 {
		add("max_request_body: must not be negative")
	} else if l.MaxRequestBody > 0 && l.MaxRequestBody < MinMaxRequestBody {
		add("max_request_body: must be at least %d bytes", MinMaxRequestBody)
	}
	return errs
}
