package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/tools"
)

// Review item 10: a tool call still waiting on Garmin when the drain period
// is over is cancelled and answered with a tool error saying why, instead of
// having its connection closed under it. Over HTTP/1.1 and over HTTP/2 with
// built-in TLS.
func TestShutdownCancelsToolCalls(t *testing.T) {
	cert := selfSigned(t)
	pool := x509.NewCertPool()
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	pool.AddCert(leaf)
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(t *testing.T) {
			testShutdownCancelsToolCall(t, h2, cert, pool)
		})
	}
}

func testShutdownCancelsToolCall(t *testing.T, h2 bool, cert tls.Certificate, pool *x509.CertPool) {
	started := make(chan struct{})
	hang := tools.Tool{
		Name:        "hang",
		Description: "waits for its call to be cancelled",
		Handler: func(ctx context.Context, _ tools.Getter, _ string, _ json.RawMessage) (tools.Result, error) {
			close(started)
			<-ctx.Done()
			return tools.Result{Errors: []string{"could not reach Garmin"}}, nil
		},
	}
	const grace = 5 * time.Second
	s := newStack(t, stackOpts{tools: append(tools.All(), hang), server: func(srv *Server) {
		srv.shutdownDrain, srv.shutdownGrace = 100*time.Millisecond, grace
		if h2 {
			srv.EnableTLS(&tls.Config{Certificates: []tls.Certificate{cert}})
		}
	}})
	token := s.accessToken(t)
	client, target := s.client, s.url+"/mcp"
	if h2 {
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}, ForceAttemptHTTP2: true}
		t.Cleanup(tr.CloseIdleConnections)
		client, target = &http.Client{Transport: tr, Timeout: 10 * time.Second}, "https://"+strings.TrimPrefix(target, "http://")
	}

	type answer struct {
		res   toolResult
		proto int
		err   error
	}
	answered := make(chan answer, 1)
	go func() { // no t.Fatal off the test goroutine: plain client calls
		req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(callBody("hang", "{}")))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Result toolResult `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
		answered <- answer{out.Result, resp.ProtoMajor, err}
	}()
	<-started
	begin := time.Now()
	if err := s.stop(); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	if el := time.Since(begin); el >= grace {
		t.Errorf("shutdown took %v: the cancelled call did not finish within the grace period", el)
	}
	a := <-answered
	if a.err != nil {
		t.Fatalf("cancelled call: %v", a.err)
	}
	if want := map[bool]int{false: 1, true: 2}[h2]; a.proto != want {
		t.Errorf("answered over HTTP/%d, want HTTP/%d", a.proto, want)
	}
	if !a.res.IsError || !strings.Contains(a.res.text(), shutdownNote) {
		t.Errorf("cancelled call = %+v, want a tool error with %q", a.res, shutdownNote)
	}
	logs := s.logs.String()
	if !strings.Contains(logs, "cancelling them") || strings.Contains(logs, "timed out") {
		t.Errorf("want the cancellation logged and no forced close:\n%s", logs)
	}
}

// The shutdown note is added only to failed calls that shutdown cancelled.
func TestNoteShutdown(t *testing.T) {
	result := tools.Result{Data: "partial", Errors: []string{"could not reach Garmin"}}
	h := noteShutdown([]tools.Tool{{Name: "t", Handler: func(context.Context, tools.Getter, string, json.RawMessage) (tools.Result, error) {
		return result, nil
	}}})[0].Handler

	shutdown, cancel := context.WithCancelCause(context.Background())
	cancel(errShuttingDown)
	res, err := h(shutdown, nil, "", nil)
	if err != nil || res.Data != "partial" || len(res.Errors) != 2 || res.Errors[1] != shutdownNote {
		t.Errorf("cancelled by shutdown: %+v %v", res, err)
	}

	other, cancel2 := context.WithCancel(context.Background())
	cancel2() // e.g. the client went away
	for _, ctx := range []context.Context{context.Background(), other} {
		if res, _ := h(ctx, nil, "", nil); len(res.Errors) != 1 {
			t.Errorf("not cancelled by shutdown: %+v", res)
		}
	}
	result = tools.Result{Data: "ok"}
	if res, _ := h(shutdown, nil, "", nil); len(res.Errors) != 0 {
		t.Errorf("successful call got a note: %+v", res)
	}
}

// The systemd unit must give the binary its whole graceful shutdown, with
// headroom, before it sends SIGKILL.
func TestUnitStopTimeoutCoversShutdown(t *testing.T) {
	unit, err := os.ReadFile("../../deploy/garmin-papi-mcp.service")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^TimeoutStopSec=(\d+)s$`).FindSubmatch(unit)
	if m == nil {
		t.Fatal("no TimeoutStopSec=<n>s in the unit")
	}
	n, _ := strconv.Atoi(string(m[1]))
	if stop := time.Duration(n) * time.Second; stop < ShutdownTimeout+5*time.Second {
		t.Errorf("TimeoutStopSec = %v, want at least %v (ShutdownTimeout + 5s)", stop, ShutdownTimeout+5*time.Second)
	}
}
