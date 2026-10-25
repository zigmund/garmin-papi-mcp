package httplog

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnnotateAddsAttrsToRecord(t *testing.T) {
	log, buf := newLogger(slog.LevelDebug)
	h := Middleware(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Annotate(r.Context(), slog.String("rpc_method", "tools/call"))
		Annotate(r.Context(), slog.String("tool", "get_profile"))
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	if recs[0]["rpc_method"] != "tools/call" || recs[0]["tool"] != "get_profile" || recs[0]["status"] != float64(200) {
		t.Errorf("record = %v", recs[0])
	}
}

// Outside the middleware, or with debug off, Annotate is a no-op.
func TestAnnotateNoop(t *testing.T) {
	Annotate(t.Context(), slog.String("k", "v")) // must not panic
	log, buf := newLogger(slog.LevelInfo)
	h := Middleware(log, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		Annotate(r.Context(), slog.String("k", "v"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if buf.Len() != 0 {
		t.Errorf("logged at info: %s", buf.String())
	}
}
