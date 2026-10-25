// Package config loads, defaults and validates the garmin-papi-mcp YAML config.
package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied when a field is omitted.
const (
	DefaultListen          = "127.0.0.1:8787"
	DefaultLogLevel        = "info"
	DefaultLogFormat       = "json"
	DefaultAccessTokenTTL  = time.Hour
	DefaultRefreshTokenTTL = 720 * time.Hour
	DefaultMaxSessionTTL   = 2160 * time.Hour // 90 days
	DefaultCodeTTL         = 5 * time.Minute
	// MaxCodeTTL and MaxAccessTokenTTL are the largest accepted lifetimes: OAuth
	// 2.1 recommends authorization codes of at most 10 minutes, and an access
	// token cannot be revoked, so its TTL is the window of a thief.
	MaxCodeTTL            = 10 * time.Minute
	MaxAccessTokenTTL     = 24 * time.Hour
	DefaultReloadInterval = time.Minute

	// DefaultGarminBaseURL and DefaultGarminTimeout are the garmin section
	// defaults; MaxGarminTimeout is the largest accepted garmin.timeout.
	DefaultGarminBaseURL = "https://personal.connectapi.garmin.com"
	DefaultGarminTimeout = 20 * time.Second
	MaxGarminTimeout     = 5 * time.Minute

	// Default per-IP rate limits of the OAuth endpoints (requests per minute
	// and burst). They are generous because claude.ai and ChatGPT call register
	// and token from shared egress addresses.
	DefaultRegisterPerMinute  = 60
	DefaultRegisterBurst      = 30
	DefaultTokenPerMinute     = 300
	DefaultTokenBurst         = 100
	DefaultAuthorizePerMinute = 30
	DefaultAuthorizeBurst     = 10
	// DefaultAuthorizeFormPerMinute/Burst limit GET /oauth/authorize (rendering
	// the form seals a request, so it is not free), far above the POST limit
	// because a browser may load the form repeatedly.
	DefaultAuthorizeFormPerMinute = 300
	DefaultAuthorizeFormBurst     = 100
	// DefaultKeyCheckGlobalPerMinute/Burst are the unkeyed ceiling on Garmin
	// key checks (every POST /oauth/authorize that reaches the check costs one
	// Garmin call, whatever the outcome).
	DefaultKeyCheckGlobalPerMinute = 300
	DefaultKeyCheckGlobalBurst     = 100

	// KeySize is the required decoded size of every token key (AES-256).
	KeySize = 32
)

// DefaultRedirectURIs returns the redirect URIs allowed when none are configured.
func DefaultRedirectURIs() []string {
	return []string{
		"https://claude.ai/api/mcp/auth_callback",
		"https://claude.com/api/mcp/auth_callback",
		// ChatGPT's stable callback, used for servers that implement RFC 9207
		// issuer identification (this one does).
		"https://chatgpt.com/connector_platform_oauth_redirect",
	}
}

// DefaultTrustedProxies returns the trusted_proxies used when none are
// configured: loopback only (a reverse proxy on the same host).
func DefaultTrustedProxies() []string { return []string{"127.0.0.0/8", "::1/128"} }

// MCPPath is the fixed path of the MCP endpoint.
const MCPPath = "/mcp"

// reservedPrefixes are paths owned by the service itself.
var reservedPrefixes = []string{"/oauth", "/.well-known", "/healthz", MCPPath}

var quotedRe = regexp.MustCompile("`[^`]*`")

// Config is the root configuration.
type Config struct {
	Listen    string      `yaml:"listen"`
	PublicURL string      `yaml:"public_url"`
	Log       Log         `yaml:"log"`
	Secrets   Secrets     `yaml:"secrets"`
	OAuth     OAuth       `yaml:"oauth"`
	Limits    ProxyLimits `yaml:"limits"`
	Garmin    Garmin      `yaml:"garmin"`
	TLS       TLS         `yaml:"tls"`

	// AllowInsecureHTTP lets public_url and allowed_redirect_uris use plain
	// http for non-loopback hosts (loopback hosts may always use http). Off by
	// default. It does not apply to garmin.base_url, which receives every
	// user's API key: that one may use http only for a loopback host.
	AllowInsecureHTTP bool `yaml:"allow_insecure_http"`

	// TrustedProxies lists the CIDRs (or bare IPs) of reverse proxies whose
	// X-Forwarded-For / X-Real-IP headers are honoured. Empty means loopback.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// TrustedNets is TrustedProxies parsed and masked. Filled by Parse.
	TrustedNets []netip.Prefix `yaml:"-"`
}

