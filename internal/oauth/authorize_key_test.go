package oauth

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
)

func (e *azEnv) sealedForm(t *testing.T) string {
	t.Helper()
	return hidden(t, e.get(e.query(nil)).Body.String())
}

func TestAuthorizeTrimsKey(t *testing.T) {
	e := newAZ(t)
	f := url.Values{requestField: {e.sealedForm(t)}, keyField: {" KEY\n"}}
	u := mustSeeOther(t, e.post(f))
	if got := e.val.got(); !slices.Equal(got, []string{"KEY"}) {
		t.Fatalf("validator saw %q, want [KEY]", got)
	}
	code, err := seal.OpenAs[tokens.Code](e.srv.sealer, seal.KindCode, u.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	if code.APIKey != "KEY" {
		t.Errorf("sealed key = %q, want trimmed KEY", code.APIKey)
	}
}

func TestAuthorizeEmptyKey400(t *testing.T) {
	e := newAZ(t)
	for _, v := range []string{"", " \t\n"} {
		rec := e.post(url.Values{requestField: {e.sealedForm(t)}, keyField: {v}})
		mustPage(t, rec, http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), "Enter your Personal API key") {
			t.Errorf("no message for %q", v)
		}
	}
	if n := len(e.val.got()); n != 0 {
		t.Errorf("validator called %d times", n)
	}
}

func TestAuthorizeOversizeKey400(t *testing.T) {
	e := newAZ(t)
	sealed := e.sealedForm(t)

	rec := e.post(url.Values{requestField: {sealed}, keyField: {strings.Repeat("a", maxKeyLen+1)}})
	mustPage(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "too long") {
		t.Error("no too-long message")
	}
	if n := len(e.val.got()); n != 0 {
		t.Errorf("validator called %d times for an oversize key", n)
	}
	// Exactly the limit is accepted.
	mustSeeOther(t, e.post(url.Values{requestField: {sealed}, keyField: {strings.Repeat("a", maxKeyLen)}}))
}

func TestAuthorizeNeverEchoesKey(t *testing.T) {
	e := newAZ(t)
	sealed := e.sealedForm(t)
	const key = "distinctive-fake-key-value"
	for name, verr := range map[string]error{
		"rejected":    garmin.ErrKeyRejected,
		"wrapped":     errors.Join(errors.New("ctx"), garmin.ErrKeyRejected),
		"unavailable": &garmin.StatusError{Status: 500},
	} {
		t.Run(name, func(t *testing.T) {
			e.val.err = verr
			rec := e.post(url.Values{requestField: {sealed}, keyField: {key}})
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), key) {
				t.Errorf("body echoes the key:\n%s", rec.Body)
			}
			for k, v := range rec.Header() {
				if strings.Contains(strings.Join(v, ","), key) {
					t.Errorf("header %s echoes the key", k)
				}
			}
		})
	}
}

func TestAuthorizeKeyRejected400(t *testing.T) {
	e := newAZ(t)
	e.val.err = garmin.ErrKeyRejected
	rec := e.post(goodForm(e.sealedForm(t)))
	mustPage(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "Garmin rejected this key") {
		t.Errorf("no rejection message:\n%s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `name="request"`) {
		t.Error("form not re-rendered")
	}
}

func TestAuthorizeGarmin5xxGives502(t *testing.T) {
	e := newAZ(t)
	e.val.err = &garmin.StatusError{Status: 503}
	rec := e.post(goodForm(e.sealedForm(t)))
	mustPage(t, rec, http.StatusBadGateway)
	if !strings.Contains(rec.Body.String(), "Try again") {
		t.Errorf("no retry message:\n%s", rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	// Fails closed: no code is issued, whatever the error.
	e.val.err = errors.New("dial tcp: i/o timeout")
	mustPage(t, e.post(goodForm(e.sealedForm(t))), http.StatusBadGateway)
}

func TestAuthorizeNilValidatorFailsClosed(t *testing.T) {
	e := newAZ(t)
	e.srv.validator = nil
	rec := e.post(goodForm(e.sealedForm(t)))
	mustPage(t, rec, http.StatusBadGateway)
	if strings.Contains(rec.Body.String(), testKey) {
		t.Error("key echoed")
	}
}

// JSON escapes ", < and & to six bytes each; the size budget must count that.
func TestAuthorizeMaxEscapingKeyFitsEveryToken(t *testing.T) {
	e := newAZ(t)
	key := strings.Repeat(`"<&`, maxKeyLen/3) + strings.Repeat("a", maxKeyLen%3)
	if len(key) != maxKeyLen {
		t.Fatalf("fixture length %d", len(key))
	}
	u := mustSeeOther(t, e.post(url.Values{requestField: {e.sealedForm(t)}, keyField: {key}}))
	if u.Query().Get("code") == "" {
		t.Fatalf("no code issued: %v", u.Query())
	}
	tk := newTK(t)
	access, refresh := tk.wantOK(t, tk.do(tk.codeForm(tk.none, tk.code(t, tk.none, func(c *tokens.Code) { c.APIKey = key }))))
	if got, err := tk.srv.OpenAccess(access); err != nil || got != key {
		t.Errorf("access: %v", err)
	}
	tk.wantOK(t, tk.do(tk.refreshForm(tk.none, refresh)))
}
