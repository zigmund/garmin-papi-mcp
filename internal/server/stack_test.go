package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// testAPIKey is an obviously fake Garmin Personal API key.
const testAPIKey = "test-key-not-real"

// testRedirect is one of the default allow-listed redirect URIs.
const testRedirect = "https://claude.ai/api/mcp/auth_callback"

// testKey derives a deterministic, non-patterned 32-byte token key from seed,
// so no key literal lives in the repository.
func testKey(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return base64.StdEncoding.EncodeToString(h[:])
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeGarmin is an in-process stand-in for the Garmin Personal API.
type fakeGarmin struct {
	srv *httptest.Server
	jwt string

	exchanges atomic.Int32
	data      atomic.Int32
	reject    atomic.Bool  // exchange and data calls answer 401
	status    atomic.Int32 // data calls answer this status when non-zero
	retry     atomic.Value // string: Retry-After of an overridden status

	mu      sync.Mutex
	auths   []string // Authorization of data calls
	agents  []string // User-Agent of every call
	keyAuth []string // Authorization of exchange calls
}

// fakeJWT builds a JWT-shaped string at run time (no token literal in the
// repository) that expires in 24 hours.
func fakeJWT() string {
	enc := base64.RawURLEncoding.EncodeToString
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{"iat": now, "exp": now + 86400, "sub": "synthetic"})
	sig := make([]byte, 24)
	_, _ = rand.Read(sig)
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(claims) + "." + enc(sig)
}

func newFakeGarmin(t *testing.T) *fakeGarmin {
	t.Helper()
	f := &fakeGarmin{jwt: fakeJWT()}
	f.retry.Store("")
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGarmin) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.agents = append(f.agents, r.UserAgent())
	f.mu.Unlock()
	if r.URL.Path == "/personalapitoken-service/personal-api/personal-api-key/token" {
		f.exchanges.Add(1)
		f.mu.Lock()
		f.keyAuth = append(f.keyAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.Method != http.MethodPost || f.reject.Load() || r.Header.Get("Authorization") != "ApiKey "+testAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, f.jwt)
		return
	}
	f.data.Add(1)
	f.mu.Lock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if f.reject.Load() || r.Header.Get("Authorization") != "Bearer "+f.jwt {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s := int(f.status.Load()); s != 0 {
		if ra, _ := f.retry.Load().(string); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(s)
		_, _ = io.WriteString(w, `{"message":"synthetic failure body"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"synthetic":true,"value":42}`)
}

func (f *fakeGarmin) dataAuths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

func (f *fakeGarmin) userAgents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.agents...)
}

// stack is the full service on a real listener, against a fake Garmin.
type stack struct {
	url    string // public_url == the listener's origin
	client *http.Client
	garmin *fakeGarmin
	logs   *syncBuf // JSON records at debug level, the server's ErrorLog included
	sealer *seal.Sealer
	skew   *atomic.Int64 // seconds added to the sealer's clock
	cfg    *config.Config
}

type stackOpts struct {
	yaml  string       // extra top-level config
	tools []tools.Tool // nil means tools.All()
}

func newStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	g := newFakeGarmin(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pub := "http://" + ln.Addr().String()
	y := "listen: " + ln.Addr().String() + "\n" +
		"public_url: " + pub + "\n" +
		"log: { level: debug, format: json }\n" +
		"secrets: { token_keys: [\"" + testKey("stack") + "\"] }\n" +
		"garmin: { base_url: \"" + g.srv.URL + "\", timeout: 5s }\n" + o.yaml
	cfg, err := config.Parse([]byte(y), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skew := new(atomic.Int64)
	sealer, err := seal.New(cfg.Secrets.Keys, seal.WithClock(func() time.Time {
		return time.Now().Add(time.Duration(skew.Load()) * time.Second)
	}))
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	toolset := o.tools
	if toolset == nil {
		toolset = tools.All()
	}
	srv, err := newServer(cfg, sealer, NewGarminClient(cfg, "test"), log, toolset)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("server did not shut down")
		}
	})
	return &stack{url: pub, client: client, garmin: g, logs: logs, sealer: sealer, skew: skew, cfg: cfg}
}

// metadataURL is the RFC 9728 document of the MCP endpoint.
func (s *stack) metadataURL() string { return s.url + "/.well-known/oauth-protected-resource/mcp" }

func (s *stack) do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func (s *stack) postForm(t *testing.T, path string, form url.Values) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.url+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(t, req)
}

