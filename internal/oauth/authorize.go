package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/tokens"
)

const (
	maxAuthorizeBody = 32 << 10 // POST form size cap
	maxStateLen      = 1024     // bytes; state is echoed verbatim
	maxKeyLen        = 512      // bytes of the Personal API key
	pkceChallengeLen = 43       // base64url(SHA-256), unpadded
	// formTTL bounds how long a rendered form may sit before it is submitted.
	// It is independent of oauth.code_ttl, which only starts at submission.
	formTTL = 15 * time.Minute
	// retryAfterUnavailable is the Retry-After (seconds) of a 502 when Garmin
	// could not verify the key.
	retryAfterUnavailable = "30"
	// keyField is the form field holding the Personal API key.
	keyField = "api_key"
	// requestField is the hidden form field holding the sealed request.
	requestField = "request"
	// acceptTermsField is the terms-of-use checkbox; acceptTermsValue is what
	// the form's checkbox sends when ticked.
	acceptTermsField = "accept_terms"
	acceptTermsValue = "on"
	// sessionField is the radio group of the session lifetime; its values are
	// the canonical config.SessionChoice strings.
	sessionField = "session"
)

// OAuth error codes (RFC 6749 §4.1.2.1, RFC 8707).
const (
	codeInvalidRequest          = "invalid_request"
	codeUnsupportedResponseType = "unsupported_response_type"
	codeInvalidTarget           = "invalid_target"
	codeServerError             = "server_error"
)

var (
	//go:embed templates/style.css
	styleCSS string
	//go:embed templates/authorize.html
	authorizeHTML string
	//go:embed templates/error.html
	errorHTML string

	authorizeTmpl = template.Must(template.New("authorize").Funcs(templateFuncs).Parse(authorizeHTML))
	errorTmpl     = template.Must(template.New("error").Funcs(templateFuncs).Parse(errorHTML))

	// styleSrc is the CSP source for the one inline stylesheet, so the policy
	// needs neither 'unsafe-inline' nor a per-request nonce.
	styleSrc = func() string {
		sum := sha256.Sum256([]byte(styleCSS))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}()
)

// authRequest is the validated authorization request. It is sealed
// (seal.KindAuthRequest) into the form's hidden field, which binds the GET
// parameters to the POST: nothing the user's browser sends back can alter it.
type authRequest struct {
	ClientID      string `json:"c"`
	RedirectURI   string `json:"r"`
	CodeChallenge string `json:"ch"`
	State         string `json:"s,omitempty"`
}

// authFailure is a failed authorize request. When RedirectURI is empty it is
// shown on an error page (the redirect target is not established, so
// redirecting would make this an open redirector); otherwise the user agent
// is redirected back to the client with the error.
type authFailure struct {
	Status      int // error page status
	Code        string
	Description string  // English, for logs and redirect error_description
	Message     message // what the error page shows
	RedirectURI string
	State       string
}

// pageFailure is an error page showing the catalog message id.
func pageFailure(status int, id string, args ...any) *authFailure {
	m := uiMsg(id, args...)
	return &authFailure{Status: status, Message: m, Description: msgText(m)}
}

// redirectFailure is only for requests whose client and redirect_uri have
// been verified.
func redirectFailure(uri, state, code, desc string) *authFailure {
	return &authFailure{Code: code, Description: desc, RedirectURI: uri, State: state}
}

// termsView is the terms block of the form.
type termsView struct {
	Paragraphs []string
}

// sessionView is the "Stay connected for" radio group.
type sessionView struct {
	Field   string
	Options []sessionOption
	Invalid bool // the submitted value was missing or not offered
}

// sessionOption is one radio button.
type sessionOption struct {
	Value   string // canonical config.SessionChoice string
	Label   string
	Checked bool
}

// formPage is the data of templates/authorize.html. Every user-visible string
// not from the operator's config or the request is a catalog message resolved
// through the T and Msg template functions, never built into the data.
type formPage struct {
	CSS          template.CSS
	Action       string
	ClientName   string
	RedirectHost string
	Request      string
	KeyField     string
	Error        message      // zero when there is no banner
	Notice       []string     // operator notice paragraphs, plain text
	Terms        *termsView   // nil when no terms are configured
	TermsInvalid bool         // the terms checkbox was not ticked
	Session      *sessionView // nil when session choices are not configured
	Contact      string       // visible text of the operator contact
	ContactHref  string       // mailto: or https: link target
}

