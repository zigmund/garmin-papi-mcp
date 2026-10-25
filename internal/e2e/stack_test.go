package e2e

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
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/server"
)

// fakeAPIKey is an obviously fake Garmin Personal API key.
const fakeAPIKey = "test-key-not-real"

// redirectURI is one of the default allow-listed redirect URIs.
const redirectURI = "https://claude.ai/api/mcp/auth_callback"

const exchangePath = "/personalapitoken-service/personal-api/personal-api-key/token"

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

// fakeGarmin serves the key exchange and a fixed table of data paths; any
// other path answers 404 so a wrong path in a tool fails its test.
type fakeGarmin struct {
	srv *httptest.Server
	jwt string

	mu     sync.Mutex
	bodies map[string]string // request path (with leading slash) -> JSON body
	hits   map[string]int
	query  map[string][]string // request path -> RawQuery of every data request

	// Optional, set before any request: each data request first signals
	// entered (if non-nil), then waits for gate to be closed (if non-nil),
	// then sleeps delay.
	entered chan struct{}
	gate    chan struct{}
	delay   time.Duration
}

func newFakeGarmin(t *testing.T, bodies map[string]string) *fakeGarmin {
	t.Helper()
	f := &fakeGarmin{jwt: fakeJWT(), bodies: bodies, hits: map[string]int{}, query: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGarmin) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == exchangePath {
		// Mimic the real endpoint: it serves text/plain only.
		if acc := r.Header.Get("Accept"); acc != "" && !strings.Contains(acc, "text/plain") && !strings.Contains(acc, "*/*") {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "ApiKey "+fakeAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, f.jwt)
		return
	}
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.query[r.URL.Path] = append(f.query[r.URL.Path], r.URL.RawQuery)
	body, ok := f.bodies[r.URL.Path]
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+f.jwt {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.gate != nil {
		<-f.gate
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// hitCount is the number of data requests seen for path.
func (f *fakeGarmin) hitCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// queries returns the RawQuery of every data request for path.
func (f *fakeGarmin) queries(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.query[path]...)
}

// totalHits is the number of data requests (key exchanges excluded).
func (f *fakeGarmin) totalHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

// stack is the full service on a real listener.
type stack struct {
	url    string
	client *http.Client
	access string
}

// newStack runs the real handler stack in-process. garminURL "" means the
// default (real) Garmin base URL; unlimited turns the MCP endpoint rate
// limits off (tests fire many calls in a row; the live test keeps them). It performs the whole OAuth login with
// apiKey and keeps the resulting access token.
func newStack(t *testing.T, garminURL, apiKey string, unlimited bool) *stack {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pub := "http://" + ln.Addr().String()
	h := sha256.Sum256([]byte("e2e-token-key"))
	y := "listen: " + ln.Addr().String() + "\n" +
		"public_url: " + pub + "\n" +
		"log: { level: error, format: json }\n" +
		"secrets: { token_keys: [\"" + base64.StdEncoding.EncodeToString(h[:]) + "\"] }\n"
	if garminURL != "" {
		y += "garmin: { base_url: \"" + garminURL + "\", timeout: 10s }\n"
	} else {
		y += "garmin: { timeout: 30s }\n"
	}
	if unlimited {
		y += "limits: { disabled: true }\n"
	}
	cfg, err := config.Parse([]byte(y), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := seal.New(cfg.Secrets.Keys)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := server.Handler(cfg, sealer, server.NewGarminClient(cfg, "e2e"), log)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(ctx)
	})
	s := &stack{url: pub, client: &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	s.access = s.login(t, apiKey)
	t.Cleanup(s.client.CloseIdleConnections)
	return s
}

// do performs req and returns the status and body. Failures never print the
// request or response.
func (s *stack) do(t *testing.T, req *http.Request) (int, []byte, http.Header) {
	t.Helper()
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: request failed", req.Method, req.URL.Path)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading response failed", req.Method, req.URL.Path)
	}
	return resp.StatusCode, body, resp.Header
}

func (s *stack) postForm(t *testing.T, path string, form url.Values) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.url+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(t, req)
}

