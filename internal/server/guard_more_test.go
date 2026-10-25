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

	"github.com/zigmund/garmin-papi-mcp/internal/mcp"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// Review item 4: the guard never reads more than the MCP handler accepts.
func TestMCPBodyCapIsAtMostMCPLimit(t *testing.T) {
	for _, tc := range []struct {
		extra string
		want  int64
	}{
		{"", mcp.MaxBodyBytes}, // default 10 MiB is capped at the handler's limit
		{"limits: { max_request_body: 2048 }\n", 2048},
		{"limits: { max_request_body: 104857600 }\n", mcp.MaxBodyBytes},
	} {
		cfg := parseCfg(t, testYAML+tc.extra)
		g := newMCPGuard(cfg, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler), nil)
		if got := g.bodyCap(); got != tc.want {
			t.Errorf("%q: body cap = %d, want %d", tc.extra, got, tc.want)
		}
	}
	// End to end: a Content-Length just above the handler's limit is refused
	// before the body is read, with the default config.
	s := newStack(t, stackOpts{})
	l := s.login(t)
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", int(mcp.MaxBodyBytes)) + `"}}`
	if resp, _ := s.rpc(t, l.access, big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("body above the MCP limit = %d, want 413", resp.StatusCode)
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

// Review item 3: a client stalling mid-body holds no per-token or global
// slot, and is cut off by the body read deadline.
func TestSlowBodyHoldsNoSlot(t *testing.T) {
	old := mcpBodyReadTimeout
	mcpBodyReadTimeout = 500 * time.Millisecond
	t.Cleanup(func() { mcpBodyReadTimeout = old })

	s := newStack(t, stackOpts{yaml: "limits: { max_concurrent_per_token: 1, max_in_flight: 1 }\n"})
	l := s.login(t)
	conn := slowBody(t, s, l.access)
	time.Sleep(100 * time.Millisecond) // the server is now waiting for the body

	// The same grant, and the only global slot, are still available.
	if resp, body := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("request next to a stalled body = %d %s, want 200", resp.StatusCode, body)
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
