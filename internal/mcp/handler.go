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

// MaxBodyBytes is the largest JSON-RPC message body the handler accepts
// (413 beyond). Callers that read the body first must not read more.
const MaxBodyBytes = 1 << 20

// fallbackVersion answers an initialize with a protocol version this server
// does not support: the latest supported one (MCP lifecycle: the server
// responds with another version it supports, and the client decides).
const fallbackVersion = "2025-11-25"

// supportedVersions are the protocol versions negotiated in initialize and
// accepted in the MCP-Protocol-Version header. 2025-03-26 is left out: it
// requires servers to accept JSON-RPC batches, which this server refuses (a
// batch would carry many tool calls past the per-request limits). Its clients
// get fallbackVersion and decide whether they can speak it.
var supportedVersions = map[string]bool{
	"2024-11-05": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

// protocolVersionHeader carries the negotiated protocol version on every
// request after initialize (MCP 2025-06-18 and later).
const protocolVersionHeader = "MCP-Protocol-Version"

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
	// Result and Error are set on a client's response to a server request.
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
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

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
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
		// Not a single message, so not a request that a JSON-RPC error
		// could answer: an HTTP error status (Streamable HTTP).
		writeRPCStatus(w, http.StatusBadRequest, rpcResponse{Error: &rpcError{codeInvalidRequest, "batch requests are not supported"}})
		return
	}
	var req rpcRequest
	// Valid JSON of the wrong shape (a non-string jsonrpc or method, a
	// non-object message) is an invalid request, not a parse error.
	var te *json.UnmarshalTypeError
	err = json.Unmarshal(trimmed, &req)
	if err != nil && !errors.As(err, &te) {
		writeRPC(w, rpcResponse{Error: &rpcError{codeParse, "parse error"}})
		return
	}
	isRequest := req.Method != "" && len(req.ID) != 0
	// invalid answers a malformed message: a request gets a JSON-RPC error
	// for its id, anything else an HTTP 400 (Streamable HTTP: input that is
	// not a request and is not accepted gets an HTTP error status).
	invalid := func(msg string) {
		if isRequest {
			writeRPC(w, rpcResponse{ID: req.ID, Error: &rpcError{codeInvalidRequest, msg}})
			return
		}
		writeRPCStatus(w, http.StatusBadRequest, rpcResponse{Error: &rpcError{codeInvalidRequest, msg}})
	}
	if err != nil || req.JSONRPC != "2.0" {
		invalid("invalid request")
		return
	}
	// The header is checked on everything but initialize, which negotiates
	// the version in its body. A message without one is served as is: the
	// spec then assumes 2025-03-26, which differs here only in batching,
	// and this server keeps no session that could tell otherwise.
	if v := r.Header.Values(protocolVersionHeader); len(v) != 0 && req.Method != "initialize" &&
		(len(v) != 1 || !supportedVersions[v[0]]) {
		writeRPCStatus(w, http.StatusBadRequest, rpcResponse{Error: &rpcError{codeInvalidRequest, "unsupported " + protocolVersionHeader}})
		return
	}
	switch {
	case req.Method == "":
		// A client response (to a server request, of which this server
		// sends none) carries exactly one of result and error.
		if (len(req.Result) != 0) == (len(req.Error) != 0) {
			invalid("invalid request")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	case !isRequest || strings.HasPrefix(req.Method, "notifications/"):
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
	writeRPCStatus(w, http.StatusOK, resp)
}

func writeRPCStatus(w http.ResponseWriter, status int, resp rpcResponse) {
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
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
