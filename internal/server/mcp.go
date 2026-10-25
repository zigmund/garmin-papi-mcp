package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/httplog"
	"github.com/zigmund/garmin-papi-mcp/internal/mcp"
	"github.com/zigmund/garmin-papi-mcp/internal/oauth"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// keyRejectedDescription describes the 401 the MCP handler answers when Garmin
// rejects the key: the token is fine, but the key in it no longer works, so
// the client must re-run authorization and the user enter a new key.
const keyRejectedDescription = "Garmin rejected the API key"

// mcpGuard protects the MCP endpoint. Order: path and method (free) ->
// Origin -> per-IP rate (before the token is opened, so unauthenticated
// floods are cut cheaply) -> bearer token -> body size and encoding ->
// per-grant rate -> per-grant concurrency -> global in-flight cap -> body read
// -> MCP handler. A refusal gives back the rate tokens taken before it.
type mcpGuard struct {
	origin string // public_url, the only accepted Origin
	meta   string // resource_metadata URL advertised in challenges
	open   func(token string) (apiKey string, err error)
	sealer *seal.Sealer
	limits *Limits
	known  map[string]bool // registered tool names, for the request log
	next   http.Handler
	log    *slog.Logger
}

func newMCPGuard(cfg *config.Config, s *seal.Sealer, open func(string) (string, error), g tools.Getter,
	toolset []tools.Tool, log *slog.Logger, clientIP func(*http.Request) string,
) *mcpGuard {
	safe := safeTools(toolset, log)
	known := make(map[string]bool, len(safe))
	for _, t := range safe {
		known[t.Name] = true
	}
	return &mcpGuard{
		origin: cfg.PublicURL,
		meta:   cfg.PublicURL + oauth.PathResourceMetadataPrefix + config.MCPPath,
		open:   open,
		sealer: s,
		limits: newLimits(cfg.Limits, log, clientIP),
		known:  known,
		next:   recoverMCP(log, mcp.NewHandler(safe, g, apiKeyFromContext, log)),
		log:    log,
	}
}

func (g *mcpGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only the exact path is served, and no encoded variant of it.
	if r.URL.Path != config.MCPPath || (r.URL.RawPath != "" && r.URL.RawPath != config.MCPPath) {
		http.NotFound(w, r)
		return
	}
	// Stateless Streamable HTTP with JSON responses: POST only (no SSE
	// stream to GET, no session to DELETE). Decided before authentication.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// DNS-rebinding protection (MCP Streamable HTTP): a browser page from
	// another origin must not reach the endpoint. Clients that send no
	// Origin (server-side connectors, CLI clients) are unaffected.
	if !g.originAllowed(r.Header.Values("Origin")) {
		g.log.Debug("mcp request with foreign origin refused")
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	ipKeys, ok := g.limits.chargeIP(w, r)
	if !ok {
		return
	}
	apiKey, ok := g.authenticate(w, r)
	if !ok {
		return
	}
	maxBody := g.limits.maxBody()
	if r.ContentLength > maxBody {
		g.limits.refundIP(ipKeys) // a cheap rejection, not abuse of the budget
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if hasContentEncoding(r.Header) {
		g.limits.refundIP(ipKeys)
		http.Error(w, "content-encoding not supported", http.StatusUnsupportedMediaType)
		return
	}
	release, ok := g.limits.admit(w, ipKeys, grantKey(g.sealer, apiKey))
	if !ok {
		return
	}
	defer release()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "reading request body failed", http.StatusBadRequest)
		return
	}
	info := g.peek(body)
	if info.method != "" {
		attrs := []slog.Attr{slog.String("rpc_method", info.method)}
		if info.tool != "" {
			attrs = append(attrs, slog.String("tool", info.tool))
		}
		httplog.Annotate(r.Context(), attrs...)
	}
	ctx := withRPCInfo(context.WithValue(r.Context(), apiKeyCtx{}, apiKey), info)
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	g.next.ServeHTTP(w, r)
}

