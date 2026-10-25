// Package garmin is a read-only client for the Garmin Connect Personal API.
package garmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	exchangeEndpoint = "/personalapitoken-service/personal-api/personal-api-key/token"
	maxBodyBytes     = 8 << 20
	maxTokenBytes    = 64 << 10
	// The exchange endpoint serves text/plain and answers 406 to a bare
	// "application/json"; data calls stay liberal for the same reason.
	acceptExchange   = "text/plain, */*"
	acceptData       = "application/json, */*;q=0.1"
	defaultUserAgent = "garmin-papi-mcp"
	// maxRetryAfter caps a Retry-After from Garmin (it is shown to the model
	// and must not overflow time.Duration).
	maxRetryAfter = time.Hour
)

// Option configures a Client.
type Option func(*Client)

// WithClock overrides the time source (tests).
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// Client talks to Garmin on behalf of many API keys, caching one JWT per key.
type Client struct {
	baseURL   string
	hc        *http.Client
	now       func() time.Time
	userAgent string
	cache     *tokenCache
}

// NewClient builds a Client for baseURL (no trailing slash needed).
func NewClient(baseURL string, hc *http.Client, opts ...Option) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	c := &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		hc:        hc,
		now:       time.Now,
		userAgent: defaultUserAgent,
		cache:     newTokenCache(maxCacheEntries),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Validate forces a key exchange, reporting whether Garmin accepts the key.
func (c *Client) Validate(ctx context.Context, apiKey string) error {
	_, err := c.token(ctx, apiKey, true)
	return err
}

// Get performs an authenticated GET and returns the response for any 2xx.
func (c *Client) Get(ctx context.Context, apiKey, path string, query url.Values) (Response, error) {
	target := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	key := hashKey(apiKey)
	for attempt := 0; ; attempt++ {
		jwt, err := c.token(ctx, apiKey, false)
		if err != nil {
			return Response{}, err
		}
		resp, status, retryAfter, err := c.do(ctx, http.MethodGet, target, "Bearer "+jwt, acceptData, maxBodyBytes)
		if err != nil {
			return Response{}, err
		}
		if status == http.StatusUnauthorized {
			if attempt == 0 && !c.cache.scoped(key, jwt) {
				// The JWT may have been revoked or expired early: drop it
				// and try once more with a fresh one. If that exchange
				// answers 401, the key itself is rejected (ErrKeyRejected).
				c.cache.invalidate(key, jwt)
				continue
			}
			// The exchange that just succeeded proves the key works, so a
			// 401 with the fresh JWT is about this endpoint (a scope the key
			// lacks, say): a plain status error, not a rejected key. The
			// fresh JWT stays cached; it is valid. It is marked scoped, and
			// a 401 with a scoped JWT fails the same way at once, without
			// dropping it: otherwise every call to such an endpoint would
			// exchange the key again (and evict the JWT other calls use).
			c.cache.markScoped(key, jwt)
			return Response{}, &StatusError{Status: status}
		}
		if err := statusErr(status, retryAfter); err != nil {
			return Response{}, err
		}
		return resp, nil
	}
}

func (c *Client) token(ctx context.Context, apiKey string, force bool) (string, error) {
	return c.cache.get(ctx, hashKey(apiKey), force, c.now, func(ctx context.Context) (string, error) {
		resp, status, retryAfter, err := c.do(ctx, http.MethodPost, c.baseURL+exchangeEndpoint, "ApiKey "+apiKey, acceptExchange, maxTokenBytes)
		if err != nil {
			return "", err
		}
		if status == http.StatusUnauthorized {
			return "", ErrKeyRejected
		}
		if err := statusErr(status, retryAfter); err != nil {
			return "", err
		}
		jwt := strings.TrimSpace(string(resp.Body))
		if jwt == "" {
			return "", errors.New("garmin: empty token in exchange response")
		}
		return jwt, nil
	})
}

// do sends one request and reads at most limit bytes of body. It returns the
// raw status even for non-2xx; errors are transport/body-limit failures only.
func (c *Client) do(ctx context.Context, method, target, auth, accept string, limit int64) (Response, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return Response{}, 0, 0, errors.New("garmin: bad request")
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", accept)
	res, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, 0, 0, ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop URL, keep cause
		}
		return Response{}, 0, 0, fmt.Errorf("garmin: request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, 0, 0, ctx.Err()
		}
		return Response{}, 0, 0, fmt.Errorf("garmin: reading response: %w", err)
	}
	if int64(len(body)) > limit {
		return Response{}, 0, 0, fmt.Errorf("garmin: response exceeds %d bytes", limit)
	}
	retryAfter := parseRetryAfter(res.Header.Get("Retry-After"), c.now())
	return Response{Status: res.StatusCode, Body: body, ContentType: res.Header.Get("Content-Type")}, res.StatusCode, retryAfter, nil
}

// parseRetryAfter reads a Retry-After value in either form (delay-seconds or
// HTTP-date, RFC 9110 §10.2.3), relative to now. It returns 0 for a missing,
// malformed or past value and caps the result at maxRetryAfter.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.ParseUint(v, 10, 64); err == nil {
		if secs >= uint64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		d = time.Duration(secs) * time.Second
	} else if errors.Is(err, strconv.ErrRange) {
		return maxRetryAfter // all digits, too large for uint64
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxRetryAfter)
}

func statusErr(status int, retryAfter time.Duration) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusTooManyRequests:
		return &RateLimitedError{RetryAfter: retryAfter}
	default:
		return &StatusError{Status: status}
	}
}
