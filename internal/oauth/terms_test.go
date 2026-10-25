package oauth

import (
	"fmt"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func setTerms(e *azEnv) {
	e.srv.cfg.OAuth.Page.TermsFile = "terms.txt"
	e.srv.cfg.OAuth.Page.TermsText = "Free.\n\nAs is."
}

func TestTermsRendering(t *testing.T) {
	e := newAZ(t)
	if b := pageBody(t, e); strings.Contains(b, "accept_terms") || strings.Contains(b, "<details") {
		t.Errorf("terms rendered without config:\n%s", b)
	}
	setTerms(e)
	b := pageBody(t, e)
	for _, want := range []string{
		`<details class="terms">`,
		`<summary>Terms of use</summary>`,
		`role="region" aria-label="Terms of use text"`,
		`<p>Free.</p>`, `<p>As is.</p>`,
		`<input type="checkbox" name="accept_terms" value="on" required>`,
		"I have read and accept the terms of use",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q:\n%s", want, b)
		}
	}
	for _, bad := range []string{`class="check bad"`, "aria-invalid", `role="alert"`, `name="lang"`, "<a ", `class="lang"`} {
		if strings.Contains(b, bad) {
			t.Errorf("fresh page contains %q", bad)
		}
	}
	// Order: terms, hint, checkbox, button.
	idx := func(s string) int { return strings.Index(b, s) }
	if idx("<details") >= idx(en("hint.continue")) || idx(en("hint.continue")) >= idx(`name="accept_terms"`) || idx(`name="accept_terms"`) >= idx("<button") {
		t.Error("terms block, hint, checkbox, button are out of order")
	}
}

func TestTermsEscaping(t *testing.T) {
	e := newAZ(t)
	e.srv.cfg.OAuth.Page.TermsFile = "terms.txt"
	e.srv.cfg.OAuth.Page.TermsText = "<script>alert(1)</script>\n\n<img src=x onerror=alert(2)> & \"q\""
	b := pageBody(t, e)
	for _, bad := range []string{"<script", "<img", "onerror=alert(2)>"} {
		if strings.Contains(b, bad) {
			t.Errorf("page contains raw %q:\n%s", bad, b)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)", "&lt;img src=x"} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks escaped %q", want)
		}
	}
}

func TestTermsEnforcement(t *testing.T) {
	e := newAZ(t)
	setTerms(e)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())

	for name, mod := range map[string]func(url.Values){
		"absent": func(url.Values) {},
		"off":    func(f url.Values) { f.Set(acceptTermsField, "off") },
		"one":    func(f url.Values) { f.Set(acceptTermsField, "1") },
		"true":   func(f url.Values) { f.Set(acceptTermsField, "true") },
		"empty":  func(f url.Values) { f.Set(acceptTermsField, "") },
		"ON":     func(f url.Values) { f.Set(acceptTermsField, "ON") },
	} {
		t.Run("refused "+name, func(t *testing.T) {
			f := goodForm(sealed)
			mod(f)
			rec := e.post(f)
			mustPage(t, rec, http.StatusBadRequest)
			b := rec.Body.String()
			for _, want := range []string{
				`<div class="err" role="alert" id="err">`, "<p>" + en(msgTermsRequired) + "</p>",
				`class="check bad"`, `aria-invalid="true" aria-describedby="err"`,
			} {
				if !strings.Contains(b, want) {
					t.Errorf("re-rendered form lacks %q", want)
				}
			}
			if strings.Contains(b, testKey) {
				t.Error("secret echoed")
			}
			if rec.Header().Get("Location") != "" {
				t.Error("redirected without acceptance")
			}
		})
	}

	f := goodForm(sealed)
	f.Set(acceptTermsField, "on")
	mustSeeOther(t, e.post(f))
}