// login is everything an MCP client and the user do to obtain tokens.
type login struct {
	clientID, verifier, code, access, refresh string
}

// login drives the OAuth flow over HTTP exactly as a client would: dynamic
// registration, authorize GET (form) and POST (key), code exchange with PKCE.
func (s *stack) login(t *testing.T) login {
	t.Helper()
	var l login

	// 1. Dynamic client registration.
	reg, _ := json.Marshal(map[string]any{
		"client_name":                "Test Client",
		"redirect_uris":              []string{testRedirect},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
	req, _ := http.NewRequest(http.MethodPost, s.url+"/oauth/register", bytes.NewReader(reg))
	req.Header.Set("Content-Type", "application/json")
	resp, body := s.do(t, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d %s", resp.StatusCode, body)
	}
	var client struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(body, &client); err != nil || client.ClientID == "" {
		t.Fatalf("register body %s: %v", body, err)
	}
	l.clientID = client.ClientID

	// 2. PKCE S256.
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	l.verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(l.verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// 3. Authorize GET renders the key form.
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {l.clientID},
		"redirect_uri":          {testRedirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"state-1"},
		"resource":              {s.url + "/mcp"},
	}
	req, _ = http.NewRequest(http.MethodGet, s.url+"/oauth/authorize?"+q.Encode(), nil)
	resp, body = s.do(t, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize GET = %d %s", resp.StatusCode, body)
	}
	const marker = `name="request" value="`
	i := bytes.Index(body, []byte(marker))
	if i < 0 {
		t.Fatalf("no hidden request field in form")
	}
	rest := body[i+len(marker):]
	sealed := string(rest[:bytes.IndexByte(rest, '"')])

	// 4. Authorize POST with the key redirects with a code.
	resp, body = s.postForm(t, "/oauth/authorize", url.Values{"request": {sealed}, "api_key": {testAPIKey}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize POST = %d %s", resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != testRedirect || loc.Query().Get("state") != "state-1" {
		t.Fatalf("redirect = %s", loc.Redacted())
	}
	l.code = loc.Query().Get("code")
	if l.code == "" {
		t.Fatalf("no code in redirect")
	}

	// 5. Token exchange.
	l.access, l.refresh = s.token(t, url.Values{
		"grant_type": {"authorization_code"}, "code": {l.code}, "redirect_uri": {testRedirect},
		"code_verifier": {l.verifier}, "client_id": {l.clientID},
	})
	return l
}

func (s *stack) token(t *testing.T, form url.Values) (access, refresh string) {
	t.Helper()
	resp, body := s.postForm(t, "/oauth/token", form)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.Access == "" || tok.Refresh == "" || tok.Type != "Bearer" {
		t.Fatalf("token body: %v", err)
	}
	return tok.Access, tok.Refresh
}

// rpc posts one JSON-RPC message to /mcp. hdr are extra header pairs.
func (s *stack) rpc(t *testing.T, token, body string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.url+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return s.do(t, req)
}

func callBody(tool string, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
}

// toolResult decodes a successful JSON-RPC tools/call answer.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (s *stack) call(t *testing.T, token, tool, args string) toolResult {
	t.Helper()
	resp, body := s.rpc(t, token, callBody(tool, args))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call %s = %d %q", tool, resp.StatusCode, body)
	}
	var out struct {
		Result *toolResult      `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Result == nil {
		t.Fatalf("tools/call %s body %s: %v", tool, body, err)
	}
	return *out.Result
}

func (r toolResult) text() string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// wantChallenge asserts a 401 whose WWW-Authenticate points at the protected
// resource metadata; invalid says whether error="invalid_token" is expected.
func (s *stack) wantChallenge(t *testing.T, resp *http.Response, invalid bool) {
	t.Helper()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	got := resp.Header.Values("WWW-Authenticate")
	if len(got) != 1 {
		t.Fatalf("WWW-Authenticate = %q, want exactly one", got)
	}
	meta := `resource_metadata="` + s.metadataURL() + `"`
	if !invalid {
		if got[0] != "Bearer "+meta {
			t.Errorf("WWW-Authenticate = %q, want %q", got[0], "Bearer "+meta)
		}
		return
	}
	if !strings.HasPrefix(got[0], "Bearer ") || !strings.Contains(got[0], `error="invalid_token"`) || !strings.HasSuffix(got[0], meta) {
		t.Errorf("WWW-Authenticate = %q, want Bearer error=\"invalid_token\" ... %s", got[0], meta)
	}
}
