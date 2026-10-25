package e2e

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	day      = "2026-10-24"
	rangeEnd = "2026-10-24"
	activity = "12345678901"
	// outputSlack: the spec budget is about 8 KB; 1000 bytes of slack for
	// the JSON envelope, notes and encoding.
	maxOutputBytes = 9000
)

// toolCase describes how one tool is exercised: a minimal valid call, the
// Garmin paths it needs, and (for range tools) the maximum range in days.
type toolCase struct {
	tool    string
	args    string
	paths   []string          // every Garmin path the minimal call uses
	big     map[string]string // large synthetic payloads by path (budget test)
	maxDays int               // 0 for tools without a date range limit
}

func rangeArgs(from, to string) string {
	return fmt.Sprintf(`{"start_date":%q,"end_date":%q}`, from, to)
}

func cases() []toolCase {
	hrDay := "/wellness-service/wellness/dailyHeartRate"
	spo2 := "/wellness-service/wellness/daily/spo2acclimation/" + day
	hrv := "/hrv-service/hrv/" + day
	bb := "/wellness-service/wellness/bodyBattery/reports/daily"
	stress := "/usersummary-service/stats/stress/daily/" + day + "/" + rangeEnd
	steps := "/usersummary-service/stats/steps/daily/" + day + "/" + rangeEnd
	sleep := "/sleep-service/sleep/dailySleep"
	daily := "/usersummary-service/usersummary/daily"
	weight := "/weight-service/weight/range/" + day + "/" + rangeEnd
	goal := "/goal-service/goal/user/effective/weightgoal/" + day + "/" + rangeEnd
	hydration := "/usersummary-service/stats/hydration/daily/" + day + "/" + rangeEnd
	wellness := "/wellnessactivity-service/activity/summary/range/" + day + "/" + rangeEnd
	list := "/activitylist-service/activities/search/activities"

	return []toolCase{
		{tool: "get_profile", args: `{}`,
			paths: []string{"/userprofile-service/userprofile/settings", "/biometric-service/heartRateZones/v2"}},
		{tool: "get_daily_summary", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{daily},
			big: map[string]string{daily: dailySummaryRows(1)}},
		{tool: "get_sleep", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{sleep}},
		{tool: "get_heart_rate", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{hrDay},
			big: map[string]string{hrDay: heartRateDay(760)}},
		{tool: "get_hrv", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{hrv},
			big: map[string]string{hrv: hrvDay(115)}},
		{tool: "get_body_battery", args: rangeArgs(day, rangeEnd), paths: []string{bb},
			big: map[string]string{bb: bodyBatteryDays(31, 60)}, maxDays: 31},
		{tool: "get_stress", args: rangeArgs(day, rangeEnd), paths: []string{stress},
			big: map[string]string{stress: stressDays(28, 120)}, maxDays: 28},
		{tool: "get_steps", args: rangeArgs(day, rangeEnd), paths: []string{steps},
			big: map[string]string{steps: stepsDays(28)}, maxDays: 28},
		{tool: "get_spo2", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{spo2},
			big: map[string]string{spo2: spo2Day(180)}},

		{tool: "list_activities", args: `{"limit":2}`, paths: []string{list},
			big: map[string]string{list: activityList(20)}},
		{tool: "get_activity", args: fmt.Sprintf(`{"activity_id":%s}`, activity),
			paths: []string{"/activity-service/activity/" + activity, "/activity-service/activity/" + activity + "/workouts"}},
		{tool: "get_weight", args: rangeArgs(day, rangeEnd), paths: []string{weight, goal},
			big: map[string]string{weight: weightDays(31)}, maxDays: 31},
		{tool: "get_nutrition", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{"/nutrition-service/food/logs/" + day}},
		{tool: "get_hydration", args: rangeArgs(day, rangeEnd), paths: []string{hydration},
			big: map[string]string{hydration: hydrationDays(31)}, maxDays: 31},
		{tool: "get_fitness_age", args: fmt.Sprintf(`{"date":%q}`, day), paths: []string{"/fitnessage-service/fitnessage/v2/" + day}},
		{tool: "get_wellness_activities", args: rangeArgs(day, rangeEnd), paths: []string{wellness},
			big: map[string]string{wellness: wellnessDays(31)}, maxDays: 31},
		{tool: "get_menstrual_cycle", args: fmt.Sprintf(`{"date":%q}`, day),
			paths: []string{"/periodichealth-service/menstrualcycle/summary/" + day, "/periodichealth-service/menstrualcycle/dayview/" + day}},
	}
}

