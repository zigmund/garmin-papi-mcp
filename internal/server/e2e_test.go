package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
)

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func TestFullFlowAuthorizeTokenToolsCall(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	if n := s.garmin.exchanges.Load(); n != 1 {
		t.Errorf("key exchanges during login = %d, want 1 (the form validates the key)", n)
	}

	resp, body := s.rpc(t, l.access, initializeBody)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize = %d %s", resp.StatusCode, body)
	}
	resp, body = s.rpc(t, l.access, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &list); err != nil || resp.StatusCode != http.StatusOK || len(list.Result.Tools) != 17 {
		t.Fatalf("tools/list = %d, %d tools, %v", resp.StatusCode, len(list.Result.Tools), err)
	}

	res := s.call(t, l.access, "get_profile", "{}")
	if res.IsError || !strings.Contains(res.text(), `"synthetic":true`) {
		t.Fatalf("get_profile = %+v", res)
	}
	auths := s.garmin.dataAuths()
	if len(auths) != 2 {
		t.Fatalf("Garmin data calls = %d, want 2 (settings and heart rate zones)", len(auths))
	}
	for _, a := range auths {
		if a != "Bearer "+s.garmin.jwt {
			t.Errorf("Garmin saw Authorization %q, want the exchanged JWT", a)
		}
	}
	if n := s.garmin.exchanges.Load(); n != 1 {
		t.Errorf("key exchanges = %d, want 1 (the JWT from the form is cached)", n)
	}
	for _, ua := range s.garmin.userAgents() {
		if ua != "garmin-papi-mcp/test" {
			t.Errorf("User-Agent to Garmin = %q", ua)
		}
	}

	// A refreshed access token works the same.
	access2, _ := s.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {l.refresh}, "client_id": {l.clientID}})
	if res := s.call(t, access2, "get_profile", "{}"); res.IsError {
		t.Fatalf("get_profile after refresh = %+v", res)
	}
}

func TestMCPWithoutTokenIs401WithMetadata(t *testing.T) {
	s := newStack(t, stackOpts{})
	for name, hdr := range map[string][]string{
		"none":  nil,
		"basic": {"Authorization", "Basic dXNlcjpwYXNz"},
	} {
		resp, body := s.rpc(t, "", initializeBody, hdr...)
		s.wantChallenge(t, resp, false)
		if strings.Contains(string(body), "jsonrpc") {
			t.Errorf("%s: 401 body = %q", name, body)
		}
	}
	// The challenge points at a document that exists and names the resource.
	req, _ := http.NewRequest(http.MethodGet, s.metadataURL(), nil)
	resp, body := s.do(t, req)
	var meta struct {
		Resource string   `json:"resource"`
		AS       []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || resp.StatusCode != 200 || meta.Resource != s.url+"/mcp" || len(meta.AS) != 1 || meta.AS[0] != s.url {
		t.Fatalf("metadata = %d %s", resp.StatusCode, body)
	}
	if s.garmin.exchanges.Load()+s.garmin.data.Load() != 0 {
		t.Error("Garmin was contacted for an unauthenticated request")
	}
}

func TestExpiredAccessToken401(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	before := s.garmin.data.Load()
	s.skew.Store(int64((2 * time.Hour).Seconds())) // access_token_ttl is 1h
	resp, _ := s.rpc(t, l.access, callBody("get_profile", "{}"))
	s.wantChallenge(t, resp, true)
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "expired") {
		t.Errorf("WWW-Authenticate = %q, want an expiry description", resp.Header.Get("WWW-Authenticate"))
	}
	if s.garmin.data.Load() != before {
		t.Error("Garmin was called with an expired token")
	}
}

func TestTruncatedToken401NoGarminCall(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	ex, data := s.garmin.exchanges.Load(), s.garmin.data.Load()
	for _, tok := range []string{l.access[:len(l.access)-6], l.access[:len(l.access)/2], l.refresh, l.code, "garbage"} {
		resp, _ := s.rpc(t, tok, callBody("get_profile", "{}"))
		s.wantChallenge(t, resp, true)
	}
	if s.garmin.exchanges.Load() != ex || s.garmin.data.Load() != data {
		t.Error("Garmin was contacted for an invalid token")
	}
}

