package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
)

var testYAML = `
listen: 127.0.0.1:0
public_url: https://mcp.example.com
secrets:
  token_keys: ["` + testKey("server") + `"]
`

func parseCfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newTestServer(t *testing.T, logw io.Writer) *Server {
	t.Helper()
	cfg := parseCfg(t, testYAML)
	sealer, err := seal.New(cfg.Secrets.Keys)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := New(cfg, sealer, NewGarminClient(cfg, "test"), log)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func handlerFor(t *testing.T, yaml string, logw io.Writer) http.Handler {
	t.Helper()
	cfg := parseCfg(t, yaml)
	sealer, err := seal.New(cfg.Secrets.Keys)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(cfg, sealer, NewGarminClient(cfg, "test"), slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func postFrom(h http.Handler, path, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerRejectsMissingDeps(t *testing.T) {
	cfg := parseCfg(t, testYAML)
	sealer, err := seal.New(cfg.Secrets.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Handler(cfg, nil, NewGarminClient(cfg, "test"), nil); err == nil {
		t.Error("nil sealer accepted")
	}
	if _, err := Handler(cfg, sealer, nil, nil); err == nil {
		t.Error("nil garmin client accepted")
	}
	if _, err := Handler(nil, sealer, NewGarminClient(cfg, "test"), nil); err == nil {
		t.Error("nil config accepted")
	}
}

func TestRouting(t *testing.T) {
	var logs bytes.Buffer
	h := newTestServer(t, &logs).Handler()

	if rec := get(t, h, http.MethodPost, "/mcp"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp = %d, want 401", rec.Code)
	}
	for _, p := range []string{"/mcp/", "/mcp/x", "/nope"} {
		if rec := get(t, h, http.MethodPost, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
	logs.Reset()
	if rec := get(t, h, http.MethodGet, "//oauth/x"); rec.Code != http.StatusTemporaryRedirect && rec.Code != http.StatusMovedPermanently {
		t.Errorf("non-clean path = %d, want a redirect", rec.Code)
	}
	if !strings.Contains(logs.String(), "http request") {
		t.Error("mux redirect must be access-logged")
	}
	rec := get(t, h, http.MethodGet, "/.well-known/oauth-authorization-server")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "https://mcp.example.com") {
		t.Errorf("metadata = %d %s", rec.Code, rec.Body.String())
	}
	rec = get(t, h, http.MethodGet, "/.well-known/oauth-protected-resource/mcp")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"resource":"https://mcp.example.com/mcp"`) {
		t.Errorf("resource metadata = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTokenEndpointRateLimited(t *testing.T) {
	var logs bytes.Buffer
	h := handlerFor(t, testYAML+"oauth:\n  rate_limit:\n    token: { per_minute: 60, burst: 3 }\n", &logs)
	for i := range 3 {
		if rec := postFrom(h, "/oauth/token", "192.0.2.10:1", nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d = %d, want 400 (empty body reaches the handler)", i, rec.Code)
		}
	}
	rec := postFrom(h, "/oauth/token", "192.0.2.10:1", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := postFrom(h, "/oauth/token", "192.0.2.11:1", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("other IP = %d", rec.Code)
	}
	for range 10 {
		if rec := get(t, h, http.MethodGet, "/healthz"); rec.Code != 200 {
			t.Fatalf("healthz = %d", rec.Code)
		}
	}
	// The limiter and the access log agree on the client IP.
	if !strings.Contains(logs.String(), "remote=192.0.2.10") {
		t.Errorf("access log lacks the client IP: %s", logs.String())
	}
}

// The MCP per-IP limit resolves the client like the access log: forwarding
// headers count only from trusted proxies.
func TestMCPRateLimitHonoursTrustedProxies(t *testing.T) {
	yaml := testYAML + "limits: { ip: { per_minute: 60, burst: 1 } }\ntrusted_proxies: [\"198.51.100.0/24\"]\n"
	h := handlerFor(t, yaml, io.Discard)
	xff := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	postFrom(h, "/mcp", "198.51.100.1:1", xff("203.0.113.1"))
	if rec := postFrom(h, "/mcp", "198.51.100.2:1", xff("203.0.113.1")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same client behind a trusted proxy = %d, want 429", rec.Code)
	}
	if rec := postFrom(h, "/mcp", "198.51.100.2:1", xff("203.0.113.2")); rec.Code == http.StatusTooManyRequests {
		t.Error("distinct client behind a trusted proxy limited")
	}
	postFrom(h, "/mcp", "127.0.0.1:1", xff("203.0.113.50"))
	if rec := postFrom(h, "/mcp", "127.0.0.1:2", xff("203.0.113.51")); rec.Code != http.StatusTooManyRequests {
		t.Error("untrusted peer's header was honoured")
	}
}

func TestMCPLimitsDisabled(t *testing.T) {
	h := handlerFor(t, testYAML+"limits: { disabled: true, ip: { burst: 1 } }\n", io.Discard)
	for i := range 20 {
		if rec := postFrom(h, "/mcp", "192.0.2.10:1", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d = %d, want 401 (limits disabled)", i, rec.Code)
		}
	}
}

func TestServerTimeouts(t *testing.T) {
	s := newTestServer(t, io.Discard)
	if s.http.ReadHeaderTimeout != ReadHeaderTimeout || s.http.IdleTimeout != IdleTimeout || s.http.MaxHeaderBytes != MaxHeaderBytes {
		t.Errorf("server limits = %v %v %d", s.http.ReadHeaderTimeout, s.http.IdleTimeout, s.http.MaxHeaderBytes)
	}
	if s.http.ErrorLog == nil {
		t.Error("ErrorLog not routed through the sanitizer")
	}
	if s.shutdownTimeout != 15*time.Second {
		t.Errorf("shutdown timeout = %v", s.shutdownTimeout)
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	s := newTestServer(t, io.Discard)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestServeShutdownTimeoutClosesStreams(t *testing.T) {
	var logs syncBuf
	s := newTestServer(t, &logs)
	s.shutdownTimeout = 100 * time.Millisecond
	started := make(chan struct{})
	s.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	<-started
	readDone := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); readDone <- err }()
	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve blocked on a long request")
	}
	if el := time.Since(begin); el < s.shutdownTimeout {
		t.Errorf("Serve returned after %v, before the shutdown timeout %v", el, s.shutdownTimeout)
	}
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client connection still open after Serve returned")
	}
	if !strings.Contains(logs.String(), "timed out") {
		t.Errorf("missing timeout log: %s", logs.String())
	}
}

func TestReloadHooks(t *testing.T) {
	var logs syncBuf
	s := newTestServer(t, &logs)
	s.Reload()
	if !strings.Contains(logs.String(), "nothing to reload") {
		t.Errorf("logs: %s", logs.String())
	}

	var calls atomic.Int32
	s.OnReload("a", func() error { calls.Add(1); return os.ErrInvalid })
	s.OnReload("b", func() error { calls.Add(1); return nil })

	sigs := make(chan os.Signal)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.WatchReload(ctx, sigs); close(done) }()
	sigs <- os.Interrupt
	sigs <- os.Interrupt
	cancel()
	<-done
	if got := calls.Load(); got < 2 {
		t.Errorf("hook calls = %d, want >= 2", got)
	}
	if !strings.Contains(logs.String(), "reload failed") {
		t.Errorf("failing hook not logged: %s", logs.String())
	}
}
