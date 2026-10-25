package oauth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A code is redeemed once; the replay is invalid_grant and issues nothing.
func TestCodeSingleUse(t *testing.T) {
	e := newTK(t)
	code := e.code(t, e.none, nil)
	e.wantOK(t, e.do(e.codeForm(e.none, code)))
	e.wantErr(t, e.do(e.codeForm(e.none, code)), 400, "invalid_grant")
}

// A refresh chain never outlives oauth.max_session_ttl counted from the login,
// however often it is refreshed, and no token is sealed past that end.
func TestRefreshRespectsMaxSession(t *testing.T) {
	e := newTK(t)
	e.cfg.OAuth.MaxSessionTTL = 100 * time.Hour
	start := e.now.Load()
	end := start + 100*3600
	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.code(t, e.none, nil))))
	if got := e.refreshPayload(t, rt).Exp; got > end {
		t.Fatalf("first refresh token expires %d, after the session end %d", got, end)
	}
	for range 3 { // slide within the window
		e.now.Add(30 * 3600)
		_, rt = e.wantOK(t, e.do(e.refreshForm(e.none, rt)))
		p := e.refreshPayload(t, rt)
		if p.Exp > end || p.Start != start {
			t.Fatalf("refresh token Exp=%d Start=%d, want Exp <= %d and Start %d", p.Exp, p.Start, end, start)
		}
	}
	// The 4th refresh would be at start+120h: past the end, refused.
	e.now.Add(30 * 3600)
	e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
}

func TestPKCEMissingRedirects(t *testing.T) {
	e := newAZ(t)
	rec := e.get(e.query(func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") }))
	u := mustRedirect(t, rec)
	if got := u.Scheme + "://" + u.Host + u.Path; got != cbClaudeAI {
		t.Fatalf("redirected to %s", got)
	}
	q := u.Query()
	if q.Get("error") != "invalid_request" || q.Get("iss") != azPublic || q.Get("state") != "st&te 1" || q.Has("code") {
		t.Errorf("query = %v", q)
	}
	if !strings.Contains(q.Get("error_description"), "PKCE") {
		t.Errorf("error_description = %q", q.Get("error_description"))
	}
}

// A redirect_uri that is not registered or not allow-listed is never
// redirected to: the user sees an error page (an open redirector otherwise).
func TestRedirectNotAllowListedIs400Page(t *testing.T) {
	e := newAZ(t)
	for _, uri := range []string{"https://evil.example.com/cb", cbClaudeCom, cbClaudeAI + "?x=1", ""} {
		rec := e.get(e.query(func(q url.Values) { q.Set("redirect_uri", uri) }))
		mustPage(t, rec, http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), "Authorization error") {
			t.Errorf("%q: no error page", uri)
		}
	}
	// Registered once, removed from the allow-list later: the form is refused too.
	e.srv.cfg.OAuth.AllowedRedirectURIs = []string{cbClaudeCom}
	mustPage(t, e.get(e.query(nil)), http.StatusBadRequest)
}
