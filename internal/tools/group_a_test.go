package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

type callA struct {
	path  string
	query string
}

type replyA struct {
	resp garmin.Response
	err  error
}

// fakeGetterA records calls and answers from a per-path table.
type fakeGetterA struct {
	replies map[string]replyA
	calls   []callA
}

func (f *fakeGetterA) Get(_ context.Context, _ string, path string, q url.Values) (garmin.Response, error) {
	f.calls = append(f.calls, callA{path: path, query: q.Encode()})
	if r, ok := f.replies[path]; ok {
		return r.resp, r.err
	}
	return garmin.Response{Status: 200, Body: []byte(`{"ok":1}`)}, nil
}

func okA(body string) replyA {
	return replyA{resp: garmin.Response{Status: 200, Body: []byte(body)}}
}

func toolA(t *testing.T, name string) Tool {
	t.Helper()
	for _, tl := range GroupA() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %s not found", name)
	return Tool{}
}

func runA(t *testing.T, name string, g Getter, args string) (Result, error) {
	t.Helper()
	return toolA(t, name).Handler(context.Background(), g, "key", json.RawMessage(args))
}

func sizeA(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func pairsA(n int) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "[%d,%d]", 1760000000000+i*60000, 40+i%60)
	}
	sb.WriteString("]")
	return sb.String()
}