// errorPage is the data of templates/error.html.
type errorPage struct {
	CSS     template.CSS
	Message message
}

// handleAuthorize serves GET (render the credentials form) and POST (issue a
// code). Method filtering is done by the mux patterns. No CORS headers are
// set: this endpoint is only ever navigated to, never fetched cross-origin.
func (s *Handlers) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.sealer == nil {
		s.log.Error("authorization requested without a sealer")
		s.writeErrorPage(w, http.StatusInternalServerError, uiMsg(msgInternal))
		return
	}
	var fail *authFailure
	if r.Method == http.MethodPost {
		fail = s.authorizeSubmit(w, r)
	} else {
		fail = s.authorizeForm(w, r)
	}
	if fail != nil {
		s.writeFailure(w, r, fail)
	}
}

// authorizeForm validates the request and renders the form.
//
// Order matters: errors found before the client and its redirect_uri are
// verified go to an error page; only afterwards may errors be redirected.
func (s *Handlers) authorizeForm(w http.ResponseWriter, r *http.Request) *authFailure {
	q := r.URL.Query()
	clientID, ok := single(q, "client_id")
	if !ok || clientID == "" {
		return pageFailure(http.StatusBadRequest, msgClientIDMissing)
	}
	c, fail := s.lookupClient(clientID)
	if fail != nil {
		return fail
	}
	redirectURI, ok := single(q, "redirect_uri")
	if !ok || !s.redirectAllowed(c, redirectURI) {
		return pageFailure(http.StatusBadRequest, msgRedirectBad)
	}

	// From here on the client and redirect target are trusted.
	state, stateOK := single(q, "state")
	rfail := func(code, desc string) *authFailure { return redirectFailure(redirectURI, state, code, desc) }
	if !stateOK || len(state) > maxStateLen {
		state = "" // never echo a state we refuse to accept
		return rfail(codeInvalidRequest, "invalid state parameter")
	}
	if v, ok := single(q, "response_type"); !ok || v != "code" {
		return rfail(codeUnsupportedResponseType, "response_type must be code")
	}
	challenge, ok1 := single(q, "code_challenge")
	method, ok2 := single(q, "code_challenge_method")
	if !ok1 || !ok2 || challenge == "" {
		return rfail(codeInvalidRequest, "code_challenge is required (PKCE S256)")
	}
	if method != "S256" {
		return rfail(codeInvalidRequest, "code_challenge_method must be S256")
	}
	if !validChallenge(challenge) {
		return rfail(codeInvalidRequest, "malformed code_challenge")
	}
	resource := q["resource"]
	if len(resource) > 1 {
		return rfail(codeInvalidTarget, "only one resource is supported")
	}
	var res string
	if len(resource) == 1 {
		res = resource[0]
	}
	if res != "" && res != ResourceURL(s.cfg.PublicURL) {
		return rfail(codeInvalidTarget, "unknown resource")
	}

	req := authRequest{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: challenge,
		State:         state,
	}
	sealed, err := s.sealer.Seal(seal.KindAuthRequest, req, s.now().Add(formTTL))
	if err != nil {
		s.log.Error("sealing authorize request", "err", err)
		return pageFailure(http.StatusInternalServerError, msgInternal)
	}
	s.writeForm(w, http.StatusOK, formArgs{Client: c, Req: req, Sealed: sealed})
	return nil
}

