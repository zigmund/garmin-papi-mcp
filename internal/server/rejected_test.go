package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/seal"
)

func TestRejectedKeysTTLAndCap(t *testing.T) {
	sealer, err := seal.New([][]byte{mustB64(t, testKey("rejected"))})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	r := newRejectedKeys(sealer, time.Hour, 3)
	r.now = func() time.Time { return now }

	r.Add("k1")
	if !r.Rejected("k1") || r.Rejected("k2") {
		t.Fatal("membership wrong")
	}
	// Entries are MACs, never the key itself.
	r.mu.Lock()
	for k := range r.m {
		if strings.Contains(k, "k1") || k != keyMAC(sealer, rejectedPurpose, "k1") {
			t.Errorf("entry %q is not the MAC of the key", k)
		}
	}
	r.mu.Unlock()

	// TTL.
	now = now.Add(59 * time.Minute)
	if !r.Rejected("k1") {
		t.Error("expired early")
	}
	now = now.Add(2 * time.Minute)
	if r.Rejected("k1") {
		t.Error("still rejected after the TTL")
	}

	// Size cap: the oldest entry makes room; the set never grows past it.
	for i := range 10 {
		r.Add("key-" + strconv.Itoa(i))
		now = now.Add(time.Second)
	}
	if n := r.Len(); n != 3 {
		t.Errorf("Len = %d, want the cap 3", n)
	}
	if r.Rejected("key-0") || !r.Rejected("key-9") || !r.Rejected("key-7") {
		t.Error("eviction is not oldest-first")
	}

	// Re-adding refreshes the entry; Forget removes it.
	r.Add("key-9")
	r.Forget("key-9")
	if r.Rejected("key-9") {
		t.Error("Forget did not remove the key")
	}
	r.Forget("never-added") // no-op
}

// Item 1 of the review: a key Garmin rejected mid-session is not revived by
// the refresh grant, is not sent to Garmin again while it stays rejected, and
// works again after the user re-authorizes with it.
func TestRejectedKeyForcesReauthorization(t *testing.T) {
	s := newStack(t, stackOpts{})
	l := s.login(t)
	if res := s.call(t, l.access, "get_profile", "{}"); res.IsError {
		t.Fatalf("first call = %+v", res)
	}
	s.garmin.reject.Store(true) // the key expired at Garmin
	resp, _ := s.rpc(t, l.access, callBody("get_profile", "{}"))
	s.wantChallenge(t, resp, true)

	// The same access token is refused without another Garmin round trip.
	ex, data := s.garmin.exchanges.Load(), s.garmin.data.Load()
	resp, _ = s.rpc(t, l.access, callBody("get_profile", "{}"))
	s.wantChallenge(t, resp, true)
	if s.garmin.exchanges.Load() != ex || s.garmin.data.Load() != data {
		t.Error("a rejected key was sent to Garmin again")
	}

	// The refresh grant does not revive it: the client must re-authorize.
	resp, body := s.postForm(t, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {l.refresh}, "client_id": {l.clientID}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), `"invalid_grant"`) {
		t.Fatalf("refresh with a rejected key = %d %s, want 400 invalid_grant", resp.StatusCode, body)
	}

	// The user renews the key at Garmin and signs in again with it.
	s.garmin.reject.Store(false)
	l2 := s.login(t)
	if res := s.call(t, l2.access, "get_profile", "{}"); res.IsError {
		t.Fatalf("after re-authorization = %+v", res)
	}
	if res := s.call(t, l.access, "get_profile", "{}"); res.IsError {
		t.Fatalf("old token after re-authorization = %+v", res)
	}
}
