package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

// mapErr turns a Garmin client error into a tool outcome. A rejected key is
// returned as ErrUnauthorized so the MCP handler can answer HTTP 401; every
// other failure becomes an error result that never carries response bodies.
func mapErr(err error) (Result, error) {
	var rl *garmin.RateLimitedError
	var se *garmin.StatusError
	switch {
	case errors.Is(err, garmin.ErrKeyRejected):
		return Result{}, ErrUnauthorized
	case errors.As(err, &rl):
		msg := "Garmin is rate limiting this account, retry later"
		if rl.RetryAfter > 0 {
			msg = fmt.Sprintf("Garmin is rate limiting this account, retry in %ds", int(rl.RetryAfter/time.Second))
		}
		return Result{Errors: []string{msg}}, nil
	case errors.As(err, &se):
		return Result{Errors: []string{fmt.Sprintf("Garmin returned HTTP %d", se.Status)}}, nil
	default:
		return Result{Errors: []string{"could not reach Garmin"}}, nil
	}
}

// fetchJSON performs a GET and decodes the JSON body into data. noData is true
// for a 204 or a body that is empty, null, [] or {}. On a failed call res holds
// the error result (or err is ErrUnauthorized); callers check err first, then
// res.Errors, then noData.
func fetchJSON(ctx context.Context, c Getter, apiKey, path string, q url.Values) (data any, noData bool, res Result, err error) {
	resp, gerr := c.Get(ctx, apiKey, path, q)
	if gerr != nil {
		res, err = mapErr(gerr)
		return nil, false, res, err
	}
	body := strings.TrimSpace(string(resp.Body))
	if resp.Status == 204 || body == "" || body == "null" || body == "[]" || body == "{}" {
		return nil, true, Result{}, nil
	}
	if jerr := json.Unmarshal(resp.Body, &data); jerr != nil {
		return nil, false, Result{Errors: []string{"Garmin returned an unreadable response"}}, nil
	}
	return data, false, Result{}, nil
}
