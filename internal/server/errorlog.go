package server

import (
	"context"
	"log"
	"log/slog"
	"strings"
)

// tlsHandshakeErrorPrefix starts every line net/http writes for a failed TLS
// handshake (port scanners, plain-HTTP clients, aborted connections).
const tlsHandshakeErrorPrefix = "http: TLS handshake error"

// panicMarker appears in the lines net/http (and its HTTP/2 server) writes for
// a recovered handler panic: "http: panic serving <addr>: <value>\n<stack>".
const panicMarker = "panic serving"

// errorLogWriter routes http.Server.ErrorLog lines into slog. Handshake
// failures are triggered by anyone who can open a TCP connection, so they go
// to debug and cannot flood the logs in built-in TLS mode. A recovered panic
// is logged at error without its value and stack: the value may carry a
// request's API key or token. Everything else (accept errors and the like)
// stays at warn.
type errorLogWriter struct{ log *slog.Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	level := slog.LevelWarn
	switch {
	case strings.HasPrefix(msg, tlsHandshakeErrorPrefix):
		level = slog.LevelDebug
	case strings.Contains(msg, panicMarker):
		i := strings.Index(msg, panicMarker)
		msg = msg[:i+len(panicMarker)] + " (value and stack redacted)"
		level = slog.LevelError
	}
	w.log.Log(context.Background(), level, msg)
	return len(p), nil
}

// newErrorLog returns the logger for http.Server.ErrorLog.
func newErrorLog(l *slog.Logger) *log.Logger {
	return log.New(errorLogWriter{l}, "", 0)
}
