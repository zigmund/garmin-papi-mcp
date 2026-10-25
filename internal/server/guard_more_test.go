package server

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/mcp"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// Review item 4: the guard never reads more than the MCP handler accepts.
func TestMCPBodyCapIsAtMostMCPLimit(t *testing.T) {
	for _, tc := range []struct {
		extra string
		want  int64
	}{
		{"", config.DefaultMaxRequestBody}, // the configured cap is the effective one
		{"limits: { max_request_body: 2048 }\n", 2048},
		{"limits: { max_request_body: 65536 }\n", config.MaxMaxRequestBody},
	} {
		cfg := parseCfg(t, testYAML+tc.extra)
		g := newMCPGuard(cfg, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler), nil)
		if got := g.bodyCap(); got != tc.want || got > mcp.MaxBodyBytes {
			t.Errorf("%q: body cap = %d, want %d (at most %d)", tc.extra, got, tc.want, mcp.MaxBodyBytes)
		}
	}
	// End to end: a Content-Length just above the default cap is refused
	// before the body is read; a body at the cap passes.
	s := newStack(t, stackOpts{})
	l := s.login(t)
	pad := func(n int) string {
		const frame = `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":""}}`
		return `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", n-len(frame)) + `"}}`
	}
	if resp, _ := s.rpc(t, l.access, pad(config.DefaultMaxRequestBody+1)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("body above the default cap = %d, want 413", resp.StatusCode)
	}
	if resp, body := s.rpc(t, l.access, pad(config.DefaultMaxRequestBody)); resp.StatusCode != http.StatusOK {
		t.Errorf("body at the default cap = %d %s, want 200", resp.StatusCode, body)
	}
}

// slowBody opens a raw connection that sends the headers of an authenticated
// MCP request and only part of its body, then stalls.
func slowBody(t *testing.T, s *stack, token string) net.Conn {
	t.Helper()
	u := strings.TrimPrefix(s.url, "http://")
	conn, err := net.Dial("tcp", u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, err = io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: "+u+"\r\nAuthorization: Bearer "+token+
		"\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(initializeBody))+"\r\n\r\n"+initializeBody[:10])
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// Review item 3, revised: the body is read under the grant's slots, so a
// grant cannot have more bodies being read at once than
// max_concurrent_per_token. A client stalling mid-body holds its slot until
// the body read deadline cuts it off, then gives it back.
func TestSlowBodyHoldsSlotUntilDeadline(t *testing.T) {
	old := mcpBodyReadTimeout
	mcpBodyReadTimeout = 500 * time.Millisecond
	t.Cleanup(func() { mcpBodyReadTimeout = old })

	s := newStack(t, stackOpts{yaml: "limits: { max_concurrent_per_token: 1 }\n"})
	l := s.login(t)
	conn := slowBody(t, s, l.access)
	time.Sleep(100 * time.Millisecond) // the server is now waiting for the body

	// The grant's only slot is taken by the stalled body.
	if resp, body := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request next to a stalled body = %d %s, want 429", resp.StatusCode, body)
	}

	// The stalled request is answered (or dropped) about at the deadline.
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err == nil && !strings.HasPrefix(line, "HTTP/1.1 4") {
		t.Errorf("stalled request answered %q", line)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the server kept waiting for the body")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("cut off after %v", time.Since(start))
	}

	// The slot is free again.
	if resp, body := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("request after the cut-off = %d %s, want 200", resp.StatusCode, body)
	}
}

// The global in-flight cap also bounds the bodies being read: a stalled
// body holds a global slot, so another grant gets 503 until it is cut off.
func TestSlowBodyHoldsGlobalSlot(t *testing.T) {
	old := mcpBodyReadTimeout
	mcpBodyReadTimeout = 500 * time.Millisecond
	t.Cleanup(func() { mcpBodyReadTimeout = old })

	s := newStack(t, stackOpts{yaml: "limits: { max_in_flight: 1 }\n"})
	l := s.login(t)
	conn := slowBody(t, s, l.access)
	time.Sleep(100 * time.Millisecond)

	if resp, _ := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("request next to a stalled body = %d, want 503", resp.StatusCode)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = bufio.NewReader(conn).ReadString('\n')
	if resp, body := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("request after the cut-off = %d %s, want 200", resp.StatusCode, body)
	}
}

// Review item 5: more Origin forms that must be refused.
func TestMCPOriginValidationMore(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	host := strings.TrimPrefix(s.url, "http://")
	ip, port, _ := net.SplitHostPort(host)
	otherPort := "1"
	if port == "1" {
		otherPort = "2"
	}
	for _, origin := range []string{
		"http://" + ip + ":" + otherPort, // same host, other port
		"http://" + ip,                   // same host, default port
		"https://" + host,                // other scheme
		"http://user@" + host,            // userinfo
		"http://" + ip + ".:" + port,     // trailing dot
		s.url + "/",                      // a path is not an origin... but "/" is tolerated below
	} {
		resp, _ := s.rpc(t, l.access, initializeBody, "Origin", origin)
		want := http.StatusForbidden
		if origin == s.url+"/" {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Errorf("Origin %q = %d, want %d", origin, resp.StatusCode, want)
		}
	}
	// Two Origin header lines, even both correct, are refused.
	req, _ := http.NewRequest(http.MethodPost, s.url+"/mcp", strings.NewReader(initializeBody))
	req.Header.Set("Authorization", "Bearer "+l.access)
	req.Header.Add("Origin", s.url)
	req.Header.Add("Origin", s.url)
	if resp, _ := s.do(t, req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("two Origin lines = %d, want 403", resp.StatusCode)
	}
}

// Review item 5: a panicking tool gives its concurrency slot back.
func TestToolPanicReleasesSlot(t *testing.T) {
	s := newStack(t, stackOpts{
		yaml:  "limits: { max_concurrent_per_token: 1, max_in_flight: 1 }\n",
		tools: append(tools.All(), explodingTool()),
	})
	l := s.login(t)
	for i := range 3 {
		if res := s.call(t, l.access, "explode", "{}"); !res.IsError {
			t.Fatalf("call %d = %+v", i, res)
		}
	}
	if res := s.call(t, l.access, "get_profile", "{}"); res.IsError {
		t.Fatalf("after panics = %+v", res)
	}
}
