package oauth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
)

func newMux(t *testing.T, publicURL string) *http.ServeMux {
	t.Helper()
	cfg := &config.Config{PublicURL: publicURL}
	mux := http.NewServeMux()
	New(cfg, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).RegisterRoutes(mux)
	return mux
}

func do(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

const mcpPRM = PathResourceMetadataPrefix + "/mcp"

func TestAuthServerMetadata(t *testing.T) {
	for _, base := range []string{"https://mcp.example.com", "http://localhost:8787"} {
		mux := newMux(t, base)
		want := AuthServerMetadata{
			Issuer:                 base,
			AuthorizationEndpoint:  base + "/oauth/authorize",
			TokenEndpoint:          base + "/oauth/token",
			RegistrationEndpoint:   base + "/oauth/register",
			ResponseTypesSupported: []string{"code"},
			ResponseModesSupported: []string{"query"},
			AuthorizationResponseIssParameterSupported: true,
			GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
			CodeChallengeMethodsSupported:              []string{"S256"},
			TokenEndpointAuthMethodsSupported:          []string{"none", "client_secret_post", "client_secret_basic"},
		}
		for _, p := range []string{PathAuthServerMetadata, PathOIDCMetadata} {
			rec := do(mux, http.MethodGet, p)
			if rec.Code != 200 {
				t.Fatalf("%s: status %d", p, rec.Code)
			}
			if got := decode[AuthServerMetadata](t, rec); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: got %+v want %+v", p, got, want)
			}
			if strings.Contains(rec.Body.String(), "scopes_supported") {
				t.Errorf("%s: scopes_supported must be omitted", p)
			}
		}
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	base := "https://mcp.example.com"
	mux := newMux(t, base)
	want := ProtectedResourceMetadata{
		Resource:               base + "/mcp",
		AuthorizationServers:   []string{base},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           ResourceName,
	}
	// Both the path-suffixed document (RFC 9728) and the bare fallback.
	for _, p := range []string{mcpPRM, PathResourceMetadataPrefix} {
		rec := do(mux, http.MethodGet, p)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", p, rec.Code)
		}
		if got := decode[ProtectedResourceMetadata](t, rec); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v want %+v", p, got, want)
		}
	}
}

// The resource indicator is public_url plus the fixed MCP path, byte for byte.
func TestResourceURL(t *testing.T) {
	if got := ResourceURL("https://mcp.example.com"); got != "https://mcp.example.com/mcp" {
		t.Errorf("ResourceURL = %q", got)
	}
	if ResourceURL("http://localhost:8787") != "http://localhost:8787"+config.MCPPath {
		t.Error("ResourceURL does not use config.MCPPath")
	}
}

func TestHeaders(t *testing.T) {
	mux := newMux(t, "https://mcp.example.com")
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := do(mux, m, PathAuthServerMetadata)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", m, rec.Code)
		}
		for k, v := range map[string]string{
			"Content-Type":                "application/json",
			"Cache-Control":               "public, max-age=300",
			"Access-Control-Allow-Origin": "*",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s = %q, want %q", m, k, got, v)
			}
		}
		if m == http.MethodHead && rec.Body.Len() != 0 {
			t.Error("HEAD returned a body")
		}
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	mux := newMux(t, "https://mcp.example.com")
	for _, p := range []string{PathResourceMetadataPrefix + "/unknown/mcp", PathResourceMetadataPrefix + "/other", mcpPRM + "/extra"} {
		if rec := do(mux, http.MethodGet, p); rec.Code != 404 {
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
		}
	}
	for _, p := range []string{PathAuthServerMetadata, PathOIDCMetadata, mcpPRM, PathResourceMetadataPrefix} {
		rec := do(mux, http.MethodPost, p)
		if rec.Code != 405 {
			t.Errorf("POST %s: status %d, want 405", p, rec.Code)
		}
		if a := rec.Header().Get("Allow"); !strings.Contains(a, "GET") {
			t.Errorf("POST %s: Allow = %q", p, a)
		}
	}
}