// login drives the OAuth flow over HTTP as a client would: dynamic
// registration, authorize GET (form) and POST (key), code exchange with PKCE
// S256. It returns the access token. Failure messages carry statuses only.
func (s *stack) login(t *testing.T, apiKey string) string {
	t.Helper()
	reg, _ := json.Marshal(map[string]any{
		"client_name":                "E2E Client",
		"redirect_uris":              []string{redirectURI},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
	req, _ := http.NewRequest(http.MethodPost, s.url+"/oauth/register", bytes.NewReader(reg))
	req.Header.Set("Content-Type", "application/json")
	status, body, _ := s.do(t, req)
	if status != http.StatusCreated {
		t.Fatalf("register: status %d", status)
	}
	var client struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(body, &client); err != nil || client.ClientID == "" {
		t.Fatalf("register: no client_id")
	}

	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"state-1"},
		"resource":              {s.url + "/mcp"},
	}
	req, _ = http.NewRequest(http.MethodGet, s.url+"/oauth/authorize?"+q.Encode(), nil)
	status, body, _ = s.do(t, req)
	if status != http.StatusOK {
		t.Fatalf("authorize GET: status %d", status)
	}
	const marker = `name="request" value="`
	i := bytes.Index(body, []byte(marker))
	if i < 0 {
		t.Fatalf("authorize GET: no hidden request field")
	}
	rest := body[i+len(marker):]
	sealed := string(rest[:bytes.IndexByte(rest, '"')])

	status, _, hdr := s.postForm(t, "/oauth/authorize", url.Values{"request": {sealed}, "api_key": {apiKey}})
	if status != http.StatusSeeOther {
		t.Fatalf("authorize POST: status %d (key not accepted?)", status)
	}
	loc, err := url.Parse(hdr.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize POST: no code in redirect")
	}

	status, body, _ = s.postForm(t, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier}, "client_id": {client.ClientID},
	})
	if status != http.StatusOK {
		t.Fatalf("token: status %d", status)
	}
	var tok struct {
		Access string `json:"access_token"`
		Type   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.Access == "" || tok.Type != "Bearer" {
		t.Fatalf("token: unexpected response")
	}
	return tok.Access
}

// rpc posts one JSON-RPC message to /mcp and returns the status and body.
func (s *stack) rpc(t *testing.T, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.url+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+s.access)
	status, b, _ := s.do(t, req)
	return status, b
}

// toolDescriptor is one entry of a tools/list answer.
type toolDescriptor struct {
	Name        string `json:"name"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

func (s *stack) listTools(t *testing.T) []toolDescriptor {
	t.Helper()
	status, body := s.rpc(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var out struct {
		Result struct {
			Tools []toolDescriptor `json:"tools"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &out) != nil {
		t.Fatalf("tools/list: status %d, undecodable answer", status)
	}
	return out.Result.Tools
}

// callOutcome is a decoded tools/call answer. text is only for assertions
// and generic error lines; callers must never log it wholesale.
type callOutcome struct {
	status   int
	protoErr bool // JSON-RPC error object or undecodable body
	isError  bool
	text     string
}

func (s *stack) call(t *testing.T, tool, args string) callOutcome {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
	status, b := s.rpc(t, body)
	out := callOutcome{status: status}
	if status != http.StatusOK {
		out.protoErr = true
		return out
	}
	var r struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &r) != nil || r.Result == nil || r.Error != nil {
		out.protoErr = true
		return out
	}
	out.isError = r.Result.IsError
	var sb strings.Builder
	for _, c := range r.Result.Content {
		sb.WriteString(c.Text)
	}
	out.text = sb.String()
	return out
}

// callHTTP is a goroutine-safe tools/call: it never calls t. err is set only
// for transport failures.
func (s *stack) callHTTP(tool, args string) (status int, retryAfter string, out callOutcome, err error) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
	req, err := http.NewRequest(http.MethodPost, s.url+"/mcp", strings.NewReader(body))
	if err != nil {
		return 0, "", out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+s.access)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", out, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", out, err
	}
	out.status = resp.StatusCode
	if resp.StatusCode == http.StatusOK {
		var r struct {
			Result *struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if json.Unmarshal(b, &r) != nil || r.Result == nil {
			out.protoErr = true
		} else {
			out.isError = r.Result.IsError
		}
	} else {
		out.protoErr = true
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), out, nil
}
