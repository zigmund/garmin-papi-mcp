package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// panicSecret is part of a panic value; it must never reach a log or reply.
const panicSecret = "panic-value-with-private-detail"

// explodingTool panics with a value carrying the API key and a secret.
func explodingTool() tools.Tool {
	return tools.Tool{
		Name:        "explode",
		Description: "panics",
		Handler: func(_ context.Context, _ tools.Getter, apiKey string, _ json.RawMessage) (tools.Result, error) {
			panic(fmt.Errorf("%s %s", panicSecret, apiKey))
		},
	}
}

// Carried item 1: a panicking tool yields a generic tool error, and the log
// names only the tool and the panic value's type.
func TestToolPanicIsGenericToolError(t *testing.T) {
	s := newStack(t, stackOpts{tools: append(tools.All(), explodingTool())})
	l := s.login(t)
	res := s.call(t, l.access, "explode", "{}")
	if !res.IsError {
		t.Fatalf("result = %+v, want a tool error", res)
	}
	if txt := res.text(); strings.Contains(txt, panicSecret) || strings.Contains(txt, testAPIKey) {
		t.Errorf("tool result leaks the panic value: %q", txt)
	}
	// The server keeps serving.
	if res := s.call(t, l.access, "get_profile", "{}"); res.IsError {
		t.Fatalf("after panic: %+v", res)
	}
	logs := s.logs.String()
	if strings.Contains(logs, panicSecret) || strings.Contains(logs, testAPIKey) {
		t.Errorf("logs carry the panic value:\n%s", logs)
	}
	if !strings.Contains(logs, `"tool":"explode"`) || !strings.Contains(logs, `"panic_type":"*errors.errorString"`) {
		t.Errorf("panic log lacks the tool name or panic type:\n%s", logs)
	}
	if strings.Contains(logs, "goroutine ") {
		t.Error("a stack trace was logged")
	}
}