// Without acceptance nothing downstream runs: no Garmin call, and the global
// ceiling on issued codes is untouched.
func TestTermsRefusalCostsNothing(t *testing.T) {
	e, _ := newCeiling(t, 1)
	setTerms(e)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	for i := 0; i < 20; i++ {
		rec := e.postFrom(fmt.Sprintf("203.0.113.%d:1", i+20), goodForm(sealed))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if n := len(e.val.got()); n != 0 {
		t.Fatalf("Garmin contacted %d times without acceptance", n)
	}
	// The burst of one code is still there.
	form := goodForm(sealed)
	form.Set(acceptTermsField, "on")
	mustSeeOther(t, e.postFrom("203.0.113.10:1", form))
	if len(e.val.got()) == 0 {
		t.Error("fixture: the accepted submit never reached the validator")
	}
}

// The acceptance is asked before the key is looked at: even an invalid key
// gets the terms message first, with Garmin never consulted.
func TestTermsCheckedBeforeKeyChecks(t *testing.T) {
	e := newAZ(t)
	e.val.err = garmin.ErrKeyRejected
	setTerms(e)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	form := goodForm(sealed)
	rec := e.post(form)
	mustPage(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), en(msgTermsRequired)) || strings.Contains(rec.Body.String(), en(msgKeyRejected)) {
		t.Errorf("terms message expected:\n%s", rec.Body)
	}
	if n := len(e.val.got()); n != 0 {
		t.Errorf("Garmin contacted %d times", n)
	}
}

func TestTermsIgnoredWhenNotConfigured(t *testing.T) {
	e := newAZ(t)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	mustSeeOther(t, e.post(goodForm(sealed))) // no accept_terms needed
}

// Only the exact value "on" accepts the terms; anything else is refused.
func TestTermsAcceptanceValue(t *testing.T) {
	e := newAZ(t)
	setTerms(e)
	sealed := hidden(t, e.get(e.query(nil)).Body.String())
	for _, v := range []string{"", "ON", "true", "1", "yes", "off", "on "} {
		f := goodForm(sealed)
		if v != "" {
			f.Set(acceptTermsField, v)
		}
		rec := e.post(f)
		mustPage(t, rec, http.StatusBadRequest)
		if b := rec.Body.String(); !strings.Contains(b, en(msgTermsRequired)) || !strings.Contains(b, `class="check bad"`) || !strings.Contains(b, `aria-invalid="true"`) {
			t.Errorf("value %q: terms error missing:\n%s", v, b)
		}
	}
	f := goodForm(sealed)
	f.Set(acceptTermsField, "on")
	mustSeeOther(t, e.post(f))
}

// The page is English only: no switcher, no language field, and lang and
// Accept-Language have no effect.
func TestPageHasNoLanguages(t *testing.T) {
	e := newAZ(t)
	for _, q := range []url.Values{
		e.query(nil),
		e.query(func(q url.Values) { q.Set("lang", "ru") }),
	} {
		rec := e.get(q)
		mustPage(t, rec, http.StatusOK)
		b := rec.Body.String()
		if !strings.Contains(b, `<html lang="en">`) || strings.Contains(b, `name="lang"`) || strings.Contains(b, `class="lang"`) || strings.Contains(b, "hreflang") {
			t.Errorf("language leftovers on the page:\n%s", b)
		}
	}
	rec := e.post(goodForm(hidden(t, e.get(e.query(nil)).Body.String())), "Accept-Language", "kk")
	mustSeeOther(t, rec)
}

// Failures of every path render the English error page.
func TestErrorPagesAreEnglish(t *testing.T) {
	e := newAZ(t)
	rec := e.get(url.Values{"client_id": {"nope"}, "lang": {"ru"}})
	mustPage(t, rec, http.StatusBadRequest)
	for _, want := range []string{`<html lang="en">`, "<title>" + en("error.title") + "</title>", en(msgClientUnknown)} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("error page lacks %q", want)
		}
	}

	rec = e.post(url.Values{requestField: {"x"}}, "Sec-Fetch-Site", "cross-site")
	mustPage(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), en(msgCrossSite)) {
		t.Errorf("cross-site error:\n%s", rec.Body)
	}

	rec = httptest.NewRecorder()
	e.srv.denyAuthorize(rec, httptest.NewRequest(http.MethodGet, PathAuthorize, nil))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), en(msgTooManyRequests)) {
		t.Errorf("deny: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	e.srv.redirect(rec, httptest.NewRequest(http.MethodGet, PathAuthorize, nil), "http://[::1", "", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), en(msgInternal)) {
		t.Errorf("redirect: %d %s", rec.Code, rec.Body)
	}
}