func smallBody(path string) string {
	if strings.HasSuffix(path, "/activities/search/activities") {
		return `[{"activityId":` + activity + `,"activityName":"Synthetic run","startTimeLocal":"2026-10-24 07:00:00","distance":5000.5,"duration":1500.2}]`
	}
	return `{"synthetic":true,"value":42}`
}

func TestAllToolsListedAndCallable(t *testing.T) {
	cs := cases()
	bodies := map[string]string{}
	var want []string
	for _, c := range cs {
		for _, p := range c.paths {
			bodies[p] = smallBody(p)
			want = append(want, p)
		}
	}
	// The range variants of the tools that also take a single date.
	rangeCalls := []toolCase{
		{tool: "get_daily_summary", args: rangeArgs(day, rangeEnd), paths: []string{"/usersummary-service/v2/dailysummaries"}},
		{tool: "get_sleep", args: rangeArgs(day, rangeEnd), paths: []string{"/sleep-service/sleep/dailySleepSummariesByDate"}},
		{tool: "get_hrv", args: rangeArgs(day, rangeEnd), paths: []string{"/hrv-service/hrv/daily/" + day + "/" + rangeEnd}},
	}
	for _, c := range rangeCalls {
		bodies[c.paths[0]] = smallBody(c.paths[0])
		want = append(want, c.paths[0])
	}
	g := newFakeGarmin(t, bodies)
	s := newStack(t, g.srv.URL, fakeAPIKey, true)

	listed := s.listTools(t)
	if len(listed) != 17 {
		t.Fatalf("tools/list returned %d tools, want 17", len(listed))
	}
	names := map[string]bool{}
	for _, d := range listed {
		names[d.Name] = true
		if !d.Annotations.ReadOnlyHint {
			t.Errorf("tool %s: readOnlyHint is not true", d.Name)
		}
	}
	for _, c := range cs {
		if !names[c.tool] {
			t.Errorf("tool %s is not listed", c.tool)
		}
	}
	if len(cs) != 17 {
		t.Fatalf("test table has %d tools, want 17", len(cs))
	}

	for _, c := range append(append([]toolCase{}, cs...), rangeCalls...) {
		t.Run(c.tool+"/"+c.paths[0], func(t *testing.T) {
			out := s.call(t, c.tool, c.args)
			if out.protoErr {
				t.Fatalf("protocol error (status %d)", out.status)
			}
			if out.isError {
				t.Fatalf("tool error: %s", firstLines(out.text, "error: "))
			}
			if strings.TrimSpace(out.text) == "" {
				t.Fatal("empty result")
			}
		})
	}
	// Every served path was requested at least once: no path in the table is
	// dead, and every tool path is one the fake knows (unknown paths 404 and
	// would have failed above).
	sort.Strings(want)
	for _, p := range want {
		if g.hitCount(p) == 0 {
			t.Errorf("path %s was never requested", p)
		}
	}
}

// firstLines returns only the lines of text starting with prefix (the
// generic "error: ..." lines of a tool result).
func firstLines(text, prefix string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "; ")
}

