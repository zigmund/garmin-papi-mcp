package oauth

import (
	"fmt"
	"html/template"
	"strconv"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/config"
)

// Message ids of the authorize and error pages. Templates refer to them as
// string literals ({{T "page.title"}}); a test checks that each one used
// there exists in the catalog.
const (
	// Messages that are produced in Go (form banner, error page).
	msgInternal        = "err.internal"
	msgCrossSite       = "err.cross_site"
	msgFormTooLarge    = "err.form_too_large"
	msgFormMalformed   = "err.form_malformed"
	msgRequestExpired  = "err.request_expired"
	msgRedirectGone    = "err.redirect_gone"
	msgTooManyRequests = "err.too_many_requests"
	msgClientIDMissing = "err.client_id_missing"
	msgRedirectBad     = "err.redirect_bad"
	msgClientUnknown   = "err.client_unknown"
	msgKeyRequired     = "key.required"
	msgKeyTooLong      = "key.too_long"
	msgKeyInvalid      = "key.invalid"
	msgKeyRejected     = "key.rejected"
	msgKeyNoVerdict    = "key.unavailable"
	msgTermsRequired   = "terms.error"
	msgSessionRequired = "session.error"
)

// catalog holds the page text: message id -> text. A text is a fmt format with
// explicit argument indexes (%[1]s). Arguments are plain values and the result
// is plain text: html/template escapes it where it is rendered, so an argument
// (host, title, label) can never inject markup. Never put HTML in a text.
var catalog = map[string]string{
	"terms.summary":      "Terms of use",
	"terms.region_label": "Terms of use text",
	"terms.accept":       "I have read and accept the terms of use",
	msgTermsRequired:     "Accept the terms of use to continue.",

	"session.legend":    "Stay connected for",
	"session.unlimited": "Until revoked",
	"session.year":      "%[1]s year",
	"session.years":     "%[1]s years",
	"session.day":       "%[1]s day",
	"session.days":      "%[1]s days",
	"session.hour":      "%[1]s hour",
	"session.hours":     "%[1]s hours",
	msgSessionRequired:  "Choose how long to stay connected.",

	"page.title":        "Connect Garmin",
	"page.request_tail": "is requesting read access to your Garmin Connect data",

	"key.label": "Personal API key",
	"key.hint":  "Paste the Personal API key from your Garmin Connect account.",

	"client.name":       "Application name:",
	"client.unnamed":    "Unnamed application",
	"client.unverified": "self-declared, unverified",

	"notice.heading": "Notice from the operator",
	"notice.contact": "Contact:",

	"privacy.sealed":      "Your key is sealed into the token and never stored or logged.",
	"privacy.proxy":       "A TLS reverse proxy in front of this server sees the key while you submit it.",
	"privacy.third_party": "This server is not affiliated with Garmin and you are giving your Personal API key to a third party.",
	"privacy.bearer":      "The token is a bearer secret: whoever holds it can use your Garmin data until it expires, and nothing is revoked when you disconnect the connector.",
	"privacy.logs":        "This server logs request metadata only (time, client IP address on sign-in attempts, status); it never logs your key, tokens, or Garmin data.",
	"hint.continue":       "Only continue if you started this connection yourself.",

	"button.authorize": "Authorize",

	"error.title": "Authorization error",
	"error.hint":  "Go back to the application and start the connection again.",

	msgInternal:        "Internal server error.",
	msgCrossSite:       "Cross-site form submission is not allowed.",
	msgFormTooLarge:    "The submitted form is too large.",
	msgFormMalformed:   "The submitted form is malformed.",
	msgRequestExpired:  "This authorization request is invalid or has expired.",
	msgRedirectGone:    "The redirect_uri is no longer allowed for this application.",
	msgTooManyRequests: "Too many requests. Please wait a moment and try again.",
	msgClientIDMissing: "The request is missing a valid client_id.",
	msgRedirectBad:     "The redirect_uri is missing or not allowed for this application.",
	msgClientUnknown:   "The client_id is not recognized. Register the application again.",
	msgKeyRequired:     "Enter your Personal API key.",
	msgKeyTooLong:      "The Personal API key is too long.",
	msgKeyInvalid:      "The Personal API key contains invalid characters.",
	msgKeyRejected:     "Garmin rejected this key. Check it and try again.",
	msgKeyNoVerdict:    "Could not verify the key with Garmin right now. Try again in a moment.",
}

// message is a catalog id with its arguments. The zero value is "no message".
type message struct {
	ID   string
	Args []any
}

func uiMsg(id string, args ...any) message { return message{ID: id, Args: args} }

// text returns the text of id, formatted with args. An id missing from the
// catalog is returned as is, so a typo is visible rather than blank.
func text(id string, args ...any) string {
	t, ok := catalog[id]
	if !ok {
		return id
	}
	if len(args) == 0 {
		return t
	}
	return fmt.Sprintf(t, args...)
}

// sessionLabel is the English radio label of a session choice, derived from its
// duration: "Until revoked", then years (multiples of 8760h), days (multiples
// of 24h) and otherwise hours.
func sessionLabel(c config.SessionChoice) string {
	if c.Unlimited {
		return text("session.unlimited")
	}
	n := int64(c.Duration / time.Hour)
	unit, per := "hour", int64(1)
	switch {
	case n%8760 == 0:
		unit, per = "year", 8760
	case n%24 == 0:
		unit, per = "day", 24
	}
	if n /= per; n != 1 {
		unit += "s"
	}
	return text("session."+unit, strconv.FormatInt(n, 10))
}

// msgText renders m; the zero message is empty.
func msgText(m message) string {
	if m.ID == "" {
		return ""
	}
	return text(m.ID, m.Args...)
}

// templateFuncs expose the catalog to the page templates: {{T "page.title"}}
// and {{Msg .Error}}.
var templateFuncs = template.FuncMap{"T": text, "Msg": msgText}