// Garmin configures the Garmin Connect Personal API client.
type Garmin struct {
	// BaseURL is the API origin (https unless loopback; allow_insecure_http does not apply).
	BaseURL string `yaml:"base_url"`
	// Timeout bounds one request to the Garmin API.
	Timeout time.Duration `yaml:"timeout"`
}

// Log configures logging.
type Log struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // json|text
}

// Secrets holds token encryption keys. Exactly one of TokenKeys and
// TokenKeysFile must be set. Generate keys with `openssl rand -base64 32`.
type Secrets struct {
	TokenKeys     []string `yaml:"token_keys"`
	TokenKeysFile string   `yaml:"token_keys_file"`

	// Keys are the decoded keys (each KeySize bytes) in config order; the
	// first one encrypts, the rest only decrypt. Filled by Load/Parse.
	// TokenKeys is cleared after a successful Parse; Keys holds the result.
	Keys [][]byte `json:"-" yaml:"-"`
}

const redacted = "[redacted]"

// String redacts key material.
func (Secrets) String() string { return redacted }

// GoString redacts key material for %#v.
func (Secrets) GoString() string { return "config.Secrets{" + redacted + "}" }

// LogValue implements slog.LogValuer with a redacted value.
func (Secrets) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON redacts key material.
func (Secrets) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// MarshalYAML redacts key material.
func (Secrets) MarshalYAML() (any, error) { return redacted, nil }

// OAuth configures token lifetimes and redirect URI allow-list.
// A TTL of 0 or unset means the default; negative values are errors.
type OAuth struct {
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl"`
	MaxSessionTTL   time.Duration `yaml:"max_session_ttl"` // absolute lifetime of a refresh chain, counted from the code exchange
	CodeTTL         time.Duration `yaml:"code_ttl"`

	// SessionChoices, when set, lets the user pick the session lifetime on the
	// authorize form: each entry is a duration of at least 1h (a whole number of
	// hours, at most MaxSessionChoices entries, no duplicates) or "unlimited".
	// The choice supersedes MaxSessionTTL for new logins; MaxSessionTTL then
	// only bounds refresh tokens that carry no choice. SessionDefault (one of
	// the choices, the first when unset) is pre-selected. Unset keeps the fixed
	// MaxSessionTTL and shows no choice.
	SessionChoices []string `yaml:"session_choices"`
	SessionDefault string   `yaml:"session_default"`
	// Sessions and DefaultSession are SessionChoices and SessionDefault parsed.
	// Filled by Parse; Sessions is empty when no choices are configured.
	Sessions       []SessionChoice `yaml:"-"`
	DefaultSession SessionChoice   `yaml:"-"`

	AllowedRedirectURIs []string  `yaml:"allowed_redirect_uris"`
	RateLimit           RateLimit `yaml:"rate_limit"`
	// Page is the operator's contact and notice on the authorize page.
	Page Page `yaml:"page"`
}

// RateLimit configures the per-IP token-bucket limits of the OAuth endpoints.
// Zero or unset values mean the defaults; negative values are errors.
type RateLimit struct {
	// Disabled switches off every limit below, the global ceilings
	// included.
	Disabled  bool  `yaml:"disabled"`
	Register  Limit `yaml:"register"`  // POST /oauth/register
	Authorize Limit `yaml:"authorize"` // POST /oauth/authorize
	Token     Limit `yaml:"token"`     // POST /oauth/token
	// AuthorizeForm limits GET /oauth/authorize (the credentials form).
	AuthorizeForm Limit `yaml:"authorize_form"`

	// AuthorizeGlobal is the unkeyed ceiling on all POST /oauth/authorize
	// requests together. Zero values mean a default derived from code_ttl.
	AuthorizeGlobal Limit `yaml:"authorize_global"`
	// KeyCheckGlobal is the unkeyed ceiling on Garmin key checks of any
	// outcome (accepted, rejected, no verdict), across all clients: each one
	// is a Garmin call made on behalf of whoever submitted the form.
	KeyCheckGlobal Limit `yaml:"key_check_global"`
}