func TestRFCKeyNames(t *testing.T) {
	mux := newMux(t, "https://mcp.example.com")
	as := decode[map[string]any](t, do(mux, http.MethodGet, PathAuthServerMetadata))
	for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint", "registration_endpoint",
		"response_types_supported", "response_modes_supported", "authorization_response_iss_parameter_supported", "grant_types_supported",
		"code_challenge_methods_supported", "token_endpoint_auth_methods_supported"} {
		if _, ok := as[k]; !ok {
			t.Errorf("AS metadata lacks %q", k)
		}
	}
	if _, ok := as["scopes_supported"]; ok {
		t.Error("scopes_supported must be absent")
	}
	prm := decode[map[string]any](t, do(mux, http.MethodGet, mcpPRM))
	for _, k := range []string{"resource", "authorization_servers", "bearer_methods_supported", "resource_name"} {
		if _, ok := prm[k]; !ok {
			t.Errorf("PRM lacks %q", k)
		}
	}
}

func TestPreflight(t *testing.T) {
	mux := newMux(t, "https://mcp.example.com")
	for _, p := range []string{PathAuthServerMetadata, PathOIDCMetadata, mcpPRM, PathResourceMetadataPrefix} {
		rec := do(mux, http.MethodOptions, p)
		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s: status %d", p, rec.Code)
		}
		h := rec.Header()
		if h.Get("Access-Control-Allow-Origin") != "*" ||
			h.Get("Access-Control-Allow-Methods") != "GET, HEAD, OPTIONS" ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "MCP-Protocol-Version") {
			t.Errorf("OPTIONS %s: bad CORS headers %v", p, h)
		}
	}
}

func TestNilSealerAndLogger(t *testing.T) {
	cfg := &config.Config{PublicURL: "https://mcp.example.com"}
	mux := http.NewServeMux()
	New(cfg, nil, nil, nil).RegisterRoutes(mux) // nil logger must not panic
	for _, p := range []string{PathAuthServerMetadata, PathOIDCMetadata, PathResourceMetadataPrefix, mcpPRM} {
		if rec := do(mux, http.MethodGet, p); rec.Code != 200 {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
}

func TestHeadContentLength(t *testing.T) {
	mux := newMux(t, "https://mcp.example.com")
	for _, p := range []string{PathAuthServerMetadata, mcpPRM} {
		get, head := do(mux, http.MethodGet, p), do(mux, http.MethodHead, p)
		if g, h := get.Header().Get("Content-Length"), head.Header().Get("Content-Length"); g == "" || g != h {
			t.Errorf("%s: GET Content-Length %q, HEAD %q", p, g, h)
		}
	}
}

func TestOtherMethodsNotAllowed(t *testing.T) {
	mux := newMux(t, "https://mcp.example.com")
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if rec := do(mux, m, PathAuthServerMetadata); rec.Code != 405 {
			t.Errorf("%s: status %d, want 405", m, rec.Code)
		}
	}
}

// TestIssuerIdentical checks the RFC 9207 requirement ChatGPT relies on: the
// AS metadata issuer (both documents), every PRM authorization_servers entry
// and the iss of a successful authorization redirect are the same byte string.
func TestIssuerIdentical(t *testing.T) {
	base := "https://mcp.example.com"
	mux := newMux(t, base)
	issuers := map[string]string{}
	for _, p := range []string{PathAuthServerMetadata, PathOIDCMetadata} {
		m := decode[AuthServerMetadata](t, do(mux, http.MethodGet, p))
		if !m.AuthorizationResponseIssParameterSupported {
			t.Errorf("%s: authorization_response_iss_parameter_supported not true", p)
		}
		issuers[p] = m.Issuer
	}
	for _, p := range []string{mcpPRM, PathResourceMetadataPrefix} {
		prm := decode[ProtectedResourceMetadata](t, do(mux, http.MethodGet, p))
		if len(prm.AuthorizationServers) != 1 {
			t.Fatalf("%s: authorization_servers = %v", p, prm.AuthorizationServers)
		}
		issuers["prm"+p] = prm.AuthorizationServers[0]
	}
	for k, v := range issuers {
		if v != base {
			t.Errorf("%s: issuer %q, want %q", k, v, base)
		}
	}

	// The iss of a successful authorization redirect is the same string.
	e := newAZ(t)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	iss := mustSeeOther(t, e.post(goodForm(sealed))).Query().Get("iss")
	if iss != issuers[PathAuthServerMetadata] {
		t.Errorf("authorize iss %q, metadata issuer %q", iss, issuers[PathAuthServerMetadata])
	}
}
