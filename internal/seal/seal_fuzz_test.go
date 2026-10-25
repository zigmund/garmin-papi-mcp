package seal

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

var fuzzKinds = []Kind{KindClient, KindCode, KindAccess, KindRefresh, KindAuthRequest}

// fuzzKind maps an arbitrary byte to a kind; one extra slot is an unknown kind.
func fuzzKind(i uint8) Kind {
	if int(i)%(len(fuzzKinds)+1) == len(fuzzKinds) {
		return Kind("unknown-kind")
	}
	return fuzzKinds[int(i)%(len(fuzzKinds)+1)]
}

type fuzzPayload struct {
	User string `json:"user"`
	N    int    `json:"n"`
}

// Fixed tokens sealed once with keys key(1), key(2) (primary: key(1)) at the
// fixedNow clock: valid for an hour past it, or expired an hour before it.
// They are constants, not generated per run, because fuzz workers are separate
// processes that must agree on which strings are genuine.
var fuzzGenuine = []struct {
	kind           Kind
	valid, expired string
}{
	{Kind("client"), "ARLQ6djUwqASHrS30FZX7bsDjnXt10ep39JHXBF4Gb7bXQVB5zEC3YrVVV9o-QPLc9rOGb0BJ_qlWVy4iON7jYPYYTxiHemO6HcmPdFr",
		"ARLQ6dgVUzrpG_nnFg_mW9xQFYmKEvr30N5jxoeihXRP_EC2f5RkYZ0aKR9G200jzE4c9jMF5n3XA8qXSE00uTOSnPdkV0w16f2Br47q"},
	{Kind("code"), "ARLQ6dgVSewcOQKpwcevRriW3xiWCTiea5dYaM8rtsHA_fKJQ98UlAEDgm7y3ituIU4eZsQ8Jq4A3ddUx8mUDrH6Ai-gTYHano92VsH0",
		"ARLQ6dg7z-vuM7SLIE3U0EWCXQw5mFME2ji_FQdqHWGZdT8xHuF5hL7wMd4VAreQnw7G5xWLIM2VXxyuFfCucpbr4gxh7PFHvnhK6qTv"},
	{Kind("access"), "ARLQ6djq8Fj9xCpFAHJoQuR6D06l9HWti-A9kyx-505V67B8ImKaWLjY9wU15SSsxVUeiAcvLeF4KPX9BC2FwCpGNerL_fCDTqt3rd9A",
		"ARLQ6djeLPJ7R4HmnuS8ElJfrtypUbaf8Im2mRK005cIVuBhRVD2xVBAbM5jRjV5_4H4yGbsw9DU_inE9YzOpso8faXNq1qADPgTgqQ8"},
	{Kind("refresh"), "ARLQ6dig2AHFvXZL4Q02L-ETyw70F07EIVvCJ9M4Aw4Ux6jdDUxslNQ2AYRsvLZHlTOdeoRsQCdY70unJYeFNNYLn89CSXyp3dR3fsRn",
		"ARLQ6diNOUooWQQqMz15xQZLE86WgDE1ey_i8NIIMOTYXVKulJZlFeyGh-YxP8MSTtvkrtXVyTjaQQyTNUtbXkuArBUR4mmI9oXyAa8g"},
	{Kind("authreq"), "ARLQ6dgyZJddZJjFthUkdvL5HW5HwFm1o3a-a3MTCpvhukKU8FpnvJ8hYsgOmqlWE7k3Y0xjjkfW3xEcbx5mcgR78DC2IYSQ0DofJ-7S",
		"ARLQ6dg2sHonnccXaEVwvT4dELtOpvmaiDKvARScF9615DGMeYeVHidJ48NyCmTO-evKuWWE1W8JRuGPjAwZdnY0dGYQWQUmLBuXyVuf"},
}

