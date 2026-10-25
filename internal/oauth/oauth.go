// Package oauth implements the OAuth 2.1 authorization server endpoints and
// discovery metadata of garmin-papi-mcp. The authorize page asks the user for
// their Garmin Personal API key; the key is sealed into the tokens and never
// stored.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/httplog"
	"github.com/zigmund/garmin-papi-mcp/internal/ratelimit"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
)

// Endpoint paths. public_url is an origin, so these are served at the host root.
const (
	PathAuthorize              = "/oauth/authorize"
	PathToken                  = "/oauth/token"
	PathRegister               = "/oauth/register"
	PathAuthServerMetadata     = "/.well-known/oauth-authorization-server"
	PathOIDCMetadata           = "/.well-known/openid-configuration"
	PathResourceMetadataPrefix = "/.well-known/oauth-protected-resource"
)

// KeyValidator checks a Garmin Personal API key against Garmin. It must return
// an error matching garmin.ErrKeyRejected when Garmin refuses the key; any
// other error means no verdict. *garmin.Client implements it.
type KeyValidator interface {
	Validate(ctx context.Context, apiKey string) error
}

// RejectionSource reports API keys that Garmin rejected after they were
// sealed into tokens (the MCP endpoint learns this on a tool call). The
// refresh grant refuses such keys so the client re-runs authorization instead
// of reviving a dead key. It must be safe for concurrent use.
type RejectionSource interface {
	Rejected(apiKey string) bool
}

// Option configures New.
type Option func(*Handlers)

// WithRejectionSource makes the refresh grant answer invalid_grant for keys
// that r reports as rejected. A nil r leaves the check off.
func WithRejectionSource(r RejectionSource) Option {
	return func(h *Handlers) {
		if r != nil {
			h.rejected = r
		}
	}
}

// Handlers hosts all OAuth endpoints.
type Handlers struct {
	cfg       *config.Config
	sealer    *seal.Sealer
	validator KeyValidator
	log       *slog.Logger
	now       func() time.Time // overridable in tests
	// codes and families make authorization codes and refresh tokens
	// single-use (per process, see replayCache and familyCache).
	codes    *replayCache
	families *familyCache
	// clientIP resolves the client address for rate limiting; limits is nil
	// when rate limiting is disabled.
	clientIP func(*http.Request) string
	limits   map[string]*ratelimit.Limiter
	// rejected, when set, lists keys Garmin rejected; see WithRejectionSource.
	rejected RejectionSource
}

// New builds the OAuth handlers. The client address for rate limiting is
// resolved with cfg.TrustedNets, like the access log.
func New(cfg *config.Config, s *seal.Sealer, v KeyValidator, log *slog.Logger, opts ...Option) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	h := &Handlers{
		cfg: cfg, sealer: s, validator: v, log: log, now: time.Now,
		codes: newReplayCache(maxReplayEntries), families: newFamilyCache(maxFamilyEntries),
		clientIP: httplog.NewResolver(cfg.TrustedNets).ClientIP,
	}
	h.limits = newLimiters(cfg.OAuth.RateLimit, cfg.OAuth.CodeTTL, func() time.Time { return h.now() })
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterRoutes mounts the OAuth endpoints and discovery documents on mux.
func (s *Handlers) RegisterRoutes(mux *http.ServeMux) {
	// Static discovery documents are rendered once here, at startup.
	for _, d := range buildMetadata(s.cfg) {
		mux.Handle("GET "+d.path, d) // also matches HEAD
		mux.Handle("OPTIONS "+d.path, preflight("GET, HEAD, OPTIONS"))
	}
	// GET (also matches HEAD) has its own, more generous per-IP limit.
	mux.Handle("GET "+PathAuthorize, s.limitMethod(formKey, http.MethodGet, s.denyAuthorize, http.HandlerFunc(s.handleAuthorize)))
	mux.Handle("POST "+PathAuthorize, s.limit("authorize", s.denyAuthorize, http.HandlerFunc(s.handleAuthorize)))
	mux.Handle("POST "+PathRegister, s.limit("register", s.denyRegister, http.HandlerFunc(s.handleRegister)))
	mux.Handle("OPTIONS "+PathRegister, preflight("POST, OPTIONS"))
	// All methods are routed so that even a 405 carries the token headers.
	mux.Handle(PathToken, s.limit("token", s.denyToken, http.HandlerFunc(s.handleToken)))
}

// OpenAccess opens an access token and returns the Garmin Personal API key
// sealed in it. The error is non-nil for a token that is malformed, tampered
// with, of another kind (a refresh token or code is rejected), sealed under
// other keys or expired. It never includes the token.
func (s *Handlers) OpenAccess(token string) (string, error) {
	if s.sealer == nil {
		return "", errors.New("oauth: no sealer")
	}
	a, err := seal.OpenAs[tokens.Access](s.sealer, seal.KindAccess, token)
	if err != nil {
		return "", fmt.Errorf("oauth: opening access token: %w", err)
	}
	if a.APIKey == "" {
		return "", errors.New("oauth: access token without key")
	}
	return a.APIKey, nil
}
