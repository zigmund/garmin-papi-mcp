// Package server assembles the HTTP service: the OAuth endpoints, the MCP
// endpoint behind its guard (origin check, rate limits, bearer token, body
// cap) and /healthz, wrapped in the access log, plus the http.Server
// lifecycle (timeouts, graceful shutdown, reload signals).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"github.com/zigmund/garmin-papi-mcp/internal/httplog"
	"github.com/zigmund/garmin-papi-mcp/internal/oauth"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

const (
	// PathHealthz is the liveness endpoint.
	PathHealthz = "/healthz"

	// ReadHeaderTimeout bounds how long a client may take to send request
	// headers (slowloris protection). It does not limit body or response.
	ReadHeaderTimeout = 10 * time.Second

	// IdleTimeout closes keep-alive connections that sit idle. It is above
	// nginx's default upstream keepalive timeout (60s) so nginx, not this
	// server, decides when to drop a reused connection.
	IdleTimeout = 120 * time.Second

	// MaxHeaderBytes caps request headers. A bearer token is at most 16 KiB
	// (seal.MaxTokenSize), so 64 KiB leaves ample headroom for the other
	// headers while staying well below the 1 MiB net/http default.
	MaxHeaderBytes = 64 << 10

	// ShutdownTimeout is how long graceful shutdown waits for in-flight
	// requests; after it the remaining connections are closed forcibly.
	ShutdownTimeout = 15 * time.Second
)

// There is deliberately no ReadTimeout and no WriteTimeout: both are absolute
// deadlines on the whole request/response and would cut slow tool calls (a
// tool may make several Garmin requests, each bounded by garmin.timeout). The
// OAuth and well-known routes get a per-request read deadline instead
// (withOAuthReadDeadline).

// Server is the assembled HTTP service.
type Server struct {
	http            *http.Server
	log             *slog.Logger
	shutdownTimeout time.Duration

	mu      sync.Mutex
	reloads []reloadHook
}

type reloadHook struct {
	name string
	fn   func() error
}

// New builds the handler tree and http.Server for cfg. It does not listen.
// g serves both the key check of the authorize form and the tool calls.
func New(cfg *config.Config, s *seal.Sealer, g *garmin.Client, log *slog.Logger) (*Server, error) {
	return newServer(cfg, s, g, log, tools.All())
}

func newServer(cfg *config.Config, s *seal.Sealer, g *garmin.Client, log *slog.Logger, toolset []tools.Tool) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	h, err := newHandler(cfg, s, g, log, toolset)
	if err != nil {
		return nil, err
	}
	return &Server{
		http: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: ReadHeaderTimeout,
			IdleTimeout:       IdleTimeout,
			MaxHeaderBytes:    MaxHeaderBytes,
			// net/http logs recovered handler panics with their value and
			// stack; the sanitizer keeps only the fact.
			ErrorLog: newErrorLog(log),
		},
		log:             log,
		shutdownTimeout: ShutdownTimeout,
	}, nil
}

// Handler returns the full request handler: GET/HEAD /healthz (unlogged, so
// probes do not flood the debug access log) and everything else behind the
// access log: the OAuth endpoints and discovery documents, and the MCP
// endpoint at config.MCPPath.
func Handler(cfg *config.Config, s *seal.Sealer, g *garmin.Client, log *slog.Logger) (http.Handler, error) {
	return newHandler(cfg, s, g, log, tools.All())
}

func newHandler(cfg *config.Config, s *seal.Sealer, g *garmin.Client, log *slog.Logger, toolset []tools.Tool) (http.Handler, error) {
	switch {
	case cfg == nil:
		return nil, errors.New("server: nil config")
	case s == nil:
		return nil, errors.New("server: nil sealer")
	case g == nil:
		return nil, errors.New("server: nil garmin client")
	}
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	// One resolver for the access log and the MCP rate limiter, so they
	// agree; the OAuth handlers build the same one from cfg.TrustedNets.
	res := httplog.NewResolver(cfg.TrustedNets)
	oa := oauth.New(cfg, s, g, log)
	oa.RegisterRoutes(mux)
	guard := newMCPGuard(cfg, s, oa.OpenAccess, g, toolset, log, res.ClientIP)
	// Exact pattern (no trailing slash): ServeMux then matches only this
	// path; /mcp/ and /mcp/x fall through to 404.
	mux.Handle(config.MCPPath, withChallenge(guard.meta, guard))

	logged := withOAuthReadDeadline(httplog.Middleware(log, mux, httplog.WithResolver(res)))
	var root http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == PathHealthz && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			healthz(w, r)
			return
		}
		// Everything else, including ServeMux redirects for non-clean paths,
		// is access-logged. Other methods on /healthz get the mux 404.
		logged.ServeHTTP(w, r)
	})
	if cfg.TLSEnabled() {
		root = withHSTS(root)
	}
	return root, nil
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok"))
}

// Handler returns the server's root handler (for tests).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// OnReload registers a hook run on every reload request (SIGHUP). Hooks run
// sequentially in registration order; an error is logged and does not stop
// the others. TLS certificate reload plugs in here.
func (s *Server) OnReload(name string, fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloads = append(s.reloads, reloadHook{name: name, fn: fn})
}

// Reload runs the registered reload hooks, or logs that there is nothing to
// reload.
func (s *Server) Reload() {
	s.mu.Lock()
	hooks := append([]reloadHook(nil), s.reloads...)
	s.mu.Unlock()
	if len(hooks) == 0 {
		s.log.Info("reload requested, nothing to reload")
		return
	}
	for _, h := range hooks {
		if err := h.fn(); err != nil {
			s.log.Error("reload failed", "hook", h.name, "error", err)
			continue
		}
		s.log.Info("reloaded", "hook", h.name)
	}
}

// WatchReload calls Reload for every value received on sigs until ctx is done
// or sigs is closed. Run it in its own goroutine.
func (s *Server) WatchReload(ctx context.Context, sigs <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sigs:
			if !ok {
				return
			}
			s.Reload()
		}
	}
}

// EnableTLS makes Serve terminate TLS with cfg (which must provide
// certificates, e.g. via GetCertificate). It must be called before Serve.
// MinVersion defaults to TLS 1.2; ServeTLS adds h2 and http/1.1 to NextProtos.
func (s *Server) EnableTLS(cfg *tls.Config) {
	c := cfg.Clone()
	if c.MinVersion == 0 {
		c.MinVersion = tls.VersionTLS12
	}
	s.http.TLSConfig = c
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully. If in-flight requests outlive the shutdown timeout, remaining
// connections are closed and Serve still returns nil.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() {
		if s.http.TLSConfig != nil {
			errc <- s.http.ServeTLS(ln, "", "") // certificates come from TLSConfig
			return
		}
		errc <- s.http.Serve(ln)
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}

	s.log.Info("shutting down", "timeout", s.shutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(sctx); err != nil {
		s.log.Warn("graceful shutdown timed out, closing connections", "error", err)
		if cerr := s.http.Close(); cerr != nil {
			s.log.Warn("closing server", "error", cerr)
		}
	}
	// Serve returns ErrServerClosed immediately once Shutdown/Close started.
	<-errc
	return nil
}
