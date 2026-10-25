// Package mcp implements a stateless MCP Streamable HTTP endpoint (JSON only).
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// Version is the server version reported in initialize; set at build time.
var Version = "dev"

const (
	maxBodyBytes    = 1 << 20
	fallbackVersion = "2025-06-18"
)

var supportedVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type handler struct {
	reg  *registry
	g    tools.Getter
	auth func(r *http.Request) (string, error)
	log  *slog.Logger
}

// NewHandler returns the MCP HTTP handler. auth resolves the caller's Garmin
// API key; errors wrapping tools.ErrUnauthorized (from auth or a tool) become
// an empty HTTP 401.
func NewHandler(reg []tools.Tool, g tools.Getter, auth func(r *http.Request) (apiKey string, err error), log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &handler{reg: newRegistry(reg), g: g, auth: auth, log: log}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	apiKey, err := h.auth(r)
	if err != nil {
		h.log.Info("auth rejected", "error_type", fmt.Sprintf("%T", err))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		writeRPC(w, rpcResponse{Error: &rpcError{codeParse, "parse error"}})
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, rpcResponse{Error: &rpcError{codeInvalidRequest, "batch requests are not supported"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeRPC(w, rpcResponse{Error: &rpcError{codeParse, "parse error"}})
		return
	}
	// Notifications (no id) and client responses (no method) are acknowledged.
	if req.Method == "" || strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
		if req.Method == "" && len(req.ID) != 0 {
			writeRPC(w, rpcResponse{ID: req.ID, Error: &rpcError{codeInvalidRequest, "invalid request"}})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = h.initialize(req.Params)
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": h.reg.descriptors()}
	case "tools/call":
		res, rerr, unauthorized := h.call(r, apiKey, req.Params)
		if unauthorized {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		resp.Result, resp.Error = res, rerr
	default:
		resp.Error = &rpcError{codeMethodNotFound, "method not found"}
	}
	writeRPC(w, resp)
}

func (h *handler) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	v := fallbackVersion
	if supportedVersions[p.ProtocolVersion] {
		v = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": v,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "garmin-papi-mcp", "version": Version},
	}
}

func (h *handler) call(r *http.Request, apiKey string, params json.RawMessage) (any, *rpcError, bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{codeInvalidParams, "invalid params"}, false
	}
	tool, ok := h.reg.byName[p.Name]
	if !ok {
		return nil, &rpcError{codeInvalidParams, "unknown tool"}, false
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := tool.Handler(r.Context(), h.g, apiKey, args)
	if err != nil {
		if errors.Is(err, tools.ErrUnauthorized) {
			h.log.Info("tool key rejected", "tool", p.Name)
			return nil, nil, true
		}
		h.log.Warn("tool failed", "tool", p.Name, "error_type", fmt.Sprintf("%T", err))
		return toolResult("tool call failed", true), nil, false
	}
	text, merr := renderText(res)
	if merr != nil {
		h.log.Warn("tool result encoding failed", "tool", p.Name, "error_type", fmt.Sprintf("%T", merr))
		return toolResult("tool call failed", true), nil, false
	}
	return toolResult(text, res.IsError()), nil, false
}

func renderText(res tools.Result) (string, error) {
	var lines []string
	if res.Note != "" {
		lines = append(lines, res.Note)
	}
	if res.Data != nil {
		b, err := json.Marshal(res.Data)
		if err != nil {
			return "", err
		}
		lines = append(lines, string(b))
	}
	for _, e := range res.Errors {
		lines = append(lines, "error: "+e)
	}
	return strings.Join(lines, "\n"), nil
}

func toolResult(text string, isError bool) map[string]any {
	m := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	if isError {
		m["isError"] = true
	}
	return m
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	resp.JSONRPC = "2.0"
	if len(resp.ID) == 0 {
		resp.ID = json.RawMessage("null")
	}
	b, err := json.Marshal(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
