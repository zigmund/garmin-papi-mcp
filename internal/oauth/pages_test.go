package oauth

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
)

// pageBody GETs the authorize form from e and returns the HTML.
func pageBody(t *testing.T, e *azEnv) string {
	t.Helper()
	rec := e.get(e.query(nil))
	mustPage(t, rec, http.StatusOK)
	return rec.Body.String()
}

func TestPageMinimal(t *testing.T) {
	e := newAZ(t)
	body := pageBody(t, e)
	for _, absent := range []string{"Notice from the operator", "Contact:", `role="alert"`, "Terms of use"} {
		if strings.Contains(body, absent) {
			t.Errorf("minimal page contains %q", absent)
		}
	}
	for _, want := range []string{
		`<html lang="en">`, "Connect Garmin", "Application name:", "self-declared, unverified",
		en("privacy.sealed"), en("privacy.proxy"), en("privacy.third_party"),
		en("hint.continue"), ">Authorize</button>", "Personal API key",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("minimal page lacks %q", want)
		}
	}
	if n := strings.Count(body, "<input"); n != 2 { // hidden request + key
		t.Errorf("form has %d inputs, want the hidden request and one key field:\n%s", n, body)
	}
}

func TestPageEmptyClientName(t *testing.T) {
	e := newAZ(t)
	id, err := e.srv.sealer.Seal(seal.KindClient, client{RedirectURIs: []string{cbClaudeAI}, AuthMethod: authNone, IssuedAt: 1}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	q := e.query(func(q url.Values) { q.Set("client_id", id) })
	rec := e.get(q)
	mustPage(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "<b>Unnamed application</b>") {
		t.Errorf("no fallback client name:\n%s", rec.Body)
	}
}

func TestPageNoticeAndContact(t *testing.T) {
	tests := []struct {
		name    string
		page    config.Page
		want    []string
		absent  []string
		section bool
	}{
		{"none", config.Page{}, nil, []string{"Notice from the operator"}, false},
		{"notice only", config.Page{Notice: "Free.\n\nAs is."},
			[]string{"<p>Free.</p>", "<p>As is.</p>"}, []string{"Contact:"}, true},
		{"contact only mailto", config.Page{Contact: "mailto:ops@example.org"},
			[]string{`<p>Contact: <a href="mailto:ops@example.org">ops@example.org</a></p>`}, nil, true},
		{"contact only https", config.Page{Contact: "https://example.org/help"},
			[]string{`<a href="https://example.org/help">https://example.org/help</a>`}, nil, true},
		{"both", config.Page{Contact: "mailto:ops@example.org", Notice: "Hi."},
			[]string{"<p>Hi.</p>", "Contact:"}, nil, true},
		{"notice is escaped", config.Page{Notice: "<script>alert(1)</script> & \"q\""},
			[]string{"<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; &#34;q&#34;</p>"}, []string{"<script"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAZ(t)
			e.srv.cfg.OAuth.Page = tt.page
			body := pageBody(t, e)
			if got := strings.Contains(body, "Notice from the operator"); got != tt.section {
				t.Errorf("notice section present = %v, want %v", got, tt.section)
			}
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("page lacks %q:\n%s", w, body)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(body, a) {
					t.Errorf("page contains %q", a)
				}
			}
		})
	}
}

func TestPageErrorBanner(t *testing.T) {
	e := newAZ(t)
	f := goodForm(hidden(t, pageBody(t, e)))
	f.Set(keyField, "")
	rec := e.post(f)
	mustPage(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), `role="alert"`) || !strings.Contains(rec.Body.String(), "<p>Enter your Personal API key.</p>") {
		t.Errorf("no error banner:\n%s", rec.Body)
	}
}