// authorizeSubmit handles the form POST. It trusts nothing but the sealed
// request: the client and redirect are re-validated against the current
// config, and the credentials are the only values taken from the form.
//
// CSRF: the endpoint relies on no ambient authority (no cookies or session),
// and the credentials come from the form itself, so a forged cross-site POST
// can only submit credentials the attacker already has, and the resulting
// code goes to an allowlisted redirect URI whose client checks its own state.
// The form is also a key-validity oracle (one Garmin call per submit); the
// per-IP authorize rate limit bounds it, and a code is only ever issued for a
// key Garmin accepts.
// The sealed field binds all request parameters (and expires), so the POST
// cannot be steered elsewhere. As defence in depth a browser-declared
// cross-site POST (Sec-Fetch-Site) is refused.
func (s *Handlers) authorizeSubmit(w http.ResponseWriter, r *http.Request) *authFailure {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return pageFailure(http.StatusForbidden, msgCrossSite)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthorizeBody)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return pageFailure(http.StatusRequestEntityTooLarge, msgFormTooLarge)
		}
		return pageFailure(http.StatusBadRequest, msgFormMalformed)
	}
	sealed := r.PostForm.Get(requestField)
	req, err := seal.OpenAs[authRequest](s.sealer, seal.KindAuthRequest, sealed)
	switch {
	case err == nil:
	case errors.Is(err, seal.ErrInvalid), errors.Is(err, seal.ErrExpired):
		return pageFailure(http.StatusBadRequest, msgRequestExpired)
	default:
		s.log.Error("opening authorize request", "err", err)
		return pageFailure(http.StatusInternalServerError, msgInternal)
	}

	c, fail := s.lookupClient(req.ClientID)
	if fail != nil {
		return fail
	}
	if !s.redirectAllowed(c, req.RedirectURI) {
		return pageFailure(http.StatusBadRequest, msgRedirectGone)
	}
	if !validChallenge(req.CodeChallenge) {
		return redirectFailure(req.RedirectURI, req.State, codeInvalidRequest, "malformed code_challenge")
	}

	apiKey, msg := collectKey(r.PostForm)
	form := func(status int, msg message) {
		s.writeForm(w, status, formArgs{Client: c, Req: req, Sealed: sealed, Msg: msg,
			TermsInvalid: msg.ID == msgTermsRequired, Session: r.PostForm.Get(sessionField),
			SessionInvalid: msg.ID == msgSessionRequired})
	}

	// Terms come first: a missing acceptance costs nothing (no sizing, no
	// Garmin call, no global-ceiling charge), and the answer does not depend
	// on the key.
	if s.cfg.OAuth.Page.TermsEnabled() && r.PostForm.Get(acceptTermsField) != acceptTermsValue {
		form(http.StatusBadRequest, uiMsg(msgTermsRequired))
		return nil
	}
	// The session choice is checked next to the terms, for the same reason.
	// Only PostForm is read (a query parameter must not choose a lifetime) and
	// the value must equal the canonical string of an offered choice.
	claim, choice, offered := s.submittedSession(r.PostForm)
	if !offered {
		form(http.StatusBadRequest, uiMsg(msgSessionRequired))
		return nil
	}
	if msg.ID != "" {
		form(http.StatusBadRequest, msg)
		return nil
	}

	// Before any Garmin call and before sealing: a key too long for a token.
	if !keyFits(req, apiKey) {
		form(http.StatusBadRequest, uiMsg(msgKeyTooLong))
		return nil
	}

	if msg, status := s.validateKey(r.Context(), apiKey, r); msg.ID != "" {
		if status == http.StatusBadGateway {
			w.Header().Set("Retry-After", retryAfterUnavailable)
		}
		form(status, msg)
		return nil
	}

	// Only a submission that is about to get a code is charged against the
	// global ceiling (it bounds live codes); everything above is bounded by the
	// per-IP limits charged up front.
	if !s.chargeGlobalAuthorize(w) {
		return pageFailure(http.StatusTooManyRequests, msgTooManyRequests)
	}

	jti, err := newID()
	if err != nil {
		s.log.Error("generating code id", "err", err)
		return redirectFailure(req.RedirectURI, req.State, codeServerError, "internal error")
	}
	ttl := s.cfg.OAuth.CodeTTL
	if ttl <= 0 {
		ttl = config.DefaultCodeTTL
	}
	exp := s.now().Add(ttl)
	code, err := s.sealer.Seal(seal.KindCode, tokens.Code{
		ClientID:      req.ClientID,
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		APIKey:        apiKey,
		JTI:           jti,
		Exp:           exp.Unix(),
		Session:       claim,
	}, exp)
	if err != nil {
		// Never log the payload: it carries credentials.
		s.log.Error("sealing authorization code", "err", err)
		return redirectFailure(req.RedirectURI, req.State, codeServerError, "internal error")
	}
	attrs := []any{"client_ip", s.clientIP(r), "client_id", shortID(req.ClientID), "redirect_host", redirectHost(req.RedirectURI)}
	if len(s.cfg.OAuth.Sessions) > 0 {
		attrs = append(attrs, "session", choice.String())
	}
	s.log.Info("authorization code issued", attrs...)
	s.redirect(w, r, req.RedirectURI, req.State, url.Values{"code": {code}})
	return nil
}

