package config

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	key1 = testKey("key1")
	key2 = testKey("key2")
)

// testKey derives a deterministic, non-patterned 32-byte key from seed.
func testKey(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return base64.StdEncoding.EncodeToString(h[:])
}

func base() string {
	return "public_url: https://mcp.example.com/\nsecrets:\n  token_keys: [\"" + key1 + "\"]\n"
}

func TestDefaultRedirectURIs(t *testing.T) {
	got := DefaultRedirectURIs()
	for _, want := range []string{
		"https://claude.ai/api/mcp/auth_callback",
		"https://claude.com/api/mcp/auth_callback",
		"https://chatgpt.com/connector_platform_oauth_redirect",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("default redirect URIs lack %s: %v", want, got)
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := Parse([]byte(base()), ".")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"listen", cfg.Listen, DefaultListen},
		{"public_url", cfg.PublicURL, "https://mcp.example.com"},
		{"log.level", cfg.Log.Level, "info"},
		{"log.format", cfg.Log.Format, "json"},
		{"access ttl", cfg.OAuth.AccessTokenTTL, time.Hour},
		{"refresh ttl", cfg.OAuth.RefreshTokenTTL, 720 * time.Hour},
		{"code ttl", cfg.OAuth.CodeTTL, 5 * time.Minute},
		{"redirects", strings.Join(cfg.OAuth.AllowedRedirectURIs, ","), strings.Join(DefaultRedirectURIs(), ",")},
		{"max session ttl", cfg.OAuth.MaxSessionTTL, 2160 * time.Hour},
		{"garmin base", cfg.Garmin.BaseURL, "https://personal.connectapi.garmin.com"},
		{"garmin timeout", cfg.Garmin.Timeout, 20 * time.Second},
		{"ip per minute", cfg.Limits.IP.PerMinute, 300},
		{"ip burst", cfg.Limits.IP.Burst, 60},
		{"token per minute", cfg.Limits.Token.PerMinute, 60},
		{"token burst", cfg.Limits.Token.Burst, 20},
		{"concurrent per token", cfg.Limits.MaxConcurrentPerToken, 8},
		{"mcp path", MCPPath, "/mcp"},
		{"tls enabled", cfg.TLSEnabled(), false},
		{"reload", cfg.TLS.ReloadInterval, time.Duration(0)},
		{"slog", cfg.Log.SlogLevel(), slog.LevelInfo},
		{"keys", len(cfg.Secrets.Keys), 1},
		{"key len", len(cfg.Secrets.Keys[0]), 32},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseExplicitValues(t *testing.T) {
	y := `
allow_insecure_http: true
listen: 0.0.0.0:9000
public_url: http://localhost:9000
log: { level: DEBUG, format: text }
secrets: { token_keys: ["` + key1 + `", "` + key2 + `"] }
oauth:
  access_token_ttl: 30m
  refresh_token_ttl: 24h
  code_ttl: 1m
  allowed_redirect_uris: ["https://example.com/cb"]
tls: { cert_file: c.pem, key_file: k.pem }
garmin: { base_url: "http://127.0.0.1:9999", timeout: 5s }
`
	cfg, err := Parse([]byte(y), ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.SlogLevel() != slog.LevelDebug || cfg.Log.Level != "debug" {
		t.Errorf("log level: %+v", cfg.Log)
	}
	if cfg.OAuth.AccessTokenTTL != 30*time.Minute || cfg.OAuth.CodeTTL != time.Minute {
		t.Errorf("oauth: %+v", cfg.OAuth)
	}
	if !cfg.TLSEnabled() || cfg.TLS.ReloadInterval != time.Minute {
		t.Errorf("tls: %+v", cfg.TLS)
	}
	if len(cfg.Secrets.Keys) != 2 || base64.StdEncoding.EncodeToString(cfg.Secrets.Keys[0]) != key1 || base64.StdEncoding.EncodeToString(cfg.Secrets.Keys[1]) != key2 {
		t.Errorf("keys order wrong")
	}
	if cfg.Garmin.BaseURL != "http://127.0.0.1:9999" || cfg.Garmin.Timeout != 5*time.Second {
		t.Errorf("garmin: %+v", cfg.Garmin)
	}
}

func TestSecretsRedacted(t *testing.T) {
	cfg, err := Parse([]byte(base()), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg.Secrets)} {
		if strings.Contains(s, key1) {
			t.Errorf("key leaked: %s", s)
		}
	}
}

func TestValidation(t *testing.T) {
	keys := "secrets: { token_keys: [\"" + key1 + "\"] }\n"
	pub := "public_url: https://mcp.example.com\n"
	okUp := ""

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"missing public_url", keys + okUp, "public_url: required"},
		{"public_url relative", "public_url: mcp.example.com\n" + keys + okUp, "public_url"},
		{"public_url ftp", "public_url: ftp://x\n" + keys + okUp, "public_url"},
		{"public_url query", "public_url: https://x?a=b\n" + keys + okUp, "must not contain a query"},
		{"public_url path", "public_url: https://x.example/mcp-proxy\n" + keys + okUp, "public_url: must be an origin without a path"},
		{"public_url path slash", "public_url: https://x.example/a/b/\n" + keys + okUp, "public_url: must be an origin without a path"},
		{"public_url fragment", "public_url: https://x#f\n" + keys + okUp, "must not contain a fragment"},
		{"bad log level", pub + "log: {level: trace}\n" + keys + okUp, "log.level"},
		{"bad log format", pub + "log: {format: xml}\n" + keys + okUp, "log.format"},
		{"negative ttl", pub + "oauth: {code_ttl: -1m}\n" + keys + okUp, "oauth.code_ttl"},
		{"bad redirect", pub + "oauth: {allowed_redirect_uris: [\"/cb\"]}\n" + keys + okUp, "oauth.allowed_redirect_uris[0]"},
		{"no keys", pub + okUp, "got neither"},
		{"both key sources", pub + "secrets: {token_keys: [\"" + key1 + "\"], token_keys_file: x}\n" + okUp, "got both"},
		{"bad base64", pub + "secrets: {token_keys: [\"!!!\"]}\n" + okUp, "secrets.token_keys[0]: not valid"},
		{"short key", pub + "secrets: {token_keys: [\"" + base64.StdEncoding.EncodeToString([]byte("short")) + "\"]}\n" + okUp, "decoded length is 5"},
		{"public_url userinfo", "public_url: https://u:p@x.example\n" + keys + okUp, "public_url: must not contain userinfo"},
		{"listen no port", pub + "listen: localhost\n" + keys + okUp, "listen"},
		{"listen big port", pub + "listen: \"127.0.0.1:70000\"\n" + keys + okUp, "port"},
		{"listen text port", pub + "listen: \"127.0.0.1:http\"\n" + keys + okUp, "port"},
		{"duplicate token_keys", pub + "secrets: {token_keys: [\"" + key1 + "\", \"" + key2 + "\", \"" + key1 + "\"]}\n" + okUp, "secrets.token_keys[2]: duplicates secrets.token_keys[0]"},
		{"duplicate redirect", pub + "oauth: {allowed_redirect_uris: [\"https://a/cb\", \"https://a/cb\"]}\n" + keys + okUp, "allowed_redirect_uris[1]: duplicates [0]"},
		{"multi document", base() + "---\nlisten: x\n", "multiple documents"},
		{"garmin http", pub + keys + "garmin: {base_url: http://garmin.example.com}\n", "garmin.base_url: must use https"},
		{"garmin relative", pub + keys + "garmin: {base_url: /x}\n", "garmin.base_url"},
		{"garmin userinfo", pub + keys + "garmin: {base_url: \"https://u:p@x.example\"}\n", "garmin.base_url: must not contain userinfo"},
		{"garmin negative timeout", pub + keys + "garmin: {timeout: -1s}\n", "garmin.timeout"},
		{"garmin huge timeout", pub + keys + "garmin: {timeout: 1h}\n", "garmin.timeout"},
		{"tls cert only", pub + keys + okUp + "tls: {cert_file: c.pem}\n", "set together"},
		{"tls key only", pub + keys + okUp + "tls: {key_file: k.pem}\n", "set together"},
		{"tls negative reload", pub + keys + okUp + "tls: {reload_interval: -1s}\n", "tls.reload_interval"},
		{"unknown field", pub + keys + okUp + "bogus: 1\n", "bogus"},
		{"unknown nested field", pub + "secrets: {token_keys: [\"" + key1 + "\"], nope: 1}\n" + okUp, "nope"},
		{"bad duration", pub + "oauth: {code_ttl: soon}\n" + keys + okUp, "into time.Duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml), ".")
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestValidationReportsAllProblems(t *testing.T) {
	_, err := Parse([]byte("log: {level: x}\ngarmin: {timeout: -1s}\n"), ".")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, w := range []string{"public_url", "log.level", "garmin.timeout", "got neither"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in %v", w, err)
		}
	}
}

func TestEmptyDocument(t *testing.T) {
	if _, err := Parse(nil, "."); err == nil {
		t.Fatal("expected validation error for empty config")
	}
}

func TestKeysFile(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys")
	content := "# primary\n" + key1 + "\n\n  # old\n  " + key2 + "  \n"
	if err := os.WriteFile(keysPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	y := "public_url: https://mcp.example.com\nsecrets: {token_keys_file: keys}\n"
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath) // relative path resolves against config dir
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Secrets.Keys) != 2 || base64.StdEncoding.EncodeToString(cfg.Secrets.Keys[0]) != key1 || base64.StdEncoding.EncodeToString(cfg.Secrets.Keys[1]) != key2 {
		t.Fatalf("keys: %d", len(cfg.Secrets.Keys))
	}

	// Absolute path works regardless of baseDir.
	y = strings.Replace(y, "keys}", keysPath+"}", 1)
	if _, err := Parse([]byte(y), "/nonexistent"); err != nil {
		t.Fatal(err)
	}
}

func TestKeysFileErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("empty", "# nothing\n\n")
	write("bad", key1+"\nnot-base64!\n")
	write("dup", key1+"\n"+key1+"\n")
	tests := []struct{ file, want string }{
		{"missing", "reading keys file"},
		{"empty", "contains no keys"},
		{"bad", "token_keys_file line 2"},
		{"dup", "line 2: duplicates secrets.token_keys_file line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			y := "public_url: https://mcp.example.com\nsecrets: {token_keys_file: " + tt.file + "}\n"
			_, err := Parse([]byte(y), dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error")
	}
}

func TestConfigRejectsGarminHTTP(t *testing.T) {
	for _, tc := range []struct {
		url      string
		insecure bool
		ok       bool
	}{
		{"http://garmin.example.com", false, false},
		{"http://127.0.0.1:8080", false, true},
		{"http://localhost:8080", false, true},
		{"http://[::1]:8080", false, true},
		{"https://garmin.example.com", false, true},
		// allow_insecure_http does not relax garmin.base_url: the key goes there.
		{"http://garmin.example.com", true, false},
		{"http://192.0.2.1:8080", true, false},
		{"http://127.0.0.1:8080", true, true},
	} {
		y := base() + "garmin: {base_url: \"" + tc.url + "\"}\n"
		if tc.insecure {
			y += "allow_insecure_http: true\n"
		}
		_, err := Parse([]byte(y), ".")
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tc.url, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "garmin.base_url: must use https")) {
			t.Errorf("%s: got %v, want https error", tc.url, err)
		}
	}
}

func TestSlogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo,
	} {
		if got := (Log{Level: in}).SlogLevel(); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}
