package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// A Garmin error status is reported as such even when its body would read as
// "no data": a 400 with "{}" is "Garmin returned HTTP 400".
func TestGarmin400EmptyObjectIsStatusError(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	s.garmin.status.Store(http.StatusBadRequest)
	s.garmin.body.Store("{}")
	res := s.call(t, l.access, "get_steps", `{"start_date":"2026-01-01","end_date":"2026-01-02"}`)
	if !res.IsError || !strings.Contains(res.text(), "Garmin returned HTTP 400") || strings.Contains(res.text(), "no data") {
		t.Fatalf("400 {} = %+v, want the tool error \"Garmin returned HTTP 400\"", res)
	}
}

// A refresh token is not an access token: as a bearer on /mcp it gets a 401
// challenge and Garmin is not called.
func TestRefreshTokenAsBearerRefused(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	ex, data := s.garmin.exchanges.Load(), s.garmin.data.Load()
	resp, _ := s.rpc(t, l.refresh, callBody("get_profile", "{}"))
	s.wantChallenge(t, resp, true)
	if s.garmin.exchanges.Load() != ex || s.garmin.data.Load() != data {
		t.Error("a refresh token used as bearer reached Garmin")
	}
}

// Garmin 5xx and 429 are failures of the call, not of the key: they do not
// mark the key rejected, so the next call and the refresh grant still work.
func TestGarminServerErrorsDoNotRejectKey(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s := newStack(t, stackOpts{})
			l := s.login(t)
			s.garmin.status.Store(int32(status))
			if res := s.call(t, l.access, "get_profile", "{}"); !res.IsError {
				t.Fatalf("call during the Garmin failure = %+v, want a tool error", res)
			}
			s.garmin.status.Store(0)
			if res := s.call(t, l.access, "get_profile", "{}"); res.IsError {
				t.Fatalf("follow-up call = %+v", res)
			}
			access, _ := s.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {l.refresh}, "client_id": {l.clientID}})
			if res := s.call(t, access, "get_profile", "{}"); res.IsError {
				t.Fatalf("call with the refreshed token = %+v", res)
			}
		})
	}
}