// Limit is one endpoint's bucket: sustained PerMinute requests, bursts up to Burst.
type Limit struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}

// Resolved returns r with every zero value replaced by its default.
func (r RateLimit) Resolved() RateLimit {
	fill := func(l *Limit, perMinute, burst int) {
		if l.PerMinute == 0 {
			l.PerMinute = perMinute
		}
		if l.Burst == 0 {
			l.Burst = burst
		}
	}
	fill(&r.Register, DefaultRegisterPerMinute, DefaultRegisterBurst)
	fill(&r.Authorize, DefaultAuthorizePerMinute, DefaultAuthorizeBurst)
	fill(&r.Token, DefaultTokenPerMinute, DefaultTokenBurst)
	fill(&r.AuthorizeForm, DefaultAuthorizeFormPerMinute, DefaultAuthorizeFormBurst)
	fill(&r.KeyCheckGlobal, DefaultKeyCheckGlobalPerMinute, DefaultKeyCheckGlobalBurst)
	return r
}

// TLS configures optional built-in TLS termination. Relative file paths are
// resolved against the config directory. ReloadInterval 0 or unset means the
// default (1m) when TLS is enabled.
type TLS struct {
	CertFile       string        `yaml:"cert_file"`
	KeyFile        string        `yaml:"key_file"`
	ReloadInterval time.Duration `yaml:"reload_interval"`
}

// TLSEnabled reports whether built-in TLS is configured.
func (c *Config) TLSEnabled() bool { return c.TLS.CertFile != "" && c.TLS.KeyFile != "" }

// SlogLevel maps Log.Level to a slog.Level (info for unknown values).
func (l Log) SlogLevel() slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(l.Level)); err != nil {
		return slog.LevelInfo
	}
	return lvl
}

// Load reads, decodes, defaults and validates the config file at p.
// A relative secrets.token_keys_file is resolved against the file's directory.
func Load(p string) (*Config, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg, err := Parse(data, filepath.Dir(p))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", p, err)
	}
	return cfg, nil
}

// Parse decodes config data strictly, applies defaults, loads keys and
// validates. baseDir resolves a relative secrets.token_keys_file.
func Parse(data []byte, baseDir string) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(&cfg)
	if err == nil {
		// A second document must not exist.
		var extra yaml.Node
		if err2 := dec.Decode(&extra); !errors.Is(err2, io.EOF) {
			if err2 != nil {
				return nil, decodeError(err2)
			}
			return nil, errors.New("decoding yaml: multiple documents are not supported")
		}
	} else if !errors.Is(err, io.EOF) {
		return nil, decodeError(err)
	}
	cfg.resolvePaths(baseDir)
	cfg.applyDefaults()

	var errs []error
	if err := cfg.loadKeys(); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, cfg.OAuth.Page.loadTerms()...)
	errs = append(errs, cfg.OAuth.resolveSessions()...)
	errs = append(errs, cfg.validate()...)
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid config: %w", errors.Join(errs...))
	}
	cfg.Secrets.TokenKeys = nil // decoded into Keys; don't keep encoded copies
	return &cfg, nil
}

// decodeError strips backtick-quoted values: yaml echoes offending scalars,
// which may be key material.
func decodeError(err error) error {
	return fmt.Errorf("decoding yaml: %s", quotedRe.ReplaceAllString(err.Error(), "`<redacted>`"))
}