// submittedSession reads the session choice of a submitted form. With no
// choices configured it reports (0, true): the code carries no choice and
// max_session_ttl applies. Otherwise the posted value must be exactly the
// canonical string of a configured choice; the result is that choice as a
// tokens.Code.Session value.
func (s *Handlers) submittedSession(form url.Values) (int64, config.SessionChoice, bool) {
	if len(s.cfg.OAuth.Sessions) == 0 {
		return 0, config.SessionChoice{}, true
	}
	v := form.Get(sessionField)
	for _, c := range s.cfg.OAuth.Sessions {
		if c.String() == v {
			return sessionClaim(c), c, true
		}
	}
	return 0, config.SessionChoice{}, false
}

// sessionGroup builds the radio group: selected (a submitted canonical value,
// possibly junk) stays checked when it is offered, otherwise the default is.
func (s *Handlers) sessionGroup(selected string, invalid bool) *sessionView {
	o := s.cfg.OAuth
	if len(o.Sessions) == 0 {
		return nil
	}
	checked := o.DefaultSession.String()
	for _, c := range o.Sessions {
		if c.String() == selected {
			checked = selected
		}
	}
	v := &sessionView{Field: sessionField, Invalid: invalid}
	for _, c := range o.Sessions {
		v.Options = append(v.Options, sessionOption{Value: c.String(), Label: sessionLabel(c), Checked: c.String() == checked})
	}
	return v
}

// redirectHost returns the host (with port, if any) of a redirect URI.
func redirectHost(uri string) string {
	if u, err := url.Parse(uri); err == nil {
		return u.Host
	}
	return ""
}

// validateKey asks Garmin whether the key works. A non-empty msg means no code
// may be issued: the form is re-rendered with that message and status (400
// rejected; 502 no verdict, so the check fails closed). Neither the key nor
// Garmin's text is logged.
func (s *Handlers) validateKey(ctx context.Context, apiKey string, r *http.Request) (msg message, status int) {
	if s.validator == nil { // misconfigured: fail closed, never issue a code
		s.log.Error("no key validator configured")
		return uiMsg(msgKeyNoVerdict), http.StatusBadGateway
	}
	err := s.validator.Validate(ctx, apiKey)
	if err == nil {
		return message{}, 0
	}
	if errors.Is(err, garmin.ErrKeyRejected) {
		s.log.Info("api key rejected by garmin", "client_ip", s.clientIP(r))
		return uiMsg(msgKeyRejected), http.StatusBadRequest
	}
	// The error may name the Garmin host or the transport failure, never the key.
	s.log.Warn("api key check unavailable", "client_ip", s.clientIP(r), "err", err)
	return uiMsg(msgKeyNoVerdict), http.StatusBadGateway
}

// lookupClient opens a client_id, mapping failures to page failures.
func (s *Handlers) lookupClient(clientID string) (client, *authFailure) {
	c, err := s.openClient(clientID)
	switch {
	case err == nil:
		return c, nil
	case errors.Is(err, errInvalidClient):
		return client{}, pageFailure(http.StatusBadRequest, msgClientUnknown)
	default:
		s.log.Error("opening client id", "err", err)
		return client{}, pageFailure(http.StatusInternalServerError, msgInternal)
	}
}

// collectKey reads the Personal API key from form: trimmed, non-empty, at most
// maxKeyLen bytes, valid UTF-8 without control characters (it ends up in an
// HTTP header). The message never contains the value.
func collectKey(form url.Values) (key string, msg message) {
	v := strings.TrimSpace(form.Get(keyField))
	switch {
	case v == "":
		return "", uiMsg(msgKeyRequired)
	case len(v) > maxKeyLen:
		return "", uiMsg(msgKeyTooLong)
	case !validKey(v):
		return "", uiMsg(msgKeyInvalid)
	}
	return v, message{}
}