// Carried item 1: a panic in the MCP handler itself (outside any tool) is a
// 500 with a generic body, logged by method and type only.
func TestMCPHandlerPanicIs500(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := recoverMCP(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(panicSecret + " " + testAPIKey)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(withRPCInfo(req.Context(), rpcInfo{method: "tools/call", tool: "get_profile"}))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), panicSecret) || strings.Contains(logs.String(), panicSecret) || strings.Contains(logs.String(), testAPIKey) {
		t.Errorf("panic value leaked: body %q logs %s", rec.Body.String(), logs.String())
	}
	for _, want := range []string{`"rpc_method":"tools/call"`, `"tool":"get_profile"`, `"panic_type":"string"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %s: %s", want, logs.String())
		}
	}

	// After the response started, the panic cannot change the status; the
	// recovery still keeps the value out of every log.
	logs.Reset()
	h = recoverMCP(log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic(panicSecret)
	}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusOK || strings.Contains(logs.String(), panicSecret) {
		t.Errorf("status %d, logs %s", rec.Code, logs.String())
	}

	// http.ErrAbortHandler keeps its meaning (abort the response silently).
	h = recoverMCP(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler { //nolint:errorlint // identity
				t.Errorf("recovered %v, want ErrAbortHandler re-raised", p)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
	}()
}

// Carried item 2: every 401 on /mcp carries the challenge, including the
// bare 401 the MCP handler writes when Garmin rejects the key.
func TestEvery401OnMCPCarriesChallenge(t *testing.T) {
	const meta = "https://mcp.example.com/.well-known/oauth-protected-resource/mcp"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("own") {
			w.Header().Set("WWW-Authenticate", "Bearer own")
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	h := withChallenge(meta, inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	got := rec.Header().Values("WWW-Authenticate")
	want := `Bearer error="invalid_token", error_description="Garmin rejected the API key", resource_metadata="` + meta + `"`
	if len(got) != 1 || got[0] != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}

	// A challenge already set by the guard is kept as is.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp?own", nil))
	if got := rec.Header().Values("WWW-Authenticate"); len(got) != 1 || got[0] != "Bearer own" {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	// Other statuses get none.
	h = withChallenge(meta, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("challenge on a 200")
	}
}

// Carried item 3: an Origin that is not the public origin is refused (DNS
// rebinding protection); a matching or absent Origin is accepted.
func TestMCPOriginValidation(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	for _, origin := range []string{"https://evil.example", "null", "http://localhost:1", s.url + ".evil.example", "not a url"} {
		data := s.garmin.data.Load()
		resp, _ := s.rpc(t, l.access, callBody("get_profile", "{}"), "Origin", origin)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Origin %q = %d, want 403", origin, resp.StatusCode)
		}
		if s.garmin.data.Load() != data {
			t.Errorf("Origin %q: Garmin was called", origin)
		}
	}
	// No token is needed to be refused: the check runs first.
	if resp, _ := s.rpc(t, "", initializeBody, "Origin", "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("unauthenticated foreign Origin = %d, want 403", resp.StatusCode)
	}
	resp, _ := s.rpc(t, l.access, initializeBody, "Origin", s.url)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("same Origin = %d, want 200", resp.StatusCode)
	}
	resp, _ = s.rpc(t, l.access, initializeBody, "Origin", strings.ToUpper(s.url[:4])+s.url[4:])
	if resp.StatusCode != http.StatusOK {
		t.Errorf("same Origin, other case = %d, want 200", resp.StatusCode)
	}
	resp, _ = s.rpc(t, l.access, initializeBody)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no Origin = %d, want 200", resp.StatusCode)
	}
}

func TestMCPPerIPLimitBeforeToken(t *testing.T) {
	s := newStack(t, stackOpts{yaml: "limits: { ip: { per_minute: 60, burst: 2 } }\n"})
	for i := range 2 {
		if resp, _ := s.rpc(t, "", initializeBody); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("request %d = %d, want 401", i, resp.StatusCode)
		}
	}
	resp, _ := s.rpc(t, "", initializeBody)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("third = %d Retry-After %q, want 429 before the token is looked at", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestMCPPerTokenLimit(t *testing.T) {
	s := newStack(t, stackOpts{yaml: "limits: { token: { per_minute: 1, burst: 2 } }\n"})
	l := s.login(t)
	for i := range 2 {
		if resp, _ := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d", i, resp.StatusCode)
		}
	}
	// A refreshed token is the same grant (the same key): no fresh budget.
	access2, _ := s.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {l.refresh}, "client_id": {l.clientID}})
	resp, _ := s.rpc(t, access2, initializeBody)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third = %d, want 429", resp.StatusCode)
	}
}

// The per-grant key is a MAC of the API key, never the key itself.
func TestGrantKeyIsMAC(t *testing.T) {
	sealer, err := seal.New([][]byte{mustB64(t, testKey("grant"))})
	if err != nil {
		t.Fatal(err)
	}
	k := grantKey(sealer, testAPIKey)
	if k != hex.EncodeToString(sealer.MAC("token", []byte(testAPIKey))) {
		t.Errorf("grantKey = %q, want hex(MAC(\"token\", key))", k)
	}
	if strings.Contains(k, testAPIKey) || k == grantKey(sealer, testAPIKey+"x") {
		t.Error("grant key leaks or collides")
	}
}

func TestMCPBodyCap(t *testing.T) {
	s := newStack(t, stackOpts{yaml: "limits: { max_request_body: 1024 }\n"})
	l := s.login(t)
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", 2048) + `"}}`
	if resp, _ := s.rpc(t, l.access, big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", resp.StatusCode)
	}
	// Chunked (no Content-Length) is capped while reading.
	req, _ := http.NewRequest(http.MethodPost, s.url+"/mcp", struct{ *strings.Reader }{strings.NewReader(big)})
	req.Header.Set("Authorization", "Bearer "+l.access)
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := s.do(t, req); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized chunked body = %d, want 413", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, s.url+"/mcp", strings.NewReader(initializeBody))
	req.Header.Set("Authorization", "Bearer "+l.access)
	req.Header.Set("Content-Encoding", "gzip")
	if resp, _ := s.do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("Content-Encoding = %d, want 415", resp.StatusCode)
	}
}