func TestEveryToolOutputUnder8KBByDefault(t *testing.T) {
	cs := cases()
	bodies := map[string]string{}
	for _, c := range cs {
		for _, p := range c.paths {
			bodies[p] = smallBody(p)
		}
		for p, b := range c.big {
			bodies[p] = b
		}
	}
	// Range variants of the tools that also take a single date.
	const (
		dailyRange = "/usersummary-service/v2/dailysummaries"
		sleepRange = "/sleep-service/sleep/dailySleepSummariesByDate"
		sleepDay   = "/sleep-service/sleep/dailySleep"
	)
	hrvRange := "/hrv-service/hrv/daily/" + day + "/" + rangeEnd
	bodies[dailyRange] = dailySummaryRows(365)
	bodies[sleepRange] = sleepNights(31)
	bodies[sleepDay] = sleepNights(1)
	bodies[hrvRange] = hrvDay(115)

	for p, b := range bodies {
		if !json.Valid([]byte(b)) {
			t.Fatalf("fixture for %s is not valid JSON", p)
		}
	}
	// The fixtures must be as large as the real responses they imitate.
	minSize := map[string]int{
		"/wellness-service/wellness/dailyHeartRate":               14000,
		"/wellness-service/wellness/daily/spo2acclimation/" + day: 8000,
		"/hrv-service/hrv/" + day:                                 9000,
		"/wellness-service/wellness/bodyBattery/reports/daily":    55000,
		sleepRange: 38000,
		"/activitylist-service/activities/search/activities": 55000,
	}
	for p, n := range minSize {
		if len(bodies[p]) < n {
			t.Fatalf("fixture for %s is %d bytes, want at least %d", p, len(bodies[p]), n)
		}
	}
	if n := len(bodies[dailyRange]); n < 50000 {
		t.Fatalf("daily summaries fixture is only %d bytes", n)
	}

	g := newFakeGarmin(t, bodies)
	s := newStack(t, g.srv.URL, fakeAPIKey, true)

	type run struct{ name, tool, args string }
	runs := []run{}
	for _, c := range cs {
		runs = append(runs, run{c.tool, c.tool, c.args})
	}
	runs = append(runs,
		run{"get_daily_summary range 365", "get_daily_summary", rangeArgs("2026-01-01", "2026-12-31")},
		run{"get_sleep range 31", "get_sleep", rangeArgs("2026-10-01", "2026-10-31")},
		run{"get_hrv range", "get_hrv", rangeArgs(day, rangeEnd)},
	)
	for _, r := range runs {
		t.Run(r.name, func(t *testing.T) {
			out := s.call(t, r.tool, r.args)
			if out.protoErr || out.isError {
				t.Fatalf("protocol error %v, tool error %v (status %d): %s", out.protoErr, out.isError, out.status, firstLines(out.text, "error: "))
			}
			t.Logf("%s: %d bytes", r.name, len(out.text))
			if len(out.text) > maxOutputBytes {
				t.Errorf("%s: default output is %d bytes, over %d", r.name, len(out.text), maxOutputBytes)
			}
		})
	}
}

func TestRangeLimitsRejectedWithoutGarminCall(t *testing.T) {
	cs := cases()
	bodies := map[string]string{}
	for _, c := range cs {
		for _, p := range c.paths {
			bodies[p] = smallBody(p)
		}
	}
	bodies["/usersummary-service/v2/dailysummaries"] = smallBody("")
	bodies["/sleep-service/sleep/dailySleepSummariesByDate"] = smallBody("")
	g := newFakeGarmin(t, bodies)
	s := newStack(t, g.srv.URL, fakeAPIKey, true)

	type limited struct {
		tool string
		days int
	}
	all := []limited{{"get_daily_summary", 365}, {"get_sleep", 31}, {"get_hrv", 28}}
	for _, c := range cs {
		if c.maxDays > 0 {
			all = append(all, limited{c.tool, c.maxDays})
		}
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, l := range all {
		t.Run(l.tool, func(t *testing.T) {
			// One day over the limit (the range is inclusive).
			end := start.AddDate(0, 0, l.days).Format("2006-01-02")
			before := g.totalHits()
			out := s.call(t, l.tool, rangeArgs("2026-01-01", end))
			if out.protoErr {
				t.Fatalf("protocol error (status %d)", out.status)
			}
			if !out.isError {
				t.Fatalf("a %d-day range (limit %d) was accepted", l.days+1, l.days)
			}
			if !strings.Contains(out.text, "limit") {
				t.Errorf("error does not mention the limit: %s", firstLines(out.text, "error: "))
			}
			if after := g.totalHits(); after != before {
				t.Fatalf("Garmin saw %d data request(s) for a rejected range", after-before)
			}
			// The exact limit is accepted (and does reach Garmin): the
			// boundary is not off by one in the other direction.
			ok := start.AddDate(0, 0, l.days-1).Format("2006-01-02")
			out = s.call(t, l.tool, rangeArgs("2026-01-01", ok))
			if out.protoErr || (out.isError && strings.Contains(out.text, "limit")) {
				t.Fatalf("a %d-day range was rejected: %s", l.days, firstLines(out.text, "error: "))
			}
		})
	}
}
