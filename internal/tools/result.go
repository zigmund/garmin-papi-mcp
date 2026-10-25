// Package tools holds the MCP tool framework and shared helpers.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

// ErrUnauthorized is returned by a tool handler (or the auth callback) when the
// caller's Garmin API key was rejected. The MCP handler answers it with HTTP 401.
var ErrUnauthorized = errors.New("unauthorized")

// Getter performs authenticated GET requests against the Garmin API.
type Getter interface {
	Get(ctx context.Context, apiKey, path string, query url.Values) (garmin.Response, error)
}

// Tool is a single read-only MCP tool.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, c Getter, apiKey string, args json.RawMessage) (Result, error)
}

// Result is the outcome of a tool call.
type Result struct {
	Data   any
	Note   string
	Errors []string
}

// NoData returns a successful result that carries only an explanatory note.
func NoData(msg string) Result { return Result{Note: msg} }

// IsError reports whether the result is a failure: errors present and no data.
func (r Result) IsError() bool { return len(r.Errors) > 0 && r.Data == nil }