// resolvePaths makes relative file paths absolute against baseDir.
func (c *Config) resolvePaths(baseDir string) {
	for _, p := range []*string{&c.Secrets.TokenKeysFile, &c.TLS.CertFile, &c.TLS.KeyFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(baseDir, *p)
		}
	}
	c.OAuth.Page.resolveTermsPath(baseDir)
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	c.PublicURL = strings.TrimSpace(c.PublicURL)
	if n, err := normalizePublicURL(c.PublicURL); err == nil {
		c.PublicURL = n // invalid values are left as-is and reported by validate
	}
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	if c.Log.Level == "" {
		c.Log.Level = DefaultLogLevel
	}
	c.OAuth.Page.normalize()
	c.Log.Format = strings.ToLower(strings.TrimSpace(c.Log.Format))
	if c.Log.Format == "" {
		c.Log.Format = DefaultLogFormat
	}
	if c.OAuth.AccessTokenTTL == 0 {
		c.OAuth.AccessTokenTTL = DefaultAccessTokenTTL
	}
	if c.OAuth.RefreshTokenTTL == 0 {
		c.OAuth.RefreshTokenTTL = DefaultRefreshTokenTTL
	}
	if c.OAuth.MaxSessionTTL == 0 {
		c.OAuth.MaxSessionTTL = DefaultMaxSessionTTL
	}
	if c.OAuth.CodeTTL == 0 {
		c.OAuth.CodeTTL = DefaultCodeTTL
	}
	if len(c.OAuth.AllowedRedirectURIs) == 0 {
		c.OAuth.AllowedRedirectURIs = DefaultRedirectURIs()
	}
	c.Garmin.BaseURL = strings.TrimSpace(c.Garmin.BaseURL)
	if c.Garmin.BaseURL == "" {
		c.Garmin.BaseURL = DefaultGarminBaseURL
	}
	if c.Garmin.Timeout == 0 {
		c.Garmin.Timeout = DefaultGarminTimeout
	}
	if c.TLSEnabled() && c.TLS.ReloadInterval == 0 {
		c.TLS.ReloadInterval = DefaultReloadInterval
	}
	c.Limits = c.Limits.Resolved()
	if len(c.TrustedProxies) == 0 {
		c.TrustedProxies = DefaultTrustedProxies()
	}
	c.OAuth.RateLimit = c.OAuth.RateLimit.Resolved()
}

// loadKeys resolves the key source and decodes Secrets.Keys.
func (c *Config) loadKeys() error {
	s := &c.Secrets
	hasList, hasFile := len(s.TokenKeys) > 0, s.TokenKeysFile != ""
	switch {
	case hasList && hasFile:
		return errors.New("secrets: exactly one of token_keys and token_keys_file must be set, got both")
	case !hasList && !hasFile:
		return errors.New("secrets: exactly one of token_keys and token_keys_file must be set, got neither")
	}

	// encoded[i] is described by where[i]; error text never includes key text.
	var encoded, where []string
	if hasList {
		encoded = s.TokenKeys
		for i := range encoded {
			where = append(where, fmt.Sprintf("secrets.token_keys[%d]", i))
		}
	} else {
		raw, err := os.ReadFile(s.TokenKeysFile)
		if err != nil {
			return fmt.Errorf("secrets.token_keys_file: reading keys file: %w", err)
		}
		for n, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			encoded = append(encoded, line)
			where = append(where, fmt.Sprintf("secrets.token_keys_file line %d", n+1))
		}
		if len(encoded) == 0 {
			return fmt.Errorf("secrets.token_keys_file: %s contains no keys", s.TokenKeysFile)
		}
	}

	var errs []error
	keys := make([][]byte, 0, len(encoded))
	seen := map[string]string{}
	for i, e := range encoded {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(e))
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: not valid standard base64", where[i]))
		case len(k) != KeySize:
			errs = append(errs, fmt.Errorf("%s: decoded length is %d bytes, want %d", where[i], len(k), KeySize))
		case insecureKey(k):
			errs = append(errs, fmt.Errorf("%s: is a known insecure key (the published example key, or an all-zero, repeated-byte or incrementing pattern); generate one with `openssl rand -base64 32`", where[i]))
		default:
			if prev, dup := seen[string(k)]; dup {
				errs = append(errs, fmt.Errorf("%s: duplicates %s", where[i], prev))
			}
			seen[string(k)] = where[i]
			keys = append(keys, k)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	s.Keys = keys
	return nil
}

