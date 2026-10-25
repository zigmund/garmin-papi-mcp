package oauth

import (
	"io"
	"log/slog"
	"testing"
)

// rejectedSet is a RejectionSource over a fixed set of keys.
type rejectedSet map[string]bool

func (r rejectedSet) Rejected(apiKey string) bool { return r[apiKey] }

func TestWithRejectionSourceOption(t *testing.T) {
	e := newTK(t)
	src := rejectedSet{}
	h := New(e.cfg, e.srv.sealer, &fakeValidator{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithRejectionSource(src))
	if h.rejected == nil {
		t.Fatal("WithRejectionSource not applied")
	}
	if New(e.cfg, e.srv.sealer, &fakeValidator{}, nil, WithRejectionSource(nil)).rejected != nil {
		t.Error("a nil source must leave the check off")
	}
}

// A refresh token around a key Garmin rejected is refused with invalid_grant,
// so the client goes back to the authorize form; other keys are unaffected,
// and the refusal does not consume the refresh token.
func TestRefreshRejectedKeyIsInvalidGrant(t *testing.T) {
	e := newTK(t)
	src := rejectedSet{}
	e.srv.rejected = src
	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.code(t, e.none, nil))))

	src[testKey] = true
	e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")

	delete(src, testKey) // the user re-authorized with the key
	e.wantOK(t, e.do(e.refreshForm(e.none, rt)))
}