// validKey rejects control characters (header injection) and invalid UTF-8.
func validKey(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// formArgs is what writeForm renders: who asks, the sealed request and the
// banner (zero for none). The key is never refilled.
type formArgs struct {
	Client client
	Req    authRequest
	Sealed string
	Msg    message
	// TermsInvalid marks the terms checkbox as the cause of Msg.
	TermsInvalid bool
	// Session is the submitted session value to keep selected (empty on a
	// fresh form); SessionInvalid marks the radio group as the cause of Msg.
	Session        string
	SessionInvalid bool
}

// writeForm renders the credentials form with the security headers.
func (s *Handlers) writeForm(w http.ResponseWriter, status int, a formArgs) {
	c, req, sealed, msg := a.Client, a.Req, a.Sealed, a.Msg
	host, formAction := "", "'self'"
	if u, err := url.Parse(req.RedirectURI); err == nil {
		host = u.Host
		// form-action covers the redirect after submission too: Chrome
		// enforces it on the redirect to the redirect_uri, so that origin
		// must be listed.
		if u.Scheme != "" && u.Host != "" {
			formAction += " " + u.Scheme + "://" + u.Host
		}
	}
	page := s.cfg.OAuth.Page
	data := formPage{
		CSS:          template.CSS(styleCSS),
		Action:       PathAuthorize,
		ClientName:   c.Name,
		RedirectHost: host,
		Request:      sealed,
		KeyField:     keyField,
		Error:        msg,
		Notice:       page.NoticeParagraphs(),
		TermsInvalid: a.TermsInvalid,
		Session:      s.sessionGroup(a.Session, a.SessionInvalid),
		Contact:      page.ContactText(),
		ContactHref:  page.ContactHref(),
	}
	if page.TermsEnabled() {
		data.Terms = &termsView{Paragraphs: page.TermsParagraphs()}
	}
	s.renderPage(w, status, formAction, authorizeTmpl, data)
}

// renderPage executes tmpl into a buffer first, so a template error yields a
// clean 500 instead of a truncated page with a success status.
func (s *Handlers) renderPage(w http.ResponseWriter, status int, formAction string, tmpl *template.Template, data any) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		s.log.Error("rendering authorize page", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	setPageHeaders(w, formAction)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func setPageHeaders(w http.ResponseWriter, formAction string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src "+styleSrc+
		"; form-action "+formAction+"; frame-ancestors 'none'; base-uri 'none'")
}

func (s *Handlers) writeErrorPage(w http.ResponseWriter, status int, m message) {
	s.renderPage(w, status, "'none'", errorTmpl, errorPage{
		CSS: template.CSS(styleCSS), Message: m,
	})
}

// writeFailure renders an error page or redirects the error to the client.
func (s *Handlers) writeFailure(w http.ResponseWriter, r *http.Request, f *authFailure) {
	if f.RedirectURI == "" {
		if f.Status >= 500 {
			s.log.Warn("authorize request failed", "status", f.Status)
		} else {
			s.log.Debug("authorize request rejected", "reason", f.Description)
		}
		s.writeErrorPage(w, f.Status, f.Message)
		return
	}
	s.log.Debug("authorize request rejected", "error", f.Code, "reason", f.Description)
	s.redirect(w, r, f.RedirectURI, f.State, url.Values{"error": {f.Code}, "error_description": {f.Description}})
}

// redirect sends the user agent to the (already verified) redirect URI with
// params, state (when present) and the issuer (RFC 9207).
func (s *Handlers) redirect(w http.ResponseWriter, r *http.Request, redirectURI, state string, params url.Values) {
	u, err := url.Parse(redirectURI)
	if err != nil { // unreachable: it is allowlisted config
		s.writeErrorPage(w, http.StatusInternalServerError, uiMsg(msgInternal))
		return
	}
	q := u.Query()
	// Stale values in the registered URI's query must not survive.
	for _, k := range []string{"code", "error", "error_description", "state", "iss"} {
		q.Del(k)
	}
	for k, v := range params {
		q[k] = v
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", s.cfg.PublicURL)
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// 303 after a POST so the browser never re-sends the credentials.
	status := http.StatusFound
	if r.Method == http.MethodPost {
		status = http.StatusSeeOther
	}
	http.Redirect(w, r, u.String(), status)
}

// single returns the only value of key; ok is false if it occurs more than
// once (RFC 6749 §3.1: parameters must not be repeated).
func single(q url.Values, key string) (string, bool) {
	v := q[key]
	switch len(v) {
	case 0:
		return "", true
	case 1:
		return v[0], true
	}
	return "", false
}

// validChallenge reports whether c is a base64url SHA-256 digest, the only
// shape an S256 code_challenge can have.
func validChallenge(c string) bool {
	if len(c) != pkceChallengeLen {
		return false
	}
	return strings.IndexFunc(c, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
	}) < 0
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("reading random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