func TestForeignKeySetToken401(t *testing.T) {
	s := newStack(t, stackOpts{})
	foreign, err := seal.New([][]byte{mustB64(t, testKey("foreign"))})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := foreign.Seal(seal.KindAccess, tokens.Access{ClientID: "c", APIKey: testAPIKey}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := s.rpc(t, tok, callBody("get_profile", "{}"))
	s.wantChallenge(t, resp, true)
	if s.garmin.exchanges.Load()+s.garmin.data.Load() != 0 {
		t.Error("Garmin was contacted for a foreign token")
	}
}

func TestKeyRejectedMidSessionIs401(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	if res := s.call(t, l.access, "get_profile", "{}"); res.IsError {
		t.Fatalf("first call = %+v", res)
	}
	s.garmin.reject.Store(true) // the user revoked the key at Garmin
	ex := s.garmin.exchanges.Load()
	resp, body := s.rpc(t, l.access, callBody("get_profile", "{}"))
	s.wantChallenge(t, resp, true)
	if len(body) != 0 {
		t.Errorf("401 body = %q, want empty", body)
	}
	if got := s.garmin.exchanges.Load() - ex; got != 1 {
		t.Errorf("re-exchanges after the data 401 = %d, want exactly 1", got)
	}
}

func TestGarmin500IsToolErrorNot401(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	s.garmin.status.Store(http.StatusInternalServerError)
	res := s.call(t, l.access, "get_profile", "{}")
	if !res.IsError || !strings.Contains(res.text(), "HTTP 500") {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.text(), "synthetic failure body") {
		t.Error("Garmin's error body reached the tool result")
	}
}

func TestGarmin429IsToolErrorWithRetryAfter(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	s.garmin.status.Store(http.StatusTooManyRequests)
	s.garmin.retry.Store("30")
	res := s.call(t, l.access, "get_profile", "{}")
	if !res.IsError || !strings.Contains(res.text(), "rate limiting") || !strings.Contains(res.text(), "30s") {
		t.Fatalf("result = %+v", res)
	}
}

func TestHealthz(t *testing.T) {
	s := newStack(t, stackOpts{})
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(m, s.url+"/healthz", nil)
		resp, body := s.do(t, req)
		if resp.StatusCode != 200 || (m == http.MethodGet && string(body) != "ok") {
			t.Fatalf("%s /healthz = %d %q", m, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
		}
	}
	if strings.Contains(s.logs.String(), "/healthz") {
		t.Errorf("healthz must not be access-logged: %s", s.logs.String())
	}
	req, _ := http.NewRequest(http.MethodPost, s.url+"/healthz", nil)
	if resp, _ := s.do(t, req); resp.StatusCode == 200 {
		t.Errorf("POST /healthz = %d", resp.StatusCode)
	}
}

// TestLogsContainNoSecrets runs the whole flow, success and failure paths,
// with debug logging and asserts that no credential ever reaches a log record
// (the server's ErrorLog goes to the same logger).
func TestLogsContainNoSecrets(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	s.call(t, l.access, "get_profile", "{}")
	s.call(t, l.access, "get_sleep", `{"date":"2026-10-01"}`)
	access2, refresh2 := s.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {l.refresh}, "client_id": {l.clientID}})
	// Failure paths: a replayed code, a truncated token, a bad Origin, Garmin
	// errors and finally a rejected key.
	s.postForm(t, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {l.code}, "redirect_uri": {testRedirect},
		"code_verifier": {l.verifier}, "client_id": {l.clientID},
	})
	s.rpc(t, access2[:len(access2)-4], callBody("get_profile", "{}"))
	s.rpc(t, access2, callBody("get_profile", "{}"), "Origin", "https://evil.example")
	s.garmin.status.Store(http.StatusInternalServerError)
	s.call(t, access2, "get_profile", "{}")
	s.garmin.status.Store(0)
	s.garmin.reject.Store(true)
	s.rpc(t, access2, callBody("get_profile", "{}"))

	logs := s.logs.String()
	if !strings.Contains(logs, `"msg":"http request"`) {
		t.Fatalf("no request log records captured (debug logging not wired?):\n%s", logs)
	}
	secrets := map[string]string{
		"api key":        testAPIKey,
		"access token":   l.access,
		"access token 2": access2,
		"refresh token":  l.refresh,
		"refresh 2":      refresh2,
		"code":           l.code,
		"pkce verifier":  l.verifier,
		"garmin jwt":     s.garmin.jwt,
	}
	for name, v := range secrets {
		if strings.Contains(logs, v) {
			t.Errorf("%s appears in the logs", name)
		}
		// A distinctive part of the value would leak just as well.
		if len(v) > 40 && strings.Contains(logs, v[len(v)-32:]) {
			t.Errorf("tail of %s appears in the logs", name)
		}
	}
	if strings.Contains(logs, "synthetic failure body") || strings.Contains(logs, `"synthetic":true`) {
		t.Error("a Garmin response body appears in the logs")
	}
	if strings.Contains(logs, "Bearer ") || strings.Contains(logs, "ApiKey ") {
		t.Error("an Authorization header value appears in the logs")
	}
}

// The request log names the MCP method and tool, nothing from the body.
func TestRequestLogRecordsMethodAndTool(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	s.call(t, l.access, "get_profile", "{}")
	s.rpc(t, l.access, callBody("no_such_tool_with_secret_name", "{}"))
	var found, unknown bool
	for _, line := range strings.Split(strings.TrimSpace(s.logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if rec["msg"] != "http request" {
			continue
		}
		if p, _ := rec["path"].(string); strings.Contains(p, "?") {
			t.Errorf("path with query logged: %q", p)
		}
		if rec["path"] == "/mcp" && rec["rpc_method"] == "tools/call" {
			switch rec["tool"] {
			case "get_profile":
				found = true
				if rec["status"] != float64(200) {
					t.Errorf("status = %v", rec["status"])
				}
				if _, ok := rec["duration"]; !ok {
					t.Error("no duration")
				}
			case "unknown":
				unknown = true
			}
		}
	}
	if !found {
		t.Errorf("no request record with rpc_method=tools/call tool=get_profile:\n%s", s.logs.String())
	}
	if !unknown {
		t.Error("an unregistered tool name must be logged as unknown")
	}
	if strings.Contains(s.logs.String(), "no_such_tool_with_secret_name") {
		t.Error("a client-chosen tool name was logged verbatim")
	}
}