func TestMCPMethodAndPath(t *testing.T) {
	s := newStack(t, stackOpts{})
	for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodPut, http.MethodOptions} {
		req, _ := http.NewRequest(m, s.url+"/mcp", nil)
		resp, _ := s.do(t, req)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
			t.Errorf("%s /mcp = %d Allow %q", m, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	for _, p := range []string{"/mcp/", "/mcp/x", "/mc", "/nope"} {
		req, _ := http.NewRequest(http.MethodPost, s.url+p, nil)
		if resp, _ := s.do(t, req); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestConcurrencyCap(t *testing.T) {
	l := newLimits(limitsConfig(t, "limits: { max_concurrent_per_token: 1, max_in_flight: 2 }\n"), slog.New(slog.DiscardHandler), nil)
	rel1, ok := l.admit(httptest.NewRecorder(), nil, "a")
	if !ok {
		t.Fatal("first slot refused")
	}
	rec := httptest.NewRecorder()
	if _, ok := l.admit(rec, nil, "a"); ok || rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second slot for the same grant: ok=%v status=%d", ok, rec.Code)
	}
	rel2, ok := l.admit(httptest.NewRecorder(), nil, "b")
	if !ok {
		t.Fatal("other grant refused")
	}
	rec = httptest.NewRecorder()
	if _, ok := l.admit(rec, nil, "c"); ok || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("beyond max_in_flight: ok=%v status=%d", ok, rec.Code)
	}
	rel1()
	rel2()
	if _, ok := l.admit(httptest.NewRecorder(), nil, "a"); !ok {
		t.Error("slot not released")
	}
}

func limitsConfig(t *testing.T, extra string) config.ProxyLimits {
	t.Helper()
	cfg, err := config.Parse([]byte("public_url: https://mcp.example.com\nsecrets: { token_keys: [\""+testKey("limits")+"\"] }\n"+extra), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Limits
}

// Final-review item 2: the platform backends that call /mcp on the user's
// behalf may send their own Origin. An Origin equal to the origin of any
// allow-listed redirect URI (the defaults: claude.ai, claude.com, chatgpt.com)
// is accepted; anything else still is not.
func TestMCPOriginOfRedirectURIsAccepted(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	for _, origin := range []string{
		"https://claude.ai", "https://claude.com", "https://chatgpt.com",
		"HTTPS://Claude.AI", "https://chatgpt.com:443", "https://claude.ai/",
	} {
		if resp, _ := s.rpc(t, l.access, initializeBody, "Origin", origin); resp.StatusCode != http.StatusOK {
			t.Errorf("Origin %q = %d, want 200", origin, resp.StatusCode)
		}
	}
	for _, origin := range []string{
		"http://claude.ai",                // other scheme
		"https://claude.ai:8443",          // other port
		"https://claude.ai.evil.example",  // suffix
		"https://evil.claude.ai",          // subdomain
		"https://claude.ai/api/mcp",       // a path is not an origin
		"https://claude.ai?x=1",           // query
		"https://user@claude.ai",          // userinfo
		"https://chatgpt.com#f",           // fragment
		"https://claude.ai, https://x.io", // a list
	} {
		if resp, _ := s.rpc(t, l.access, initializeBody, "Origin", origin); resp.StatusCode != http.StatusForbidden {
			t.Errorf("Origin %q = %d, want 403", origin, resp.StatusCode)
		}
	}

	// The set follows the configured allow-list, not the defaults.
	s2 := newStack(t, stackOpts{yaml: "oauth: { allowed_redirect_uris: [\"" + testRedirect + "\", \"http://localhost:53682/callback\"] }\n"})
	l2 := s2.login(t)
	for origin, want := range map[string]int{
		"http://localhost:53682": http.StatusOK,
		"https://claude.ai":      http.StatusOK,
		"https://chatgpt.com":    http.StatusForbidden,
		"http://localhost:53683": http.StatusForbidden,
		s2.url:                   http.StatusOK,
	} {
		if resp, _ := s2.rpc(t, l2.access, initializeBody, "Origin", origin); resp.StatusCode != want {
			t.Errorf("configured allow-list: Origin %q = %d, want %d", origin, resp.StatusCode, want)
		}
	}
}
