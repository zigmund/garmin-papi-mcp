package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

type fakeGetter struct{}

func (fakeGetter) Get(context.Context, string, string, url.Values) (garmin.Response, error) {
	return garmin.Response{Status: 200}, nil
}

func testTools() []tools.Tool {
	return []tools.Tool{
		{
			Name:        "echo",
			Description: "echo tool",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(ctx context.Context, c tools.Getter, apiKey string, args json.RawMessage) (tools.Result, error) {
				return tools.Result{Data: map[string]any{"key": apiKey, "n": 1}, Note: "hello"}, nil
			},
		},
		{
			Name:        "bad",
			Description: "always fails",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, tools.Getter, string, json.RawMessage) (tools.Result, error) {
				return tools.Result{}, errors.New("secret-token-123 leaked")
			},
		},
		{
			Name:        "soft",
			Description: "result errors",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, tools.Getter, string, json.RawMessage) (tools.Result, error) {
				return tools.Result{Errors: []string{"upstream 500"}}, nil
			},
		},
		{
			Name:        "denied",
			Description: "key rejected",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, tools.Getter, string, json.RawMessage) (tools.Result, error) {
				return tools.Result{}, tools.ErrUnauthorized
			},
		},
	}
}

func newTestHandler() http.Handler {
	auth := func(r *http.Request) (string, error) { return "k1", nil }
	return NewHandler(testTools(), fakeGetter{}, auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postVersion(t, h, body)
}

// postVersion posts body with one MCP-Protocol-Version line per version.
func postVersion(t *testing.T, h http.Handler, body string, versions ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, v := range versions {
		req.Header.Add("MCP-Protocol-Version", v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json %q: %v", rec.Body.String(), err)
	}
	return m
}

func errCode(t *testing.T, m map[string]any) float64 {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error in %v", m)
	}
	return e["code"].(float64)
}

func TestHandlerInitialize(t *testing.T) {
	h := newTestHandler()
	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	m := decode(t, rec)
	res := m["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("version %v", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities %v", res["capabilities"])
	}
	if res["serverInfo"].(map[string]any)["name"] != "garmin-papi-mcp" {
		t.Fatalf("serverInfo %v", res["serverInfo"])
	}
	if m["id"].(float64) != 1 {
		t.Fatalf("id %v", m["id"])
	}

	rec = post(t, h, `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	res = decode(t, rec)["result"].(map[string]any)
	// An unknown version gets the latest supported one (the client then
	// decides whether it can speak it).
	if res["protocolVersion"] != "2025-11-25" {
		t.Fatalf("fallback version %v, want the latest supported 2025-11-25", res["protocolVersion"])
	}

	for _, v := range []string{"2024-11-05", "2025-11-25"} {
		res = decode(t, post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+v+`"}}`))["result"].(map[string]any)
		if res["protocolVersion"] != v {
			t.Fatalf("version %v, want %s", res["protocolVersion"], v)
		}
	}
	// 2025-03-26 requires accepting batches, which this server refuses: it
	// is not negotiated.
	res = decode(t, post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`))["result"].(map[string]any)
	if res["protocolVersion"] != "2025-11-25" {
		t.Fatalf("2025-03-26 negotiated as %v, want the fallback 2025-11-25", res["protocolVersion"])
	}
}

func TestHandlerProtocolVersionHeader(t *testing.T) {
	h := newTestHandler()
	const ping = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for _, v := range [][]string{nil, {"2024-11-05"}, {"2025-06-18"}, {"2025-11-25"}} {
		rec := postVersion(t, h, ping, v...)
		if rec.Code != 200 || decode(t, rec)["result"] == nil {
			t.Fatalf("%q: %d %q", v, rec.Code, rec.Body.String())
		}
	}
	for _, v := range [][]string{{"1999-01-01"}, {"2025-03-26"}, {""}, {"2025-06-18, 2025-11-25"}, {"2025-06-18", "2025-11-25"}} {
		rec := postVersion(t, h, ping, v...)
		if rec.Code != 400 {
			t.Fatalf("%q: status %d, want 400", v, rec.Code)
		}
		if m := decode(t, rec); errCode(t, m) != -32600 || m["id"] != nil {
			t.Fatalf("%q: body %v", v, m)
		}
		// Notifications are refused the same way.
		if rec = postVersion(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, v...); rec.Code != 400 {
			t.Fatalf("%q notification: status %d, want 400", v, rec.Code)
		}
	}
	// initialize negotiates in its body; the header does not apply to it.
	rec := postVersion(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "1999-01-01")
	if rec.Code != 200 || decode(t, rec)["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerJSONRPCVersion(t *testing.T) {
	h := newTestHandler()
	for _, body := range []string{
		`{"jsonrpc":"1.0","id":3,"method":"ping"}`,
		`{"id":3,"method":"ping"}`,
		`{"jsonrpc":2.0,"id":3,"method":"ping"}`,
	} {
		rec := post(t, h, body)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
		m := decode(t, rec)
		if errCode(t, m) != -32600 || m["id"] != float64(3) || m["result"] != nil {
			t.Fatalf("%s: %v", body, m)
		}
	}
	// Without a request to answer (a notification, a message that is not an
	// object or has no usable method): an HTTP error status.
	for _, body := range []string{
		`{"jsonrpc":"1.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":7}`,
		`"ping"`,
		`null`,
	} {
		rec := post(t, h, body)
		if rec.Code != 400 || errCode(t, decode(t, rec)) != -32600 {
			t.Fatalf("%s: %d %q", body, rec.Code, rec.Body.String())
		}
	}
}

func TestHandlerClientResponseAccepted(t *testing.T) {
	h := newTestHandler()
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":5,"result":{}}`,
		`{"jsonrpc":"2.0","id":"s-1","error":{"code":-32601,"message":"no"}}`,
		`{"jsonrpc":"2.0","id":5,"result":null}`,
	} {
		if rec := post(t, h, body); rec.Code != 202 || rec.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", body, rec.Code, rec.Body.String())
		}
	}
	// A message with neither method nor exactly one of result and error is
	// no response.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":5}`,
		`{"jsonrpc":"2.0","id":5,"result":{},"error":{"code":1,"message":"x"}}`,
		`{"jsonrpc":"2.0"}`,
	} {
		rec := post(t, h, body)
		if rec.Code != 400 || errCode(t, decode(t, rec)) != -32600 {
			t.Fatalf("%s: %d %q", body, rec.Code, rec.Body.String())
		}
	}
}

