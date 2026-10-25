package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// skippedRun runs the live test as a subtest and reports whether it skipped.
func skippedRun(t *testing.T) (skipped bool) {
	t.Helper()
	t.Run("live", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		TestLiveAllReadOnlyTools(t)
	})
	return skipped
}

func TestLiveSkipsWhenEnvUnset(t *testing.T) {
	t.Setenv(liveEnv, "")
	// A key file that would be found must not matter, and must not be read.
	t.Setenv(liveKeyFileEnv, filepath.Join(t.TempDir(), "never-read.key"))
	if !skippedRun(t) {
		t.Fatal("live test did not skip with GARMIN_LIVE unset")
	}
	if _, reason := liveKey(os.Getenv, os.ReadFile); reason != "GARMIN_LIVE is not 1; live test skipped" {
		t.Fatalf("skip reason = %q", reason)
	}
}

func TestLiveSkipsWhenKeyFileMissing(t *testing.T) {
	t.Setenv(liveEnv, "1")
	t.Setenv(liveKeyFileEnv, filepath.Join(t.TempDir(), "missing.key"))
	if !skippedRun(t) {
		t.Fatal("live test did not skip with a missing key file")
	}
}

func TestLiveKeyDoesNotReadFileUnlessEnabled(t *testing.T) {
	read := func(string) ([]byte, error) {
		t.Fatal("key file was read although GARMIN_LIVE is not 1")
		return nil, errors.New("unreachable")
	}
	for _, v := range []string{"", "0", "true"} {
		env := func(k string) string {
			if k == liveEnv {
				return v
			}
			return ""
		}
		if key, skip := liveKey(env, read); key != "" || skip == "" {
			t.Errorf("GARMIN_LIVE=%q: key=%q skip=%q", v, key, skip)
		}
	}
}

func TestLiveKeyReadsConfiguredFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(p, []byte("  test-key-not-real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		switch k {
		case liveEnv:
			return "1"
		case liveKeyFileEnv:
			return p
		}
		return ""
	}
	if key, skip := liveKey(env, os.ReadFile); key != "test-key-not-real" || skip != "" {
		t.Fatalf("key=%q skip=%q", key, skip)
	}
}
