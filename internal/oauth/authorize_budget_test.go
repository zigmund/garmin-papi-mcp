package oauth

import (
	"net/url"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
)

// The largest accepted key fits into the code, the access token and the
// refresh token, so a submit that passes the length check cannot fail later
// with server_error.
func TestAuthorizeMaxKeyFitsEveryToken(t *testing.T) {
	e := newAZ(t)
	req := authRequest{ClientID: e.clientID, RedirectURI: cbClaudeAI, CodeChallenge: azChallenge}
	key := strings.Repeat("é", maxKeyLen/2) // multi-byte, exactly maxKeyLen bytes
	if len(key) != maxKeyLen {
		t.Fatalf("fixture length %d", len(key))
	}
	if !keyFits(req, key) {
		t.Fatal("maximal key does not fit")
	}
	// A key far over the cap does not.
	if keyFits(req, strings.Repeat("a", seal.MaxTokenSize)) {
		t.Error("oversized key fits")
	}

	u := mustSeeOther(t, e.post(url.Values{requestField: {e.sealedForm(t)}, keyField: {key}}))
	code, err := seal.OpenAs[tokens.Code](e.srv.sealer, seal.KindCode, u.Query().Get("code"))
	if err != nil || code.APIKey != key {
		t.Fatalf("code: %v", err)
	}
	// And it survives the whole token flow.
	tk := newTK(t)
	access, refresh := tk.wantOK(t, tk.do(tk.codeForm(tk.none, tk.code(t, tk.none, func(c *tokens.Code) { c.APIKey = key }))))
	if got, err := tk.srv.OpenAccess(access); err != nil || got != key {
		t.Errorf("access: %v", err)
	}
	tk.wantOK(t, tk.do(tk.refreshForm(tk.none, refresh)))
}