func TestHandlerToolsListAnnotated(t *testing.T) {
	rec := post(t, newTestHandler(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := decode(t, rec)["result"].(map[string]any)["tools"].([]any)
	if len(list) != 4 {
		t.Fatalf("len %d", len(list))
	}
	for _, it := range list {
		tool := it.(map[string]any)
		ann := tool["annotations"].(map[string]any)
		if ann["readOnlyHint"] != true {
			t.Fatalf("not read-only: %v", tool)
		}
		if tool["name"] == "" || tool["inputSchema"] == nil || tool["description"] == "" {
			t.Fatalf("incomplete tool %v", tool)
		}
	}
}

func TestHandlerToolsCallErrorIsResultNotProtocolError(t *testing.T) {
	h := newTestHandler()

	rec := post(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bad","arguments":{}}}`)
	m := decode(t, rec)
	if m["error"] != nil {
		t.Fatalf("protocol error: %v", m)
	}
	res := m["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("isError %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "secret-token") || text == "" {
		t.Fatalf("text %q", text)
	}

	rec = post(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"soft"}}`)
	res = decode(t, rec)["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("soft: %v", res)
	}
	if got := res["content"].([]any)[0].(map[string]any)["text"]; got != "error: upstream 500" {
		t.Fatalf("soft text %q", got)
	}

	rec = post(t, h, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	res = decode(t, rec)["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("echo isError: %v", res)
	}
	got := res["content"].([]any)[0].(map[string]any)
	if got["type"] != "text" || got["text"] != "hello\n{\"key\":\"k1\",\"n\":1}" {
		t.Fatalf("echo text %q", got["text"])
	}
}

func TestHandlerToolsCallProtocolErrors(t *testing.T) {
	h := newTestHandler()
	if c := errCode(t, decode(t, post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`))); c != -32602 {
		t.Fatalf("unknown tool code %v", c)
	}
	if c := errCode(t, decode(t, post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"x"}`))); c != -32602 {
		t.Fatalf("bad params code %v", c)
	}
	if c := errCode(t, decode(t, post(t, h, `{"jsonrpc":"2.0","id":1,"method":"nope"}`))); c != -32601 {
		t.Fatalf("unknown method code %v", c)
	}
	if c := errCode(t, decode(t, post(t, h, `{not json`))); c != -32700 {
		t.Fatalf("parse code %v", c)
	}
}

func TestHandlerPing(t *testing.T) {
	m := decode(t, post(t, newTestHandler(), `{"jsonrpc":"2.0","id":9,"method":"ping"}`))
	if r, ok := m["result"].(map[string]any); !ok || len(r) != 0 {
		t.Fatalf("ping %v", m)
	}
}

func TestHandlerNotificationAccepted(t *testing.T) {
	rec := post(t, newTestHandler(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != 202 || rec.Body.Len() != 0 {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerRejectsGET(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(m, "/mcp", nil)
		rec := httptest.NewRecorder()
		newTestHandler().ServeHTTP(rec, req)
		if rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
			t.Fatalf("%s: %d allow=%q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestHandlerBatchRejected(t *testing.T) {
	rec := post(t, newTestHandler(), `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	if rec.Code != 400 {
		t.Fatalf("status %d", rec.Code)
	}
	if c := errCode(t, decode(t, rec)); c != -32600 {
		t.Fatalf("code %v", c)
	}
}

func TestHandlerBodyLimit(t *testing.T) {
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", 1<<20) + `"}}`
	rec := post(t, newTestHandler(), big)
	if rec.Code == 200 && decode(t, rec)["result"] != nil {
		t.Fatal("oversized body was processed")
	}
	if rec.Code != 413 && rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHandlerUnauthorized(t *testing.T) {
	// auth error
	h := NewHandler(testTools(), fakeGetter{}, func(*http.Request) (string, error) {
		return "", tools.ErrUnauthorized
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if rec.Code != 401 || rec.Body.Len() != 0 {
		t.Fatalf("auth: %d %q", rec.Code, rec.Body.String())
	}
	// tool-level key rejection
	rec = post(t, newTestHandler(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"denied"}}`)
	if rec.Code != 401 || rec.Body.Len() != 0 {
		t.Fatalf("tool: %d %q", rec.Code, rec.Body.String())
	}
}