// originAllowed reports whether the Origin header lines permit the request:
// none at all, or exactly one equal to public_url (scheme, host and port,
// case-insensitive, default port implied).
func (g *mcpGuard) originAllowed(values []string) bool {
	switch len(values) {
	case 0:
		return true
	case 1:
	default:
		return false
	}
	u, err := url.Parse(strings.TrimSpace(values[0]))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	defaultPort := scheme == "http" && u.Port() == "80" || scheme == "https" && u.Port() == "443"
	if u.Port() != "" && !defaultPort {
		host += ":" + u.Port()
	}
	return scheme+"://"+host == g.origin
}

// authenticate opens the bearer token and returns the API key it carries. On
// failure it has written a 401 challenge. Nothing about the token is logged.
func (g *mcpGuard) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, present := bearer(r.Header.Get("Authorization"))
	if !present {
		g.challenge(w, "", "")
		return "", false
	}
	apiKey, err := g.open(raw)
	switch {
	case err == nil:
		return apiKey, true
	case errors.Is(err, seal.ErrExpired):
		g.log.Debug("mcp token rejected", "reason", "expired")
		g.challenge(w, "invalid_token", "token expired")
	case errors.Is(err, seal.ErrPayload):
		// Authentic token with an unexpected payload shape (e.g. after a
		// deploy changed it): a server-side anomaly, but the client's fix is
		// to re-authorize.
		g.log.Error("access token payload mismatch")
		g.challenge(w, "invalid_token", "token invalid")
	case errors.Is(err, seal.ErrInvalid):
		g.log.Debug("mcp token rejected", "reason", "invalid")
		g.challenge(w, "invalid_token", "token invalid")
	default:
		// An authentic access token without a key; the sealer itself was
		// checked at startup. The client can only re-authorize.
		g.log.Warn("mcp token rejected", "reason", "unusable", "error_type", errorType(err))
		g.challenge(w, "invalid_token", "token invalid")
	}
	return "", false
}

// challenge writes a 401 with an RFC 6750 / RFC 9728 WWW-Authenticate header.
// code and desc are fixed strings (never request data), so no escaping needed.
func (g *mcpGuard) challenge(w http.ResponseWriter, code, desc string) {
	w.Header().Set("WWW-Authenticate", challengeValue(g.meta, code, desc))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func challengeValue(meta, code, desc string) string {
	v := "Bearer "
	if code != "" {
		v += fmt.Sprintf("error=%q, error_description=%q, ", code, desc)
	}
	return v + fmt.Sprintf("resource_metadata=%q", meta)
}

// bearer extracts the token from an Authorization header. present is false
// when the header is absent or uses another scheme.
func bearer(h string) (token string, present bool) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	// An empty token yields present=true and fails validation as invalid.
	// Anything but exactly one token (embedded whitespace or control
	// characters, as in "Bearer a b") is treated as empty.
	rest = strings.TrimSpace(rest)
	if strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", true
	}
	return rest, true
}

// hasContentEncoding reports whether the request declares any encoding other
// than identity, in any header line or comma-separated list element.
func hasContentEncoding(hdr http.Header) bool {
	for _, v := range hdr.Values("Content-Encoding") {
		for _, enc := range strings.Split(v, ",") {
			if enc = strings.TrimSpace(enc); enc != "" && !strings.EqualFold(enc, "identity") {
				return true
			}
		}
	}
	return false
}

// withChallenge makes every 401 on the MCP endpoint carry a WWW-Authenticate
// challenge pointing at the protected resource metadata. The guard sets its
// own (missing or invalid token); a 401 without one comes from the MCP
// handler when Garmin rejected the key, so that is the description used.
func withChallenge(meta string, next http.Handler) http.Handler {
	value := challengeValue(meta, "invalid_token", keyRejectedDescription)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&challengeWriter{ResponseWriter: w, value: value}, r)
	})
}

type challengeWriter struct {
	http.ResponseWriter
	value string
}

