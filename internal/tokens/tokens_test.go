package tokens

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zigmund/garmin-papi-mcp/internal/seal"
)

// secret is an obviously synthetic key.
const secret = "SECRETKEY"

func leaks(t *testing.T, vals ...any) {
	t.Helper()
	var texts []string
	for _, v := range vals {
		texts = append(texts, fmt.Sprint(v), fmt.Sprintf("%v %+v %#v", v, v, v))
		for _, h := range []func(*bytes.Buffer) slog.Handler{
			func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
			func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		} {
			var buf bytes.Buffer
			slog.New(h(&buf)).Info("x", "v", v, slog.Any("any", v))
			texts = append(texts, buf.String())
		}
	}
	for _, s := range texts {
		if strings.Contains(s, secret) {
			t.Errorf("secret leaked: %s", s)
		}
	}
}

func TestAccessTokenRedaction(t *testing.T) {
	leaks(t, Access{ClientID: "c", APIKey: secret})
}

func TestRedaction(t *testing.T) {
	leaks(t,
		Access{ClientID: "c", APIKey: secret},
		Refresh{ClientID: "c", APIKey: secret},
		Code{ClientID: "c", APIKey: secret},
		&Access{ClientID: "c", APIKey: secret},
	)
}

func TestJSONKeys(t *testing.T) {
	b, err := json.Marshal(Access{ClientID: "c", APIKey: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"c":"c","k":"v"}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	b, err = json.Marshal(Refresh{ClientID: "c", APIKey: "v", Family: "f", Gen: 2, Exp: 7, Start: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"c":"c","k":"v","f":"f","g":2,"e":7,"s":3}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestCodeJSONKeys(t *testing.T) {
	b, err := json.Marshal(Code{ClientID: "c", RedirectURI: "r", CodeChallenge: "ch", APIKey: "v", JTI: "j", Exp: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"c":"c","r":"r","ch":"ch","k":"v","j":"j","e":7}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestOpenWrongKind(t *testing.T) {
	// Synthetic, non-repeating-pattern key made for the test only.
	k := []byte("0123456789abcdefghijklmnopqrstuv")
	s, err := seal.New([][]byte{k})
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	refresh, err := s.Seal(seal.KindRefresh, Refresh{ClientID: "c", APIKey: "k", Family: "f"}, exp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seal.OpenAs[Access](s, seal.KindAccess, refresh); err == nil {
		t.Fatal("refresh blob opened as access")
	}
	got, err := seal.OpenAs[Refresh](s, seal.KindRefresh, refresh)
	if err != nil || got.Family != "f" || got.APIKey != "k" {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
}
