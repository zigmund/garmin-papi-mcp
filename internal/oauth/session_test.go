package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
)

func choices(t *testing.T, def string, raw ...string) ([]config.SessionChoice, config.SessionChoice) {
	t.Helper()
	var out []config.SessionChoice
	for _, r := range raw {
		c, err := config.ParseSessionChoice(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if def == "" {
		return out, out[0]
	}
	d, err := config.ParseSessionChoice(def)
	if err != nil {
		t.Fatal(err)
	}
	return out, d
}

func setSessions(cfg *config.Config, t *testing.T, def string, raw ...string) {
	t.Helper()
	cfg.OAuth.Sessions, cfg.OAuth.DefaultSession = choices(t, def, raw...)
}

func TestSessionLabels(t *testing.T) {
	for raw, want := range map[string]string{
		"unlimited": "Until revoked",
		"1h":        "1 hour",
		"5h":        "5 hours",
		"25h":       "25 hours",
		"24h":       "1 day",
		"48h":       "2 days",
		"720h":      "30 days",
		"2160h":     "90 days",
		"8760h":     "1 year",
		"17520h":    "2 years",
		"8784h":     "366 days", // not a multiple of 8760h, but of 24h
	} {
		c, err := config.ParseSessionChoice(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := sessionLabel(c); got != want {
			t.Errorf("sessionLabel(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestSessionFormRendering(t *testing.T) {
	e := newAZ(t)
	setTerms(e)
	setSessions(e.srv.cfg, t, "2160h", "720h", "2160h", "8760h", "unlimited")
	rec := e.get(e.query(nil))
	mustPage(t, rec, http.StatusOK)
	b := rec.Body.String()
	for _, want := range []string{
		`<fieldset class="choices" role="radiogroup">`,
		`<legend>Stay connected for</legend>`,
		`<input type="radio" name="session" value="720h"><span>30 days</span>`,
		`<input type="radio" name="session" value="2160h" checked><span>90 days</span>`,
		`<input type="radio" name="session" value="8760h"><span>1 year</span>`,
		`<input type="radio" name="session" value="unlimited"><span>Until revoked</span>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q:\n%s", want, b)
		}
	}
	if n := strings.Count(b, " checked>"); n != 1 {
		t.Errorf("%d radios are checked", n)
	}
	if strings.Contains(b, "aria-invalid") || strings.Contains(b, `role="alert"`) {
		t.Error("fresh form shows an error state")
	}
	idx := func(s string) int { return strings.Index(b, s) }
	if idx("<fieldset") < idx(`name="api_key"`) || idx("</fieldset>") > idx("<button") {
		t.Error("the group must follow the key field and precede the button")
	}

	// The default is the first entry when none is named, and may be unlimited.
	setSessions(e.srv.cfg, t, "", "48h", "unlimited")
	if b := pageBody(t, e); !strings.Contains(b, `value="48h" checked>`) {
		t.Errorf("first entry not pre-checked:\n%s", b)
	}
	setSessions(e.srv.cfg, t, "unlimited", "48h", "unlimited")
	if b := pageBody(t, e); !strings.Contains(b, `value="unlimited" checked>`) {
		t.Errorf("unlimited default not pre-checked:\n%s", b)
	}
}

func TestSessionFormAbsentWithoutChoices(t *testing.T) {
	e := newAZ(t)
	b := pageBody(t, e)
	for _, bad := range []string{"<fieldset", "<legend", `type="radio"`, `name="session"`, "Stay connected"} {
		if strings.Contains(b, bad) {
			t.Errorf("page contains %q without session_choices", bad)
		}
	}
	// A submit needs no session; a stray value is ignored.
	sealed := hidden(t, b)
	rec := e.post(goodForm(sealed))
	u := mustSeeOther(t, rec)
	if c := openCode(t, e, u); c.Session != 0 {
		t.Errorf("Code.Session = %d, want 0 (no choice)", c.Session)
	}
	f := goodForm(sealed)
	f.Set(sessionField, "720h")
	if c := openCode(t, e, mustSeeOther(t, e.post(f))); c.Session != 0 {
		t.Errorf("Code.Session = %d, a stray value must not travel", c.Session)
	}
}

func openCode(t *testing.T, e *azEnv, loc *url.URL) tokens.Code {
	t.Helper()
	code, err := seal.OpenAs[tokens.Code](e.srv.sealer, seal.KindCode, loc.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestSessionSubmitAccepted(t *testing.T) {
	e := newAZ(t)
	setSessions(e.srv.cfg, t, "", "720h", "2160h", "unlimited")
	sealed := hidden(t, pageBody(t, e))
	for value, want := range map[string]int64{
		"720h":      720 * 3600,
		"2160h":     2160 * 3600,
		"unlimited": tokens.SessionUnlimited,
	} {
		f := goodForm(sealed)
		f.Set(sessionField, value)
		if got := openCode(t, e, mustSeeOther(t, e.post(f))).Session; got != want {
			t.Errorf("%s: Code.Session = %d, want %d", value, got, want)
		}
	}
}

func TestSessionRefusals(t *testing.T) {
	e := newAZ(t)
	setSessions(e.srv.cfg, t, "2160h", "720h", "2160h", "unlimited")
	sealed := hidden(t, pageBody(t, e))

	for name, mod := range map[string]func(url.Values){
		"missing":        func(url.Values) {},
		"empty":          func(f url.Values) { f.Set(sessionField, "") },
		"unlisted":       func(f url.Values) { f.Set(sessionField, "100h") },
		"other spelling": func(f url.Values) { f.Set(sessionField, "43200m") },
		"padded":         func(f url.Values) { f.Set(sessionField, " 720h") },
		"case":           func(f url.Values) { f.Set(sessionField, "Unlimited") },
		"zero":           func(f url.Values) { f.Set(sessionField, "0") },
	} {
		t.Run(name, func(t *testing.T) {
			form := goodForm(sealed)
			mod(form)
			rec := e.post(form)
			mustPage(t, rec, http.StatusBadRequest)
			b := rec.Body.String()
			for _, want := range []string{
				`<div class="err" role="alert" id="err">`, "<p>" + en(msgSessionRequired) + "</p>",
				`class="choices bad" role="radiogroup" aria-invalid="true" aria-describedby="err"`,
				`value="2160h" checked>`, // the default is shown selected again
			} {
				if !strings.Contains(b, want) {
					t.Errorf("re-rendered form lacks %q", want)
				}
			}
			if strings.Contains(b, testKey) {
				t.Error("secret echoed")
			}
		})
	}

	// A value only in the query string is not read.
	req := httptest.NewRequest(http.MethodPost, PathAuthorize+"?session=720h", strings.NewReader(goodForm(sealed).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	mustPage(t, rec, http.StatusBadRequest)

	if n := len(e.val.got()); n != 0 {
		t.Errorf("Garmin contacted %d times for refused submits", n)
	}
}

// A valid but unselected-looking submit keeps the user's pick on a later error.
func TestSessionPickSurvivesOtherErrors(t *testing.T) {
	e := newAZ(t)
	setSessions(e.srv.cfg, t, "2160h", "720h", "2160h", "unlimited")
	sealed := hidden(t, pageBody(t, e))
	f := goodForm(sealed)
	f.Set(sessionField, "unlimited")
	f.Set(keyField, "")
	rec := e.post(f)
	mustPage(t, rec, http.StatusBadRequest)
	b := rec.Body.String()
	if !strings.Contains(b, `value="unlimited" checked>`) || strings.Contains(b, `value="2160h" checked>`) || strings.Contains(b, "aria-invalid") {
		t.Errorf("pick lost or group flagged:\n%s", b)
	}
}

// The terms are asked first; the session check precedes the Garmin call.
func TestSessionCheckedNextToTerms(t *testing.T) {
	e := newAZ(t)
	e.val.err = garmin.ErrKeyRejected
	setTerms(e)
	setSessions(e.srv.cfg, t, "", "720h", "unlimited")
	sealed := hidden(t, pageBody(t, e))

	form := goodForm(sealed)
	rec := e.post(form) // neither terms nor session
	mustPage(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), en(msgTermsRequired)) {
		t.Errorf("terms message expected first:\n%s", rec.Body)
	}
	form.Set(acceptTermsField, "on")
	rec = e.post(form) // terms ok, session missing, key would be rejected
	mustPage(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), en(msgSessionRequired)) || strings.Contains(rec.Body.String(), en(msgKeyRejected)) {
		t.Errorf("session message expected before the Garmin check:\n%s", rec.Body)
	}
	if n := len(e.val.got()); n != 0 {
		t.Errorf("Garmin contacted %d times", n)
	}
}

// login runs authorize (form, submit) against the token test server and
// returns the issued code. An empty session posts none.
func (e *tkEnv) login(t *testing.T, session string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(tkVerifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {e.none}, "redirect_uri": {tkRedirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {azRes},
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathAuthorize+"?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize form: %d %s", rec.Code, rec.Body)
	}
	form := url.Values{requestField: {hidden(t, rec.Body.String())}, keyField: {testKey}}
	if session != "" {
		form.Set(sessionField, session)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, PathAuthorize, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("authorize submit: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Query().Get("code") == "" {
		t.Fatalf("no code in %q (%v)", rec.Header().Get("Location"), err)
	}
	return u.Query().Get("code")
}

// A fixed max_session_ttl, no choices: the whole flow is as before and the
// refresh token carries no End.
func TestSessionFlowWithoutChoicesUnchanged(t *testing.T) {
	e := newTK(t)
	e.cfg.OAuth.MaxSessionTTL = 100 * time.Hour
	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.login(t, ""))))
	if r := e.refreshPayload(t, rt); r.End != 0 || r.Start != fixedNow.Unix() {
		t.Fatalf("refresh payload = %+v", r)
	}
	raw, _ := json.Marshal(e.refreshPayload(t, rt))
	if strings.Contains(string(raw), `"z"`) {
		t.Errorf("End must be omitted when unset: %s", raw)
	}
	e.now.Add(99 * 3600)
	_, rt = e.wantOK(t, e.do(e.refreshForm(e.none, rt)))
	e.now.Add(3600)
	e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
}

func TestSessionFlowChoiceCapsExpiry(t *testing.T) {
	e := newTK(t)
	e.cfg.OAuth.MaxSessionTTL = 100 * time.Hour // superseded by the choice
	setSessions(e.cfg, t, "", "720h", "unlimited")
	end := fixedNow.Unix() + 720*3600

	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.login(t, "720h"))))
	if r := e.refreshPayload(t, rt); r.End != end || r.Start != fixedNow.Unix() {
		t.Fatalf("payload = %+v, want End %d", r, end)
	}
	// Past max_session_ttl, still inside the chosen session.
	e.now.Add(150 * 3600)
	_, rt = e.wantOK(t, e.do(e.refreshForm(e.none, rt)))
	if r := e.refreshPayload(t, rt); r.End != end {
		t.Fatalf("End not preserved: %+v", r)
	}
	// 30 minutes of session left: expires_in and refresh expiry are capped.
	e.now.Store(end - 1800)
	rec := e.do(e.refreshForm(e.none, rt))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d; %s", rec.Code, rec.Body)
	}
	m := decodeTK(t, rec)
	if m["expires_in"] != float64(1800) {
		t.Errorf("expires_in = %v, want 1800", m["expires_in"])
	}
	rt2, _ := m["refresh_token"].(string)
	if r := e.refreshPayload(t, rt2); r.Exp != end || r.End != end {
		t.Errorf("payload = %+v, want Exp and End %d", r, end)
	}
	acc, _ := m["access_token"].(string)
	e.now.Store(end - 1)
	if _, err := seal.OpenAs[tokens.Access](e.srv.sealer, seal.KindAccess, acc); err != nil {
		t.Errorf("access token must live until the session end: %v", err)
	}
	e.now.Store(end)
	if _, err := seal.OpenAs[tokens.Access](e.srv.sealer, seal.KindAccess, acc); err == nil {
		t.Error("access token must expire at the session end")
	}
	e.wantErr(t, e.do(e.refreshForm(e.none, rt2)), 400, "invalid_grant")
}

func TestSessionFlowChoiceBoundary(t *testing.T) {
	e := newTK(t)
	setSessions(e.cfg, t, "", "24h")
	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.login(t, "24h"))))
	e.now.Add(24*3600 - 1)
	rec := e.do(e.refreshForm(e.none, rt))
	if rec.Code != http.StatusOK || decodeTK(t, rec)["expires_in"] != float64(1) {
		t.Fatalf("one second before the end: %d %s", rec.Code, rec.Body)
	}
	rt, _ = decodeTK(t, rec)["refresh_token"].(string)
	e.now.Add(1)
	e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
}

func TestSessionFlowUnlimited(t *testing.T) {
	e := newTK(t)
	e.cfg.OAuth.MaxSessionTTL = 100 * time.Hour
	setSessions(e.cfg, t, "", "720h", "unlimited")

	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.login(t, "unlimited"))))
	if r := e.refreshPayload(t, rt); r.End != tokens.SessionUnlimited {
		t.Fatalf("End = %d, want the unlimited marker", r.End)
	}
	// Well past max_session_ttl and past any 720h window: the chain lives on
	// as long as it is refreshed within refresh_token_ttl.
	for i := 0; i < 5; i++ {
		e.now.Add(500 * 3600)
		rec := e.do(e.refreshForm(e.none, rt))
		if rec.Code != http.StatusOK {
			t.Fatalf("refresh %d: %d %s", i, rec.Code, rec.Body)
		}
		m := decodeTK(t, rec)
		if m["expires_in"] != float64(3600) {
			t.Fatalf("refresh %d: expires_in = %v, want the uncapped access ttl", i, m["expires_in"])
		}
		rt, _ = m["refresh_token"].(string)
		r := e.refreshPayload(t, rt)
		if r.End != tokens.SessionUnlimited || r.Exp != e.now.Load()+720*3600 {
			t.Fatalf("refresh %d: payload %+v, want sliding idle expiry", i, r)
		}
	}
	// Idle for refresh_token_ttl: dead, session or not.
	e.now.Add(720 * 3600)
	e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
}

// A refresh token without End keeps the max_session_ttl cap, whether or not
// choices are configured now: zero is never unlimited.
func TestSessionLegacyRefreshKeepsMaxSessionTTL(t *testing.T) {
	for _, withChoices := range []bool{false, true} {
		e := newTK(t)
		e.cfg.OAuth.MaxSessionTTL = 100 * time.Hour
		if withChoices {
			setSessions(e.cfg, t, "", "720h", "unlimited")
		}
		now := e.now.Load()
		legacy := e.sealRefresh(t, tokens.Refresh{
			ClientID: e.none, APIKey: testKey,
			Family: "old", Exp: now + 720*3600, Start: now, // no End
		})
		e.now.Add(99 * 3600)
		_, rt := e.wantOK(t, e.do(e.refreshForm(e.none, legacy)))
		r := e.refreshPayload(t, rt)
		if r.End != 0 || r.Start != now || r.Exp != now+100*3600 {
			t.Fatalf("choices=%v: payload %+v, want End 0 and the max_session_ttl cap", withChoices, r)
		}
		e.now.Add(3600)
		e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
	}
}

func TestSessionRefreshEndOutOfRange(t *testing.T) {
	e := newTK(t)
	now := e.now.Load()
	bad := e.sealRefresh(t, tokens.Refresh{
		ClientID: e.none, APIKey: testKey,
		Family: "bad-end", Exp: now + 3600, Start: now, End: -5,
	})
	e.wantErr(t, e.do(e.refreshForm(e.none, bad)), 400, "invalid_grant")
}

// A code whose choice the config no longer offers is refused.
func TestSessionCodeChoiceNoLongerOffered(t *testing.T) {
	e := newTK(t)
	for name, session := range map[string]int64{
		"unlimited without choices": tokens.SessionUnlimited,
		"duration without choices":  720 * 3600,
		"negative":                  -7,
	} {
		code := e.code(t, e.none, func(c *tokens.Code) { c.Session = session })
		rec := e.do(e.codeForm(e.none, code))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400; %s", name, rec.Code, rec.Body)
		}
	}
	setSessions(e.cfg, t, "", "720h")
	e.wantErr(t, e.do(e.codeForm(e.none, e.code(t, e.none, func(c *tokens.Code) { c.Session = tokens.SessionUnlimited }))), 400, "invalid_grant")
	e.wantErr(t, e.do(e.codeForm(e.none, e.code(t, e.none, func(c *tokens.Code) { c.Session = 24 * 3600 }))), 400, "invalid_grant")
	// An offered one and a code without a choice (issued before the upgrade) pass.
	e.wantOK(t, e.do(e.codeForm(e.none, e.code(t, e.none, func(c *tokens.Code) { c.Session = 720 * 3600 }))))
	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.code(t, e.none, nil))))
	if r := e.refreshPayload(t, rt); r.End != 0 {
		t.Errorf("a code without a choice must give End 0, got %d", r.End)
	}
}

// Lowering the config never lengthens a login, only shortens it at the next
// refresh; an unlimited choice that is still configured is kept.
func TestSessionClampOnRefresh(t *testing.T) {
	const h = 3600
	start := fixedNow.Unix()
	for _, tc := range []struct {
		name    string
		login   string
		setup   func(e *tkEnv)
		wantEnd int64 // End of the refreshed token
	}{
		{"unlimited, unlimited removed", "unlimited", func(e *tkEnv) { setSessions(e.cfg, t, "", "720h", "2160h") }, start + 2160*h},
		{"8760h, 8760h removed", "8760h", func(e *tkEnv) { setSessions(e.cfg, t, "", "720h", "2160h") }, start + 2160*h},
		{"unlimited, choices removed", "unlimited", func(e *tkEnv) { e.cfg.OAuth.Sessions = nil }, start + 100*h},
		{"8760h, choices removed, longer max_session_ttl", "8760h", func(e *tkEnv) {
			e.cfg.OAuth.Sessions = nil
			e.cfg.OAuth.MaxSessionTTL = 20000 * time.Hour
		}, start + 8760*h},
		{"shorter choice kept", "720h", func(e *tkEnv) { setSessions(e.cfg, t, "", "720h", "2160h") }, start + 720*h},
		{"unlimited kept while configured", "unlimited", func(e *tkEnv) { setSessions(e.cfg, t, "", "24h", "unlimited") }, tokens.SessionUnlimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTK(t)
			e.cfg.OAuth.MaxSessionTTL = 100 * time.Hour
			setSessions(e.cfg, t, "", "720h", "8760h", "unlimited")
			_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.login(t, tc.login))))
			tc.setup(e)
			e.now.Add(10 * h)
			_, rt = e.wantOK(t, e.do(e.refreshForm(e.none, rt)))
			r := e.refreshPayload(t, rt)
			if r.End != tc.wantEnd || r.End == 0 {
				t.Fatalf("End = %d, want %d", r.End, tc.wantEnd)
			}
			if tc.wantEnd == tokens.SessionUnlimited {
				return
			}
			// The clamped end is enforced, and never extended by later refreshes.
			for e.now.Load()+700*h < tc.wantEnd-1 { // stay inside the idle expiry
				e.now.Add(700 * h)
				_, rt = e.wantOK(t, e.do(e.refreshForm(e.none, rt)))
			}
			e.now.Store(tc.wantEnd - 1)
			rec := e.do(e.refreshForm(e.none, rt))
			if rec.Code != http.StatusOK {
				t.Fatalf("one second before the end: %d %s", rec.Code, rec.Body)
			}
			rt, _ = decodeTK(t, rec)["refresh_token"].(string)
			if got := e.refreshPayload(t, rt).End; got != tc.wantEnd {
				t.Fatalf("End moved to %d", got)
			}
			e.now.Store(tc.wantEnd)
			e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
		})
	}
}

// A chain whose clamped end already passed is refused outright at the refresh.
func TestSessionClampRefusesExpiredChain(t *testing.T) {
	e := newTK(t)
	setSessions(e.cfg, t, "", "720h", "unlimited")
	_, rt := e.wantOK(t, e.do(e.codeForm(e.none, e.login(t, "unlimited"))))
	setSessions(e.cfg, t, "", "24h")
	e.now.Add(48 * 3600)
	e.wantErr(t, e.do(e.refreshForm(e.none, rt)), 400, "invalid_grant")
}
