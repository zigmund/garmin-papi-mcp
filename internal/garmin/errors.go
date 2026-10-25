package garmin

import (
	"errors"
	"fmt"
	"time"
)

// ErrKeyRejected means Garmin refused the API key: the key-to-JWT exchange
// answered 401 (also when it was retried after a data call's 401).
var ErrKeyRejected = errors.New("garmin: api key rejected")

// RateLimitedError is returned for HTTP 429. RetryAfter is zero when Garmin
// sent no usable Retry-After header.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("garmin: rate limited, retry after %s", e.RetryAfter)
	}
	return "garmin: rate limited"
}

// StatusError is returned for any non-2xx status other than 429, including a
// data call's 401 that persists with a freshly exchanged JWT, and any later
// 401 with that JWT (the key works, the endpoint refuses it). The response
// body is deliberately not kept.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("garmin: unexpected status %d", e.Status)
}