func TestPageErrorPage(t *testing.T) {
	e := newAZ(t)
	rec := e.get(url.Values{"client_id": {"nope"}})
	mustPage(t, rec, http.StatusBadRequest)
	body := rec.Body.String()
	for _, want := range []string{
		"<title>Authorization error</title>", `<h1 class="h-err">Authorization error</h1>`, `role="alert"`,
		en(msgClientUnknown), en("error.hint"), `<html lang="en">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("error page lacks %q", want)
		}
	}
}

func TestPageSecurityContent(t *testing.T) {
	body := pageBody(t, newAZ(t))
	h1 := regexp.MustCompile(`(?s)<h1>(.*?)</h1>`).FindStringSubmatch(body)
	if h1 == nil || !strings.HasPrefix(h1[1], `<span class="host">claude.ai</span>`) {
		t.Errorf("redirect host must lead the h1: %v", h1)
	}
	if strings.Index(body, "claude.ai") > strings.Index(body, "Evil") {
		t.Error("redirect host must come before the client name")
	}
}

// Hostile values from config and the client must stay inert.
func TestPageEscaping(t *testing.T) {
	e := newAZ(t)
	e.srv.cfg.OAuth.Page = config.Page{
		Contact: `https://x.example/"><script>alert(1)</script>`,
		Notice:  "<script>alert(2)</script>\n\n<img src=x onerror=alert(3)>",
	}
	body := pageBody(t, e)
	for _, bad := range []string{"<script", "<img", `"><script`, "onerror=alert(3)>"} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains raw %q:\n%s", bad, body)
		}
	}
	if want := "&lt;script&gt;alert(2)"; !strings.Contains(body, want) {
		t.Errorf("page lacks escaped %q", want)
	}

	// A contact that bypassed validation with a dangerous scheme is neutralised.
	e.srv.cfg.OAuth.Page = config.Page{Contact: "javascript:alert(1)"}
	if body := pageBody(t, e); strings.Contains(body, `href="javascript`) {
		t.Errorf("javascript: URL rendered:\n%s", body)
	}
}

func TestPageHostileClientAndHost(t *testing.T) {
	e := newAZ(t) // client name is "<b>Evil</b> App"
	body := pageBody(t, e)
	if strings.Contains(body, "<b>Evil</b>") {
		t.Error("client name not escaped")
	}
}

// The page names the three facts a user must know before pasting a key.
func TestPagePrivacyCopy(t *testing.T) {
	body := pageBody(t, newAZ(t))
	for _, want := range []string{
		"Your key is sealed into the token and never stored or logged.",
		"A TLS reverse proxy in front of this server sees the key while you submit it.",
		"This server is not affiliated with Garmin and you are giving your Personal API key to a third party.",
		"The token is a bearer secret: whoever holds it can use your Garmin data until the session ends. This server cannot revoke it: disconnecting the connector changes nothing, and after you revoke the key at Garmin access can continue for up to 24 hours.",
		"This server logs request metadata only: time, status, client IP address on sign-in attempts and, if the operator turns on debug logging, on every request, plus the tool name; it never logs your key, tokens or Garmin data.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// Every re-rendered form (rejected key, no verdict) keeps the privacy copy and
// stays free of the key.
func TestPageErrorFormsKeepCopy(t *testing.T) {
	e := newAZ(t)
	f := goodForm(hidden(t, pageBody(t, e)))
	for _, verr := range []error{garmin.ErrKeyRejected, &garmin.StatusError{Status: 500}} {
		e.val.err = verr
		rec := e.post(f)
		if !strings.Contains(rec.Body.String(), en("privacy.third_party")) || strings.Contains(rec.Body.String(), testKey) {
			t.Errorf("%v: bad re-render:\n%s", verr, rec.Body)
		}
	}
}

// The CSP carries the hash of the stylesheet that is really embedded, for both
// pages, and the pages stay free of inline script, style attributes and
// external resources.
func TestPageCSPAndStrictness(t *testing.T) {
	e := newAZ(t)
	pages := map[string]string{
		"authorize": e.get(e.query(nil)).Header().Get("Content-Security-Policy"),
		"error":     e.get(url.Values{"client_id": {"nope"}}).Header().Get("Content-Security-Policy"),
	}
	for name, csp := range pages {
		if !strings.Contains(csp, "style-src "+styleSrc+";") {
			t.Errorf("%s CSP %q lacks the hash of the embedded CSS %s", name, csp, styleSrc)
		}
		if strings.Contains(csp, "unsafe") || !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("%s CSP not strict: %q", name, csp)
		}
	}
	for name, src := range map[string]string{"authorize.html": authorizeHTML, "error.html": errorHTML} {
		for _, bad := range []string{"<script", "style=", "<link", "<img", "<iframe", "http://", "https://", "onclick", "javascript:"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		if n := strings.Count(src, "<style>"); n != 1 {
			t.Errorf("%s has %d <style> elements, want 1", name, n)
		}
	}
	for _, bad := range []string{"@import", "url(", "expression("} {
		if strings.Contains(styleCSS, bad) {
			t.Errorf("style.css contains %q", bad)
		}
	}
}