func readingsA(n int, key string) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"%s":%d,"readingTimeGMT":"2026-10-24T%02d:%02d:00.0"}`, key, 30+i%50, (i/60)%24, i%60)
	}
	sb.WriteString("]")
	return sb.String()
}

func TestGroupANames(t *testing.T) {
	want := []string{"get_profile", "get_daily_summary", "get_sleep", "get_heart_rate", "get_hrv", "get_body_battery", "get_stress", "get_steps", "get_spo2"}
	got := GroupA()
	if len(got) != len(want) {
		t.Fatalf("got %d tools", len(got))
	}
	for i, tl := range got {
		if tl.Name != want[i] {
			t.Errorf("tool %d = %s want %s", i, tl.Name, want[i])
		}
		if tl.Description == "" || tl.Handler == nil {
			t.Errorf("%s: missing description or handler", tl.Name)
		}
		if tl.InputSchema["type"] != "object" || tl.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: bad schema %v", tl.Name, tl.InputSchema)
		}
		if _, ok := tl.InputSchema["properties"].(map[string]any); !ok {
			t.Errorf("%s: schema has no properties", tl.Name)
		}
	}
}

func TestToolsHitExpectedPathA(t *testing.T) {
	const d, f, to = "2026-10-24", "2026-10-01", "2026-10-07"
	cases := []struct {
		tool, args string
		want       []callA
	}{
		{"get_profile", `{}`, []callA{{"userprofile-service/userprofile/settings", ""}, {"biometric-service/heartRateZones/v2", ""}}},
		{"get_daily_summary", `{"date":"` + d + `"}`, []callA{{"usersummary-service/usersummary/daily", "calendarDate=" + d}}},
		{"get_daily_summary", `{"start_date":"` + f + `","end_date":"` + to + `"}`, []callA{{"usersummary-service/v2/dailysummaries", "endDate=" + to + "&startDate=" + f}}},
		{"get_sleep", `{"date":"` + d + `"}`, []callA{{"sleep-service/sleep/dailySleep", "date=" + d}}},
		{"get_sleep", `{"start_date":"` + f + `","end_date":"` + to + `"}`, []callA{{"sleep-service/sleep/dailySleepSummariesByDate", "endDate=" + to + "&startDate=" + f}}},
		{"get_heart_rate", `{"date":"` + d + `"}`, []callA{{"wellness-service/wellness/dailyHeartRate", "date=" + d}}},
		{"get_hrv", `{"date":"` + d + `"}`, []callA{{"hrv-service/hrv/" + d, ""}}},
		{"get_hrv", `{"start_date":"` + f + `","end_date":"` + to + `"}`, []callA{{"hrv-service/hrv/daily/" + f + "/" + to, ""}}},
		{"get_body_battery", `{"start_date":"` + f + `","end_date":"` + to + `"}`, []callA{{"wellness-service/wellness/bodyBattery/reports/daily", "endDate=" + to + "&startDate=" + f}}},
		{"get_stress", `{"start_date":"` + f + `","end_date":"` + to + `"}`, []callA{{"usersummary-service/stats/stress/daily/" + f + "/" + to, ""}}},
		{"get_steps", `{"start_date":"` + f + `","end_date":"` + to + `"}`, []callA{{"usersummary-service/stats/steps/daily/" + f + "/" + to, ""}}},
		{"get_spo2", `{"date":"` + d + `"}`, []callA{{"wellness-service/wellness/daily/spo2acclimation/" + d, ""}}},
	}
	for _, c := range cases {
		t.Run(c.tool+" "+c.args, func(t *testing.T) {
			g := &fakeGetterA{}
			res, err := runA(t, c.tool, g, c.args)
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err=%v res=%+v", err, res)
			}
			if fmt.Sprint(g.calls) != fmt.Sprint(c.want) {
				t.Fatalf("calls = %v want %v", g.calls, c.want)
			}
			if res.Data == nil {
				t.Fatal("no data")
			}
		})
	}
}

func TestToolsRejectBadArgsA(t *testing.T) {
	cases := []struct{ tool, args string }{
		{"get_heart_rate", `{"date":"24-10-2026"}`},
		{"get_heart_rate", `{"date":"2026-02-30"}`},
		{"get_heart_rate", `{}`},
		{"get_heart_rate", `{"start_date":"2026-10-01","end_date":"2026-10-02"}`},
		{"get_heart_rate", `{"date":"2026-10-01","bogus":1}`},
		{"get_heart_rate", `{"date":5}`},
		{"get_heart_rate", `not json`},
		{"get_spo2", `{"date":"yesterday"}`},
		{"get_spo2", `{}`},
		{"get_daily_summary", `{"date":"bad"}`},
		{"get_daily_summary", `{}`},
		{"get_daily_summary", `{"start_date":"2026-10-01"}`},
		{"get_daily_summary", `{"date":"2026-10-01","start_date":"2026-10-01","end_date":"2026-10-02"}`},
		{"get_daily_summary", `{"start_date":"2026-10-05","end_date":"2026-10-01"}`},
		{"get_sleep", `{"start_date":"2026-10-01","end_date":"nope"}`},
		{"get_hrv", `{"date":"2026-13-01"}`},
		{"get_body_battery", `{"date":"2026-10-01"}`},
		{"get_body_battery", `{"start_date":"2026-10-01"}`},
		{"get_stress", `{"start_date":"x","end_date":"2026-10-01"}`},
		{"get_steps", `{"end_date":"2026-10-01"}`},
		{"get_profile", `{"date":"2026-10-01"}`},
	}
	for _, c := range cases {
		t.Run(c.tool+" "+c.args, func(t *testing.T) {
			g := &fakeGetterA{}
			res, err := runA(t, c.tool, g, c.args)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !res.IsError() || len(res.Errors[0]) == 0 {
				t.Fatalf("expected error result, got %+v", res)
			}
			if len(g.calls) != 0 {
				t.Fatalf("Garmin was called: %v", g.calls)
			}
		})
	}
}

func TestRangeLimitsA(t *testing.T) {
	// Range of n days ending 2026-10-31 (inclusive).
	rng := func(n int) string {
		end, _ := ParseDate("2026-10-31")
		start := end.AddDate(0, 0, -(n - 1))
		return fmt.Sprintf(`{"start_date":"%s","end_date":"2026-10-31"}`, start.Format("2006-01-02"))
	}
	cases := []struct {
		tool string
		max  int
	}{
		{"get_daily_summary", 365},
		{"get_sleep", 31},
		{"get_hrv", 28},
		{"get_body_battery", 31},
		{"get_stress", 28},
		{"get_steps", 28},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			g := &fakeGetterA{replies: map[string]replyA{}}
			// Accepted at the limit; the default fake reply is fine for every path.
			res, err := runA(t, c.tool, g, rng(c.max))
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("%d days rejected: %v %+v", c.max, err, res)
			}
			before := len(g.calls)
			res, err = runA(t, c.tool, g, rng(c.max+1))
			if err != nil || !res.IsError() {
				t.Fatalf("%d days accepted: %v %+v", c.max+1, err, res)
			}
			if len(g.calls) != before {
				t.Fatal("Garmin called for over-limit range")
			}
			if !strings.Contains(res.Errors[0], fmt.Sprint(c.max)) {
				t.Errorf("error does not state the limit: %q", res.Errors[0])
			}
		})
	}
}

func TestEmptyIsNoDataA(t *testing.T) {
	calls := []struct{ tool, args string }{
		{"get_daily_summary", `{"date":"2026-10-24"}`},
		{"get_sleep", `{"date":"2026-10-24"}`},
		{"get_heart_rate", `{"date":"2026-10-24"}`},
		{"get_hrv", `{"date":"2026-10-24"}`},
		{"get_body_battery", `{"start_date":"2026-10-20","end_date":"2026-10-24"}`},
		{"get_stress", `{"start_date":"2026-10-20","end_date":"2026-10-24"}`},
		{"get_steps", `{"start_date":"2026-10-20","end_date":"2026-10-24"}`},
		{"get_spo2", `{"date":"2026-10-24"}`},
	}
	for _, body := range []struct {
		name   string
		status int
		body   string
	}{{"204", 204, ""}, {"array", 200, "[]"}, {"object", 200, "{}"}} {
		for _, c := range calls {
			t.Run(body.name+" "+c.tool, func(t *testing.T) {
				g := &fakeGetterA{replies: map[string]replyA{}}
				for _, p := range []string{
					"usersummary-service/usersummary/daily", "sleep-service/sleep/dailySleep", "wellness-service/wellness/dailyHeartRate",
					"hrv-service/hrv/2026-10-24", "wellness-service/wellness/bodyBattery/reports/daily",
					"usersummary-service/stats/stress/daily/2026-10-20/2026-10-24", "usersummary-service/stats/steps/daily/2026-10-20/2026-10-24",
					"wellness-service/wellness/daily/spo2acclimation/2026-10-24",
				} {
					g.replies[p] = replyA{resp: garmin.Response{Status: body.status, Body: []byte(body.body)}}
				}
				res, err := runA(t, c.tool, g, c.args)
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError() || len(res.Errors) > 0 || res.Data != nil || !strings.HasPrefix(res.Note, "no data for") {
					t.Fatalf("not a NoData result: %+v", res)
				}
			})
		}
	}
}

func TestSeriesToolsDownsampleUnder8KBA(t *testing.T) {
	cases := []struct {
		tool, args, path, body string
	}{
		{"get_heart_rate", `{"date":"2026-10-24"}`, "wellness-service/wellness/dailyHeartRate",
			`{"calendarDate":"2026-10-24","restingHeartRate":48,"maxHeartRate":150,"heartRateValues":` + pairsA(1440) + `}`},
		{"get_stress", `{"start_date":"2026-10-24","end_date":"2026-10-24"}`, "usersummary-service/stats/stress/daily/2026-10-24/2026-10-24",
			`[{"calendarDate":"2026-10-24","values":{"overallStressLevel":31},"stressValuesArray":` + pairsA(960) + `}]`},
		{"get_body_battery", `{"start_date":"2026-10-24","end_date":"2026-10-24"}`, "wellness-service/wellness/bodyBattery/reports/daily",
			`[{"date":"2026-10-24","charged":60,"drained":55,"bodyBatteryValuesArray":` + strings.ReplaceAll(pairsA(900), ",4", ",\"MEASURED\",4") + `}]`},
		{"get_hrv", `{"date":"2026-10-24"}`, "hrv-service/hrv/2026-10-24",
			`{"hrvSummary":{"weeklyAvg":45,"lastNightAvg":47},"hrvReadings":` + readingsA(288, "hrvValue") + `}`},
		{"get_spo2", `{"date":"2026-10-24"}`, "wellness-service/wellness/daily/spo2acclimation/2026-10-24",
			`{"calendarDate":"2026-10-24","averageSpO2":95,"spO2SingleValues":` + readingsA(400, "spo2Reading") + `}`},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			if len(c.body) < 8192 {
				t.Fatalf("fixture too small (%d)", len(c.body))
			}
			g := &fakeGetterA{replies: map[string]replyA{c.path: okA(c.body)}}
			res, err := runA(t, c.tool, g, c.args)
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err=%v res=%+v", err, res)
			}
			if n := sizeA(t, res.Data); n > 8192 {
				t.Fatalf("default output %d bytes", n)
			}
			// Summary stats survive downsampling.
			if !strings.Contains(fmt.Sprint(mustJSONA(t, res.Data)), `"max"`) {
				t.Errorf("no stats kept: %s", mustJSONA(t, res.Data))
			}
		})
	}
}

func mustJSONA(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDownsampleKeepsStatsA(t *testing.T) {
	g := &fakeGetterA{replies: map[string]replyA{
		"wellness-service/wellness/dailyHeartRate": okA(`{"heartRateValues":` + pairsA(1440) + `}`),
	}}
	res, err := runA(t, "get_heart_rate", g, `{"date":"2026-10-24"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		HeartRateValues struct {
			Min, Max, Count int
			Mean            float64
			Points          [][2]float64
		} `json:"heartRateValues"`
	}
	if err := json.Unmarshal([]byte(mustJSONA(t, res.Data)), &out); err != nil {
		t.Fatal(err)
	}
	h := out.HeartRateValues
	if h.Count != 1440 || h.Min != 40 || h.Max != 99 || len(h.Points) >= 1440 || len(h.Points) < 2 {
		t.Fatalf("unexpected series %+v", h)
	}
}