func fuzzSealer(tb testing.TB, keys ...[]byte) *Sealer {
	tb.Helper()
	s, err := New(keys, WithClock(func() time.Time { return fixedNow }))
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

// FuzzOpen feeds arbitrary strings to Open for every kind. Nothing may panic,
// and Open may succeed only for tokens this test sealed itself, and only for
// the kind they were sealed as.
func FuzzOpen(f *testing.F) {
	s := fuzzSealer(f, key(1), key(2))
	good := map[string]Kind{} // genuine unexpired tokens
	expired := map[string]Kind{}
	for _, g := range fuzzGenuine {
		good[g.valid], expired[g.expired] = g.kind, g.kind
		f.Add(uint8(0), g.valid)
		f.Add(uint8(1), g.valid)
		f.Add(uint8(2), g.expired)
	}
	for _, seed := range []string{
		"", "A", "AAAA", "=", "AQ==", "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"AQ\r\nAA", "-_-_", string(bytes.Repeat([]byte("A"), 20<<10)),
	} {
		f.Add(uint8(0), seed)
	}

	other := fuzzSealer(f, key(9)) // unrelated keys
	f.Fuzz(func(t *testing.T, kindIdx uint8, token string) {
		kind := fuzzKind(kindIdx)
		var out fuzzPayload
		err := s.Open(kind, token, &out)
		if err == nil {
			if good[token] != kind {
				t.Fatalf("Open(%q) succeeded for a token not sealed as that kind", kind)
			}
			if out.User != "alice" || out.N != 7 {
				t.Fatalf("payload = %+v", out)
			}
			return
		}
		if errors.Is(err, ErrExpired) {
			if expired[token] != kind {
				t.Fatalf("ErrExpired for a token that is not an expired %q token", kind)
			}
			return
		}
		if good[token] == kind && kind != "" {
			t.Fatalf("valid token rejected: %v", err)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
		// The generic wrapper agrees.
		if _, err2 := OpenAs[fuzzPayload](s, kind, token); err2 == nil {
			t.Fatal("OpenAs succeeded where Open failed")
		}
		// A sealer with unrelated keys accepts nothing.
		if _, err := OpenAs[fuzzPayload](other, kind, token); err == nil {
			t.Fatal("token opened under foreign keys")
		}
	})
}

// FuzzSealRoundTrip checks Seal -> Open, kind separation, key rotation and
// that no single-byte modification of a token is accepted.
func FuzzSealRoundTrip(f *testing.F) {
	f.Add(uint8(0), []byte("hello"), uint16(0), uint8(1))
	f.Add(uint8(2), []byte(`{"u":"tn"}`), uint16(40), uint8(0x80))
	f.Add(uint8(4), []byte{}, uint16(3), uint8(0xff))
	f.Add(uint8(1), bytes.Repeat([]byte{0xff}, 512), uint16(900), uint8(4))

	oldOnly := fuzzSealer(f, key(1))
	rotated := fuzzSealer(f, key(2), key(1)) // [new, old]
	newOnly := fuzzSealer(f, key(2))

	f.Fuzz(func(t *testing.T, kindIdx uint8, payload []byte, pos uint16, mask uint8) {
		kind := fuzzKind(kindIdx)
		tok, err := rotated.Seal(kind, payload, time.Time{})
		if err != nil {
			return // oversized payload
		}
		got, err := OpenAs[[]byte](rotated, kind, tok)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("round trip: got %q err %v, want %q", got, err, payload)
		}
		// Sealed under the new key: the new-only sealer opens it, the old-only one does not.
		if _, err := OpenAs[[]byte](newOnly, kind, tok); err != nil {
			t.Fatalf("new-key token rejected by new-only sealer: %v", err)
		}
		if _, err := OpenAs[[]byte](oldOnly, kind, tok); !errors.Is(err, ErrInvalid) {
			t.Fatalf("new-key token under old-only sealer: %v", err)
		}
		// A token from before the rotation still opens under [new, old].
		oldTok, err := oldOnly.Seal(kind, payload, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := OpenAs[[]byte](rotated, kind, oldTok); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("old-key token under rotated sealer: %q %v", got, err)
		}
		if _, err := OpenAs[[]byte](newOnly, kind, oldTok); !errors.Is(err, ErrInvalid) {
			t.Fatalf("old-key token after dropping the key: %v", err)
		}
		// Never crosses kinds.
		for _, other := range append(fuzzKinds, "unknown-kind") {
			if other == kind {
				continue
			}
			if _, err := OpenAs[[]byte](rotated, other, tok); !errors.Is(err, ErrInvalid) {
				t.Fatalf("token sealed as %q opened as %q: %v", kind, other, err)
			}
		}
		// Any modification fails: a substituted character, a flipped raw bit,
		// truncation, extension and padding.
		mut := []byte(tok)
		i := int(pos) % len(mut)
		if mask == 0 {
			mask = 1
		}
		mut[i] ^= mask
		mustFail(t, rotated, kind, string(mut), "substituted char")

		raw, _ := base64.RawURLEncoding.DecodeString(tok)
		raw[int(pos)%len(raw)] ^= mask
		mustFail(t, rotated, kind, base64.RawURLEncoding.EncodeToString(raw), "flipped raw byte")
		mustFail(t, rotated, kind, tok[:len(tok)-1], "truncated")
		mustFail(t, rotated, kind, tok+"A", "extended")
		mustFail(t, rotated, kind, tok+"=", "padded")
		mustFail(t, rotated, kind, tok+"\n", "newline")
		mustFail(t, rotated, kind, "", "empty")
	})
}

func mustFail(t *testing.T, s *Sealer, kind Kind, token, what string) {
	t.Helper()
	var out []byte
	if err := s.Open(kind, token, &out); err == nil {
		t.Fatalf("%s token accepted", what)
	}
}

// TestFuzzGenuineTokens guards the constants above: each must open under its
// own kind (valid) or report expiry (expired) with the keys used by FuzzOpen.
func TestFuzzGenuineTokens(t *testing.T) {
	s := fuzzSealer(t, key(1), key(2))
	for _, g := range fuzzGenuine {
		got, err := OpenAs[fuzzPayload](s, g.kind, g.valid)
		if err != nil || got.User != "alice" || got.N != 7 {
			t.Errorf("%s: valid token: %+v %v", g.kind, got, err)
		}
		if _, err := OpenAs[fuzzPayload](s, g.kind, g.expired); !errors.Is(err, ErrExpired) {
			t.Errorf("%s: expired token: %v", g.kind, err)
		}
	}
}
