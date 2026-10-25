package config

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exampleCredPath = "token_keys_file: /run/credentials/garmin-papi-mcp.service/keys"

// writeExample writes the shipped example config, with the systemd credential
// path pointed at a freshly generated key file, into a temp dir and returns
// the config path. extra is appended to the YAML.
func writeExample(t *testing.T, extra string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), exampleCredPath) {
		t.Fatalf("example config no longer contains %q; update this test", exampleCredPath)
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.WriteFile(keys, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(string(raw), exampleCredPath, "token_keys_file: "+keys, 1) + extra
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(writeExample(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://garmin-papi-mcp.zigmund.xyz" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if cfg.Listen != "127.0.0.1:8787" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if len(cfg.Secrets.Keys) != 1 {
		t.Errorf("keys = %d, want 1", len(cfg.Secrets.Keys))
	}
	if len(cfg.OAuth.AllowedRedirectURIs) != len(DefaultRedirectURIs()) {
		t.Errorf("redirect URIs = %v", cfg.OAuth.AllowedRedirectURIs)
	}
}

// The example must stay strict-decodable: an unknown key is rejected.
func TestExampleConfigRejectsUnknownKey(t *testing.T) {
	if _, err := Load(writeExample(t, "\nno_such_option: true\n")); err == nil {
		t.Fatal("unknown key accepted; strict decoding is broken")
	}
}

func TestExampleTermsFileIsValid(t *testing.T) {
	if _, err := readTermsFile(filepath.Join("..", "..", "deploy", "terms.example.txt")); err != nil {
		t.Fatal(err)
	}
}