func (c *challengeWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized && c.Header().Get("WWW-Authenticate") == "" {
		c.Header().Set("WWW-Authenticate", c.value)
	}
	c.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the underlying writer for http.ResponseController.
func (c *challengeWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// apiKeyCtx carries the API key from the guard to the MCP handler's auth
// callback.
type apiKeyCtx struct{}

func apiKeyFromContext(r *http.Request) (string, error) {
	k, _ := r.Context().Value(apiKeyCtx{}).(string)
	if k == "" {
		return "", tools.ErrUnauthorized
	}
	return k, nil
}

// rpcInfo is what the request log and panic reports may say about a JSON-RPC
// message: the method if it is a known MCP method ("other" otherwise) and,
// for tools/call, the tool name if it is registered ("unknown" otherwise).
// Client-chosen strings are never logged verbatim.
type rpcInfo struct {
	method string
	tool   string
}

type rpcInfoCtx struct{}

func withRPCInfo(ctx context.Context, info rpcInfo) context.Context {
	return context.WithValue(ctx, rpcInfoCtx{}, info)
}

func rpcInfoFrom(ctx context.Context) rpcInfo {
	info, _ := ctx.Value(rpcInfoCtx{}).(rpcInfo)
	return info
}

// knownMethods are the MCP methods logged by name.
var knownMethods = map[string]bool{
	"initialize": true, "ping": true, "tools/list": true, "tools/call": true,
	"resources/list": true, "resources/templates/list": true, "resources/read": true,
	"prompts/list": true, "prompts/get": true, "logging/setLevel": true, "completion/complete": true,
	"notifications/initialized": true, "notifications/cancelled": true, "notifications/progress": true,
	"notifications/roots/list_changed": true,
}

// peek extracts rpcInfo from a request body. A malformed body yields what
// could be read; the MCP handler reports the error to the client.
func (g *mcpGuard) peek(body []byte) rpcInfo {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return rpcInfo{method: "batch"}
	}
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal(trimmed, &msg) // partial results are fine
	var info rpcInfo
	switch {
	case msg.Method == "":
		return info
	case knownMethods[msg.Method]:
		info.method = msg.Method
	default:
		info.method = "other"
	}
	if info.method == "tools/call" {
		info.tool = "unknown"
		if g.known[msg.Params.Name] {
			info.tool = msg.Params.Name
		}
	}
	return info
}

// recoverMCP turns a panic in the MCP handler into a 500 (when no response
// has started) and a log record naming only the method, the tool and the
// panic value's type: the value itself may carry the API key or response
// data. http.ErrAbortHandler is re-raised, keeping its meaning.
func recoverMCP(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &startWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler { //nolint:errorlint // net/http compares by identity
				panic(p)
			}
			info := rpcInfoFrom(r.Context())
			log.Error("mcp handler panic", "rpc_method", info.method, "tool", info.tool, "panic_type", errorType(p))
			if !sw.started {
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// startWriter records whether the response has started.
type startWriter struct {
	http.ResponseWriter
	started bool
}

func (s *startWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		s.started = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *startWriter) Write(b []byte) (int, error) {
	s.started = true
	return s.ResponseWriter.Write(b)
}

// Unwrap returns the underlying writer for http.ResponseController.
func (s *startWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// safeTools returns copies of toolset whose handlers recover from panics: the
// call becomes a generic tool error, and the log names the tool and the panic
// value's type only.
func safeTools(toolset []tools.Tool, log *slog.Logger) []tools.Tool {
	out := make([]tools.Tool, len(toolset))
	for i, t := range toolset {
		h, name := t.Handler, t.Name
		t.Handler = func(ctx context.Context, c tools.Getter, apiKey string, args json.RawMessage) (res tools.Result, err error) {
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler { //nolint:errorlint // net/http compares by identity
					panic(p)
				}
				log.Error("tool panic", "tool", name, "panic_type", errorType(p))
				res, err = tools.Result{Errors: []string{"internal error"}}, nil
			}()
			return h(ctx, c, apiKey, args)
		}
		out[i] = t
	}
	return out
}

// errorType names the dynamic type of v; never its value.
func errorType(v any) string { return fmt.Sprintf("%T", v) }