func TestFullFlagReturnsAllPointsA(t *testing.T) {
	g := &fakeGetterA{replies: map[string]replyA{
		"wellness-service/wellness/dailyHeartRate": okA(`{"heartRateValues":` + pairsA(1440) + `}`),
		"hrv-service/hrv/2026-10-24":               okA(`{"hrvReadings":` + readingsA(288, "hrvValue") + `}`),
	}}
	res, err := runA(t, "get_heart_rate", g, `{"date":"2026-10-24","full":true}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Data.(map[string]any)
	if n := len(m["heartRateValues"].([]any)); n != 1440 {
		t.Fatalf("heart rate points = %d", n)
	}
	res, err = runA(t, "get_hrv", g, `{"date":"2026-10-24","full":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Data.(map[string]any)["hrvReadings"].([]any)); n != 288 {
		t.Fatalf("hrv readings = %d", n)
	}
	res, _ = runA(t, "get_heart_rate", g, `{"date":"2026-10-24","full":"yes"}`)
	if !res.IsError() {
		t.Fatal("non-boolean full accepted")
	}
}

func TestGetProfilePartialFailureA(t *testing.T) {
	g := &fakeGetterA{replies: map[string]replyA{
		"userprofile-service/userprofile/settings": okA(`{"measurementSystem":"metric"}`),
		"biometric-service/heartRateZones/v2":      {err: &garmin.StatusError{Status: 500}},
	}}
	res, err := runA(t, "get_profile", g, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Data == nil || res.IsError() {
		t.Fatalf("expected data: %+v", res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "heart rate zones") || !strings.Contains(res.Errors[0], "500") {
		t.Fatalf("errors = %v", res.Errors)
	}
	if !strings.Contains(mustJSONA(t, res.Data), "metric") {
		t.Fatalf("settings missing: %s", mustJSONA(t, res.Data))
	}
}

func TestGetProfileAllFailedAndRejectedA(t *testing.T) {
	g := &fakeGetterA{replies: map[string]replyA{
		"userprofile-service/userprofile/settings": {err: &garmin.StatusError{Status: 503}},
		"biometric-service/heartRateZones/v2":      {err: &garmin.StatusError{Status: 500}},
	}}
	res, err := runA(t, "get_profile", g, `{}`)
	if err != nil || !res.IsError() || len(res.Errors) != 2 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	g = &fakeGetterA{replies: map[string]replyA{
		"biometric-service/heartRateZones/v2": {err: garmin.ErrKeyRejected},
	}}
	if _, err := runA(t, "get_profile", g, `{}`); err != ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
	g = &fakeGetterA{replies: map[string]replyA{
		"userprofile-service/userprofile/settings": okA(`{}`),
		"biometric-service/heartRateZones/v2":      replyA{resp: garmin.Response{Status: 204}},
	}}
	res, err = runA(t, "get_profile", g, `{}`)
	if err != nil || res.Data != nil || !strings.HasPrefix(res.Note, "no data") {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

func TestErrorsNeverLeakKeyOrBodyA(t *testing.T) {
	g := &fakeGetterA{replies: map[string]replyA{
		"wellness-service/wellness/dailyHeartRate": {err: &garmin.StatusError{Status: 502}},
	}}
	res, err := runA(t, "get_heart_rate", g, `{"date":"2026-10-24"}`)
	if err != nil || !res.IsError() || res.Errors[0] != "Garmin returned HTTP 502" {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	g.replies["wellness-service/wellness/dailyHeartRate"] = replyA{err: garmin.ErrKeyRejected}
	if _, err := runA(t, "get_heart_rate", g, `{"date":"2026-10-24"}`); err != ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestRangeRowsFitBudgetA(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 365; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"calendarDate":"2025-%02d-%02d","totalSteps":%d,"totalKilocalories":2300,"restingHeartRate":50,"averageStressLevel":30,"junk":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, i/28+1, i%28+1, 5000+i)
	}
	sb.WriteString("]")
	g := &fakeGetterA{replies: map[string]replyA{"usersummary-service/v2/dailysummaries": okA(sb.String())}}
	res, err := runA(t, "get_daily_summary", g, `{"start_date":"2025-10-25","end_date":"2026-10-24"}`)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if n := sizeA(t, res.Data); n > 8192 {
		t.Fatalf("size %d", n)
	}
	if res.Note == "" {
		t.Error("expected a note explaining the thinning")
	}
	res, _ = runA(t, "get_daily_summary", g, `{"start_date":"2025-10-25","end_date":"2026-10-24","full":true}`)
	if n := len(res.Data.([]any)); n != 365 {
		t.Fatalf("full rows = %d", n)
	}
}
