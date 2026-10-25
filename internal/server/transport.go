package server

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

// oauthReadTimeout bounds how long a client may take to deliver the whole
// request (headers already received) of an OAuth or well-known endpoint. Their
// bodies are small forms or JSON documents, so a slow body is an attack, not a
// use case. It is a variable only so tests can shorten it.
var oauthReadTimeout = 10 * time.Second

// hstsValue is sent when built-in TLS is on: one year, no includeSubDomains
// (the server cannot know what else lives on the domain).
const hstsValue = "max-age=31536000"

// Garmin transport limits. The overall per-request bound is garmin.timeout
// (http.Client.Timeout); these bound connection setup.
const (
	garminDialTimeout  = 10 * time.Second
	garminTLSTimeout   = 10 * time.Second
	garminKeepAlive    = 30 * time.Second
	garminMaxIdleConns = 64
)

// isOAuthPath reports whether p belongs to the authorization server: the
// /oauth tree and the /.well-known discovery documents. The MCP path is
// excluded: a tool call may legitimately take long, so no absolute read
// deadline applies to it (its body is small and read up front).
func isOAuthPath(p string) bool {
	return p == "/oauth" || strings.HasPrefix(p, "/oauth/") || strings.HasPrefix(p, "/.well-known/")
}

// withOAuthReadDeadline sets a per-request read deadline on OAuth and
// well-known requests. A writer that cannot set deadlines (tests, wrappers
// without Unwrap) is tolerated: the request proceeds without one.
func withOAuthReadDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isOAuthPath(r.URL.Path) {
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(oauthReadTimeout))
		}
		next.ServeHTTP(w, r)
	})
}

// withHSTS adds Strict-Transport-Security to every response. The header is
// written at WriteHeader time, replacing (not appending to) any value a
// handler set.
func withHSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&hstsWriter{ResponseWriter: w}, r)
	})
}

type hstsWriter struct {
	http.ResponseWriter
	wrote bool
}

func (h *hstsWriter) WriteHeader(code int) {
	// Informational (1xx) responses other than 101 are followed by the final one.
	if code < 200 && code != http.StatusSwitchingProtocols {
		h.ResponseWriter.WriteHeader(code)
		return
	}
	if !h.wrote {
		h.wrote = true
		h.Header().Set("Strict-Transport-Security", hstsValue)
	}
	h.ResponseWriter.WriteHeader(code)
}

func (h *hstsWriter) Write(b []byte) (int, error) {
	if !h.wrote {
		h.WriteHeader(http.StatusOK)
	}
	return h.ResponseWriter.Write(b)
}

// Flush implements http.Flusher.
func (h *hstsWriter) Flush() {
	if !h.wrote {
		h.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(h.ResponseWriter).Flush()
}

// Unwrap returns the underlying writer for http.ResponseController.
func (h *hstsWriter) Unwrap() http.ResponseWriter { return h.ResponseWriter }

// NewGarminClient builds the Garmin API client for cfg: the transport of
// newGarminHTTPClient and the User-Agent "garmin-papi-mcp/<version>".
func NewGarminClient(cfg *config.Config, version string) *garmin.Client {
	if version == "" {
		version = "dev"
	}
	return garmin.NewClient(cfg.Garmin.BaseURL, newGarminHTTPClient(cfg), garmin.WithUserAgent("garmin-papi-mcp/"+version))
}

// newGarminHTTPClient returns the HTTP client for Garmin calls: one request
// is bounded by garmin.timeout, connections are reused, and MaxConnsPerHost
// follows limits.max_in_flight (0, unlimited, when that cap is off).
//
// The API key and the JWT travel in request headers, so: proxies from the
// environment (HTTP(S)_PROXY) are never used, since they would see them, and
// redirects are never followed, so they cannot be sent anywhere but
// garmin.base_url (a 3xx surfaces as a status error).
func newGarminHTTPClient(cfg *config.Config) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // stdlib default
	t.Proxy = nil
	t.DialContext = (&net.Dialer{Timeout: garminDialTimeout, KeepAlive: garminKeepAlive}).DialContext
	t.TLSHandshakeTimeout = garminTLSTimeout
	t.MaxIdleConnsPerHost = garminMaxIdleConns
	t.MaxConnsPerHost = max(cfg.Limits.MaxInFlight, 0)
	return &http.Client{
		Transport: t,
		Timeout:   cfg.Garmin.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
