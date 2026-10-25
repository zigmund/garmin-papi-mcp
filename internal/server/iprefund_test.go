package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
)

// Final-review item 3: claude.ai and ChatGPT call /mcp for all their users
// from a few egress addresses, so the per-IP limit must not cap authenticated
// traffic. The IP tokens of a request whose bearer token authenticates are
// given back; the per-grant limit governs it.
func TestMCPPerIPLimitRefundedForAuthenticated(t *testing.T) {
	s := newStack(t, stackOpts{yaml: "limits: { ip: { per_minute: 1, burst: 2 }, token: { per_minute: 1, burst: 10 } }\n"})
	l := s.login(t)
	for i := range 10 {
		if resp, _ := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusOK {
			t.Fatalf("authenticated request %d = %d, want 200 (the IP bucket holds only 2)", i, resp.StatusCode)
		}
	}
	// The per-grant limit still applies.
	resp, _ := s.rpc(t, l.access, initializeBody)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("beyond the per-grant burst = %d, want 429", resp.StatusCode)
	}
	// Unauthenticated traffic from the same IP is still limited: the
	// authenticated requests left the IP bucket full (2), no more.
	for i := range 2 {
		if resp, _ := s.rpc(t, "", initializeBody); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated request %d = %d, want 401", i, resp.StatusCode)
		}
	}
	if resp, _ := s.rpc(t, "", initializeBody); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third unauthenticated request = %d, want 429", resp.StatusCode)
	}
}

// Invalid tokens are charged against the client IP like no token at all, so
// they cannot be used to get around the per-IP limit; once the bucket is
// empty even a valid token is refused (the IP is charged before the token is
// opened).
func TestMCPPerIPLimitChargesInvalidTokens(t *testing.T) {
	s := newStack(t, stackOpts{yaml: "limits: { ip: { per_minute: 1, burst: 2 } }\n"})
	l := s.login(t)
	for i := range 2 {
		if resp, _ := s.rpc(t, "not-a-token", initializeBody); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("invalid token %d = %d, want 401", i, resp.StatusCode)
		}
	}
	if resp, _ := s.rpc(t, "not-a-token", initializeBody); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third invalid token = %d, want 429", resp.StatusCode)
	}
	if resp, _ := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("valid token with an empty IP bucket = %d, want 429", resp.StatusCode)
	}
}

// A request whose body cannot be read stays charged against the client IP,
// even with a valid token: the refund comes only once the body is in, so a
// stream of broken or abandoned bodies is still capped per IP.
func TestMCPPerIPLimitChargesUnreadableBody(t *testing.T) {
	s := newStack(t, stackOpts{yaml: "limits: { ip: { per_minute: 1, burst: 2 } }\n"})
	l := s.login(t)

	conn, err := net.Dial("tcp", strings.TrimPrefix(s.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// Content-Length promises more than is sent; the write side is closed, so
	// the server's read ends early.
	_, err = fmt.Fprintf(conn, "POST /mcp HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"jsonrpc\":", strings.TrimPrefix(s.url, "http://"), l.access)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("truncated body = %d, want 400", resp.StatusCode)
	}

	// One IP token is left: an authenticated request passes (and is refunded),
	// one unauthenticated request uses it up, the next is refused.
	if resp, _ := s.rpc(t, l.access, initializeBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request = %d, want 200", resp.StatusCode)
	}
	if resp, _ := s.rpc(t, "", initializeBody); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d, want 401", resp.StatusCode)
	}
	if resp, _ := s.rpc(t, "", initializeBody); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("next unauthenticated request = %d, want 429 (the truncated body stayed charged)", resp.StatusCode)
	}
}