// exampleKeyBytes is the key that older example configs shipped (bytes
// 0x00..0x1f), built at run time so no key-looking literal sits in the source.
// It is public, so it must never protect real tokens. insecureKey's
// constant-step rule already covers it; the explicit match is kept so the
// shipped example key stays rejected even if that heuristic changes.
func exampleKeyBytes() []byte {
	b := make([]byte, KeySize)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// insecureKey reports whether k is a known-weak token key: the published
// example key, or any constant-stride byte sequence (all zero, all the same
// byte, 0,1,2,... and the like).
func insecureKey(k []byte) bool {
	if bytes.Equal(k, exampleKeyBytes()) {
		return true
	}
	for i := 2; i < len(k); i++ {
		if k[i]-k[i-1] != k[1]-k[0] {
			return false
		}
	}
	return len(k) > 0
}

// errInsecureScheme is returned for a non-https URL that is not exempt.
var errInsecureScheme = errors.New("must use https (http is allowed only for loopback hosts or with allow_insecure_http)")

// errGarminInsecureScheme is returned for a non-https, non-loopback
// garmin.base_url, whatever allow_insecure_http says.
var errGarminInsecureScheme = errors.New("must use https (http is allowed only for loopback hosts; allow_insecure_http does not apply because the API key is sent there)")

// checkTransport requires https unless the host is loopback or allowInsecure
// is set. raw must already have passed checkHTTPURL.
func checkTransport(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || strings.EqualFold(u.Scheme, "https") || allowInsecure {
		return nil
	}
	if isLoopbackHost(u.Hostname()) {
		return nil
	}
	return errInsecureScheme
}

// isLoopbackHost reports whether h is localhost or a loopback IP literal.
func isLoopbackHost(h string) bool {
	if strings.EqualFold(strings.TrimSuffix(h, "."), "localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.Unmap().IsLoopback()
}

// normalizePublicURL validates raw and returns the canonical form: lowercase
// scheme and host, default port dropped, no trailing slash. A path is
// rejected: RFC 8414/9728 well-known URIs live at the host root.
func normalizePublicURL(raw string) (string, error) {
	if err := checkHTTPURL(raw, false); err != nil {
		return "", err
	}
	u, _ := url.Parse(raw) // checked above
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	defaultPort := scheme == "http" && port == "80" || scheme == "https" && port == "443"
	if port != "" && !defaultPort {
		host += ":" + port
	}
	if strings.TrimRight(u.EscapedPath(), "/") != "" {
		return "", errors.New("must be an origin without a path (well-known metadata is served at the host root)")
	}
	return scheme + "://" + host, nil
}

// checkHTTPURL requires an absolute http(s) URL without userinfo or fragment
// (and without query unless allowQuery). Errors never include the value, as
// it may carry credentials; the field path identifies it.
func checkHTTPURL(raw string, allowQuery bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must be an absolute http(s) URL")
	}
	if u.User != nil {
		return errors.New("must not contain userinfo")
	}
	if u.Host == "" {
		return errors.New("has no host")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("must not contain a fragment")
	}
	if !allowQuery && (u.RawQuery != "" || strings.Contains(raw, "?")) {
		return errors.New("must not contain a query")
	}
	if strings.HasSuffix(u.Host, ":") {
		return errors.New("has an empty port")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.ParseUint(p, 10, 16); err != nil || n == 0 {
			return errors.New("port must be in 1..65535")
		}
	}
	return nil
}

// parseTrusted parses a CIDR or a bare IP (taken as /32 or /128). IPv4-mapped
// IPv6 notation is rejected: write the IPv4 form instead.
func parseTrusted(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	var p netip.Prefix
	if strings.Contains(raw, "/") {
		var err error
		if p, err = netip.ParsePrefix(raw); err != nil {
			return netip.Prefix{}, errors.New("is not a valid CIDR or IP address")
		}
	} else {
		a, err := netip.ParseAddr(raw)
		if err != nil || a.Zone() != "" {
			return netip.Prefix{}, errors.New("is not a valid CIDR or IP address")
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Addr().Is4In6() {
		return netip.Prefix{}, errors.New("must not use IPv4-mapped IPv6 notation; write the IPv4 form")
	}
	if p.Bits() == 0 {
		return netip.Prefix{}, errors.New("must not match every address (prefix length 0 would make X-Forwarded-For fully spoofable)")
	}
	return p.Masked(), nil
}

func (c *Config) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if _, port, err := net.SplitHostPort(c.Listen); err != nil {
		add("listen: %q must be host:port", c.Listen)
	} else if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		add("listen: port %q must be a number in 0..65535", port)
	}

	if c.PublicURL == "" {
		add("public_url: required")
	} else if _, err := normalizePublicURL(c.PublicURL); err != nil {
		add("public_url: %v", err)
	} else if err := checkTransport(c.PublicURL, c.AllowInsecureHTTP); err != nil {
		add("public_url: %v", err)
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level: %q must be one of debug|info|warn|error", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format: %q must be one of json|text", c.Log.Format)
	}

	errs = append(errs, c.OAuth.Page.validate()...)

	for _, d := range []struct {
		field string
		v     time.Duration
	}{
		{"oauth.access_token_ttl", c.OAuth.AccessTokenTTL},
		{"oauth.refresh_token_ttl", c.OAuth.RefreshTokenTTL},
		{"oauth.max_session_ttl", c.OAuth.MaxSessionTTL},
		{"oauth.code_ttl", c.OAuth.CodeTTL},
	} {
		if d.v <= 0 {
			add("%s: must be a positive duration", d.field)
		}
	}
	if c.OAuth.CodeTTL > MaxCodeTTL {
		add("oauth.code_ttl: %s exceeds the maximum of %s", c.OAuth.CodeTTL, MaxCodeTTL)
	}
	if c.OAuth.AccessTokenTTL > MaxAccessTokenTTL {
		add("oauth.access_token_ttl: %s exceeds the maximum of %s", c.OAuth.AccessTokenTTL, MaxAccessTokenTTL)
	}
	redirects := map[string]int{}
	for i, r := range c.OAuth.AllowedRedirectURIs {
		if err := checkHTTPURL(r, true); err != nil {
			add("oauth.allowed_redirect_uris[%d]: %v", i, err)
		} else if err := checkTransport(r, c.AllowInsecureHTTP); err != nil {
			add("oauth.allowed_redirect_uris[%d]: %v", i, err)
		}
		if j, dup := redirects[r]; dup {
			add("oauth.allowed_redirect_uris[%d]: duplicates [%d]", i, j)
		}
		redirects[r] = i
	}

	if err := checkHTTPURL(c.Garmin.BaseURL, false); err != nil {
		add("garmin.base_url: %v", err)
	} else if checkTransport(c.Garmin.BaseURL, false) != nil {
		// Every user's API key is sent there: allow_insecure_http does not
		// apply, plain http is for a local test server only.
		add("garmin.base_url: %v", errGarminInsecureScheme)
	}
	if c.Garmin.Timeout <= 0 || c.Garmin.Timeout > MaxGarminTimeout {
		add("garmin.timeout: must be a positive duration of at most %s", MaxGarminTimeout)
	}

	for _, l := range []struct {
		field string
		v     Limit
	}{
		{"register", c.OAuth.RateLimit.Register},
		{"authorize", c.OAuth.RateLimit.Authorize},
		{"token", c.OAuth.RateLimit.Token},
		{"authorize_form", c.OAuth.RateLimit.AuthorizeForm},
		{"authorize_global", c.OAuth.RateLimit.AuthorizeGlobal},
		{"key_check_global", c.OAuth.RateLimit.KeyCheckGlobal},
	} {
		if l.v.PerMinute < 0 {
			add("oauth.rate_limit.%s.per_minute: must not be negative", l.field)
		}
		if l.v.Burst < 0 {
			add("oauth.rate_limit.%s.burst: must not be negative", l.field)
		}
	}

	if g := c.OAuth.RateLimit.AuthorizeGlobal; g.PerMinute == 0 && g.Burst > 0 {
		add("oauth.rate_limit.authorize_global.burst: needs per_minute to be set too")
	}

	c.TrustedNets = c.TrustedNets[:0]
	for i, raw := range c.TrustedProxies {
		p, err := parseTrusted(raw)
		if err != nil {
			add("trusted_proxies[%d]: %v", i, err)
			continue
		}
		c.TrustedNets = append(c.TrustedNets, p)
	}

	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		add("tls: cert_file and key_file must be set together")
	}
	if c.TLS.ReloadInterval < 0 {
		add("tls.reload_interval: must be a positive duration")
	}
	errs = append(errs, c.Limits.validate()...)
	return errs
}
