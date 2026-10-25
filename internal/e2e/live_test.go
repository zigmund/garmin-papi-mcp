package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Live test: opt-in, talks to the real Garmin Personal API.
//
//	GARMIN_LIVE=1 go test ./internal/e2e/ -run Live -v
//
// The key is read from GARMIN_API_KEY_FILE (default ../../test/api.key,
// relative to this package). The test prints only the tool name, an outcome
// (ok, no-data, tool-error), the result size and, for tool errors, the
// generic error line of the result. It never logs payloads, the key, tokens
// or anything else derived from a response.

const (
	liveEnv         = "GARMIN_LIVE"
	liveKeyFileEnv  = "GARMIN_API_KEY_FILE"
	liveDefaultFile = "../../test/api.key"
)

// liveKey returns the API key, or a skip reason. It reads nothing unless
// GARMIN_LIVE=1, so a plain `go test ./...` never touches the key file.
func liveKey(getenv func(string) string, read func(string) ([]byte, error)) (key, skip string) {
	if getenv(liveEnv) != "1" {
		return "", liveEnv + " is not 1; live test skipped"
	}
	path := getenv(liveKeyFileEnv)
	if path == "" {
		path = liveDefaultFile
	}
	raw, err := read(path)
	if err != nil {
		return "", "API key file " + path + " is not readable; live test skipped (set " + liveKeyFileEnv + ")"
	}
	key = strings.TrimSpace(string(raw))
	if key == "" {
		return "", "API key file " + path + " is empty; live test skipped"
	}
	return key, ""
}

// classify maps a call outcome to ok, no-data or tool-error.
func classify(out callOutcome) string {
	switch {
	case out.isError:
		return "tool-error"
	case strings.HasPrefix(out.text, "no ") && !strings.ContainsAny(out.text, "{["):
		// A note without any JSON: "no data for ...", "no activities ...".
		return "no-data"
	}
	return "ok"
}

// authFailure reports an error line that means the key was refused.
func authFailure(line string) bool {
	return strings.Contains(line, "HTTP 401") || strings.Contains(line, "HTTP 403")
}

func TestLiveAllReadOnlyTools(t *testing.T) {
	key, skip := liveKey(os.Getenv, os.ReadFile)
	if skip != "" {
		t.Skip(skip)
	}

	s := newStack(t, "", key, false)
	ymd := "2006-01-02"
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1).Format(ymd)
	weekAgo := now.AddDate(0, 0, -7).Format(ymd)
	rng := rangeArgs(weekAgo, yesterday)
	dateArg := fmt.Sprintf(`{"date":%q}`, yesterday)

	if n := len(s.listTools(t)); n != 17 {
		t.Fatalf("tools/list returned %d tools, want 17", n)
	}

	type call struct{ tool, args string }
	calls := []call{
		{"get_profile", `{}`},
		{"get_daily_summary", dateArg},
		{"get_sleep", dateArg},
		{"get_heart_rate", dateArg},
		{"get_hrv", dateArg},
		{"get_body_battery", rng},
		{"get_stress", rng},
		{"get_steps", rng},
		{"get_spo2", dateArg},
		{"list_activities", `{"limit":1}`},
		{"get_weight", rng},
		{"get_nutrition", dateArg},
		{"get_hydration", rng},
		{"get_fitness_age", dateArg},
		{"get_wellness_activities", rng},
		{"get_menstrual_cycle", dateArg},
	}

	// Stay well under the default per-token rate limit (60/min, burst 20).
	pace := func() { time.Sleep(1100 * time.Millisecond) }

	run := func(c call) callOutcome {
		out := s.call(t, c.tool, c.args)
		if out.protoErr {
			// Status only: the body is never shown.
			if out.status == 401 {
				t.Errorf("%s: authentication failure (HTTP 401)", c.tool)
			} else {
				t.Errorf("%s: protocol error (HTTP %d)", c.tool, out.status)
			}
			return out
		}
		kind := classify(out)
		line := fmt.Sprintf("%-24s %-10s %6d bytes", c.tool, kind, len(out.text))
		if kind == "tool-error" {
			errLine := firstLines(out.text, "error: ")
			line += "  " + errLine
			if authFailure(errLine) {
				t.Errorf("%s: authentication failure", c.tool)
			}
		}
		t.Log(line)
		if len(out.text) > maxOutputBytes {
			t.Errorf("%s: default output is %d bytes, over %d", c.tool, len(out.text), maxOutputBytes)
		}
		return out
	}

	var activityID int64
	for _, c := range calls {
		pace()
		out := run(c)
		if c.tool == "list_activities" && !out.protoErr && !out.isError {
			activityID = firstActivityID(out.text)
		}
	}

	pace()
	if activityID == 0 {
		t.Log("get_activity              skipped    no activities on this account")
		return
	}
	run(call{"get_activity", fmt.Sprintf(`{"activity_id":%d}`, activityID)})
}

// firstActivityID extracts activities[0].activityId from a list_activities
// result text (a JSON line, possibly after a note line). It returns 0 when
// there is none. Nothing from the text is logged.
func firstActivityID(text string) int64 {
	for _, l := range strings.Split(text, "\n") {
		var v struct {
			Activities []struct {
				ID json.Number `json:"activityId"`
			} `json:"activities"`
		}
		if json.Unmarshal([]byte(l), &v) != nil || len(v.Activities) == 0 {
			continue
		}
		n, err := v.Activities[0].ID.Int64()
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}
