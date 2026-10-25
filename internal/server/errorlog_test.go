package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestErrorLogLevels(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	el := newErrorLog(l)
	el.Printf("http: TLS handshake error from 192.0.2.1:1234: EOF")
	el.Printf("http: Accept error: boom; retrying")

	var hs, other string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, "TLS handshake error") {
			hs = line
		} else {
			other = line
		}
	}
	if !strings.Contains(hs, "level=DEBUG") {
		t.Errorf("handshake error line = %q, want debug", hs)
	}
	if !strings.Contains(other, "level=WARN") || !strings.Contains(other, "Accept error") {
		t.Errorf("other line = %q, want warn", other)
	}
}

func TestErrorLogHandshakeHiddenAtInfo(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	el := newErrorLog(l)
	for range 100 {
		el.Printf("http: TLS handshake error from 192.0.2.1:1234: EOF")
	}
	if buf.Len() != 0 {
		t.Errorf("handshake errors leaked at info level: %q", buf.String())
	}
}

// Carried item 1: net/http logs a recovered handler panic with its value and
// stack; the value may hold a key, so only the fact is kept.
func TestErrorLogRedactsPanics(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	el := newErrorLog(l)
	el.Printf("http: panic serving 192.0.2.1:1234: %s\ngoroutine 7 [running]:\nmain.handler()\n", panicSecret+" "+testAPIKey)
	el.Printf("http2: panic serving 192.0.2.1:1234: %s\ngoroutine 9 [running]:\n", panicSecret)
	out := buf.String()
	if strings.Contains(out, panicSecret) || strings.Contains(out, testAPIKey) || strings.Contains(out, "goroutine") {
		t.Errorf("panic details reached the log: %q", out)
	}
	if strings.Count(out, "panic serving") != 2 || !strings.Contains(out, "level=ERROR") {
		t.Errorf("panic not reported: %q", out)
	}
}
