package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// testReadTimeout replaces ReadTimeout in these tests.
const testReadTimeout = 300 * time.Millisecond

// accessToken seals an access token for testAPIKey with the stack's sealer,
// for clients that cannot run the OAuth flow (e.g. over built-in TLS).
func (s *stack) accessToken(t *testing.T) string {
	t.Helper()
	tok, err := s.sealer.Seal(seal.KindAccess, tokens.Access{ClientID: "c", APIKey: testAPIKey}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// slowTool answers after d (or when its call is cancelled).
func slowTool(d time.Duration) tools.Tool {
	return tools.Tool{
		Name:        "slow",
		Description: "answers late",
		Handler: func(ctx context.Context, _ tools.Getter, _ string, _ json.RawMessage) (tools.Result, error) {
			select {
			case <-time.After(d):
				return tools.Result{Data: "done"}, nil
			case <-ctx.Done():
				return tools.Result{Errors: []string{"cancelled"}}, nil
			}
		},
	}
}

// Review item 1: a request whose body nobody reads (401 without a token,
// 404, 415) used to be drained by net/http without any deadline, so a client
// stalling that body held the connection and its goroutine forever.
// ReadTimeout now cuts it off on every path.
func TestStalledUnreadBodyIsCutOff(t *testing.T) {
	s := newStack(t, stackOpts{server: func(srv *Server) { srv.http.ReadTimeout = testReadTimeout }})
	host := strings.TrimPrefix(s.url, "http://")
	token := s.accessToken(t)
	for name, req := range map[string]string{
		"401 chunked":        "POST /mcp HTTP/1.1\r\nHost: " + host + "\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab",
		"401 content-length": "POST /mcp HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 1000\r\n\r\n{\"js",
		"404 chunked":        "POST /nope HTTP/1.1\r\nHost: " + host + "\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab",
		"415 chunked": "POST /mcp HTTP/1.1\r\nHost: " + host + "\r\nAuthorization: Bearer " + token +
			"\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab",
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.Dial("tcp", host)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if _, err := io.WriteString(conn, req); err != nil {
				t.Fatal(err)
			}
			// The client now stalls. The server answers, or drops the
			// connection, once the read deadline has passed.
			start := time.Now()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err = io.Copy(io.Discard, conn)
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("the server kept the stalled connection open")
			}
			if el := time.Since(start); el > 3*time.Second {
				t.Errorf("connection closed after %v", el)
			}
		})
	}
}

// ReadTimeout bounds reading the request, not the tool call: a call that
// takes several times longer is answered, over HTTP/1.1 and over HTTP/2 with
// built-in TLS.
func TestLongToolCallOutlivesReadTimeout(t *testing.T) {
	cert := selfSigned(t)
	toolset := append(tools.All(), slowTool(4*testReadTimeout))

	t.Run("http1", func(t *testing.T) {
		s := newStack(t, stackOpts{tools: toolset, server: func(srv *Server) { srv.http.ReadTimeout = testReadTimeout }})
		res := s.call(t, s.accessToken(t), "slow", "{}")
		if res.IsError || !strings.Contains(res.text(), "done") {
			t.Fatalf("slow call over HTTP/1.1 = %+v", res)
		}
	})

	t.Run("http2", func(t *testing.T) {
		s := newStack(t, stackOpts{tools: toolset, server: func(srv *Server) {
			srv.http.ReadTimeout = testReadTimeout
			srv.EnableTLS(&tls.Config{Certificates: []tls.Certificate{cert}})
		}})
		pool := x509.NewCertPool()
		leaf, _ := x509.ParseCertificate(cert.Certificate[0])
		pool.AddCert(leaf)
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}, ForceAttemptHTTP2: true}
		t.Cleanup(tr.CloseIdleConnections)
		req, _ := http.NewRequest(http.MethodPost, "https://"+strings.TrimPrefix(s.url, "http://")+"/mcp", strings.NewReader(callBody("slow", "{}")))
		req.Header.Set("Authorization", "Bearer "+s.accessToken(t))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK {
			t.Fatalf("got %s %d %s", resp.Proto, resp.StatusCode, body)
		}
		var out struct {
			Result toolResult `json:"result"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Result.IsError || !strings.Contains(out.Result.text(), "done") {
			t.Fatalf("slow call over HTTP/2 = %s (%v)", body, err)
		}
	})
}

// A request without a body (or whose body has been read) is not cut off by
// ReadTimeout either: the connection stays usable for the next request.
func TestKeepAliveAfterReadTimeout(t *testing.T) {
	s := newStack(t, stackOpts{server: func(srv *Server) { srv.http.ReadTimeout = testReadTimeout }})
	conn, err := net.Dial("tcp", strings.TrimPrefix(s.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for i := range 2 {
		if i > 0 {
			time.Sleep(2 * testReadTimeout) // idle between requests: IdleTimeout applies
		}
		if _, err := io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d", i, resp.StatusCode)
		}
	}
}
