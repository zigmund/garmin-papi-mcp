package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

func TestMapErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"rate limit with retry-after", &garmin.RateLimitedError{RetryAfter: 30 * time.Second}, "Garmin is rate limiting this account, retry in 30s"},
		{"rate limit without retry-after", &garmin.RateLimitedError{}, "Garmin is rate limiting this account, retry later"},
		{"status", &garmin.StatusError{Status: 500}, "Garmin returned HTTP 500"},
		{"other", errors.New("dial tcp: secret-host"), "could not reach Garmin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := mapErr(c.err)
			if err != nil || len(res.Errors) != 1 || res.Errors[0] != c.want || !res.IsError() {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
	t.Run("key rejected", func(t *testing.T) {
		_, err := mapErr(garmin.ErrKeyRejected)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestFetchJSON(t *testing.T) {
	get := func(r replyA) (any, bool, Result, error) {
		g := &fakeGetterA{replies: map[string]replyA{"p": r}}
		return fetchJSON(context.Background(), g, "key", "p", nil)
	}
	t.Run("data", func(t *testing.T) {
		d, nd, res, err := get(okA(`{"a":1}`))
		if err != nil || nd || len(res.Errors) > 0 || d.(map[string]any)["a"] != float64(1) {
			t.Fatalf("%v %v %+v %v", d, nd, res, err)
		}
	})
	for name, r := range map[string]replyA{
		"204":   {resp: garmin.Response{Status: 204}},
		"empty": okA(""),
		"blank": okA("  \n"),
		"null":  okA("null"),
		"array": okA("[]"),
		"obj":   okA("{}"),
	} {
		t.Run(name, func(t *testing.T) {
			d, nd, res, err := get(r)
			if err != nil || !nd || d != nil || len(res.Errors) > 0 {
				t.Fatalf("%v %v %+v %v", d, nd, res, err)
			}
		})
	}
	t.Run("unreadable body", func(t *testing.T) {
		_, nd, res, err := get(okA("<html>secret body</html>"))
		if err != nil || nd || len(res.Errors) != 1 || res.Errors[0] != "Garmin returned an unreadable response" {
			t.Fatalf("%v %+v %v", nd, res, err)
		}
	})
	t.Run("status error", func(t *testing.T) {
		_, _, res, err := get(replyA{err: &garmin.StatusError{Status: 404}})
		if err != nil || len(res.Errors) != 1 || res.Errors[0] != "Garmin returned HTTP 404" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		_, _, res, err := get(replyA{err: &garmin.RateLimitedError{RetryAfter: 5 * time.Second}})
		if err != nil || len(res.Errors) != 1 || res.Errors[0] != "Garmin is rate limiting this account, retry in 5s" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("key rejected", func(t *testing.T) {
		_, _, _, err := get(replyA{err: garmin.ErrKeyRejected})
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("err = %v", err)
		}
	})
}
