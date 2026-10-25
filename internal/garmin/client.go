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
	defaultUserAgent = "garmin-papi-mcp"
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
	for attempt := 0; ; attempt++ {
		jwt, err := c.token(ctx, apiKey, false)
		if err != nil {
			return Response{}, err
		}
		resp, status, retryAfter, err := c.do(ctx, http.MethodGet, target, "Bearer "+jwt, maxBodyBytes)
		if err != nil {
			return Response{}, err
		}
		if status == http.StatusUnauthorized {
			if attempt == 0 {
				c.cache.invalidate(hashKey(apiKey), jwt)
				continue
			}
			return Response{}, ErrKeyRejected
		}
		if err := statusErr(status, retryAfter); err != nil {
			return Response{}, err
		}
		return resp, nil
	}
}

func (c *Client) token(ctx context.Context, apiKey string, force bool) (string, error) {
	return c.cache.get(ctx, hashKey(apiKey), force, c.now, func(ctx context.Context) (string, error) {
		resp, status, retryAfter, err := c.do(ctx, http.MethodPost, c.baseURL+exchangeEndpoint, "ApiKey "+apiKey, maxTokenBytes)
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
func (c *Client) do(ctx context.Context, method, target, auth string, limit int64) (Response, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return Response{}, 0, 0, errors.New("garmin: bad request")
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
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
	defer res.Body.Close()
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
	var retryAfter time.Duration
	if secs, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && secs > 0 {
		retryAfter = time.Duration(secs) * time.Second
	}
	return Response{Status: res.StatusCode, Body: body, ContentType: res.Header.Get("Content-Type")}, res.StatusCode, retryAfter, nil
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
