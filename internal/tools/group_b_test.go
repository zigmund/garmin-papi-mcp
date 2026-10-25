package tools

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

type respB struct {
	status int
	body   string
	err    error
}

type callB struct {
	path  string
	query url.Values
}

// fakeGetterB answers by the longest route key contained in the request path.
type fakeGetterB struct {
	mu     sync.Mutex
	routes map[string]respB
	calls  []callB
}

func newFakeB(routes map[string]respB) *fakeGetterB { return &fakeGetterB{routes: routes} }

func (f *fakeGetterB) Get(_ context.Context, _ string, path string, q url.Values) (garmin.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, callB{path, q})
	best := ""
	for k := range f.routes {
		if strings.Contains(path, k) && len(k) > len(best) {
			best = k
		}
	}
	r, ok := f.routes[best]
	if !ok {
		return garmin.Response{}, &garmin.StatusError{Status: 404}
	}
	if r.err != nil {
		return garmin.Response{}, r.err
	}
	return garmin.Response{Status: r.status, Body: []byte(r.body)}, nil
}

func okB(body string) respB { return respB{status: 200, body: body} }

var noContentB = respB{status: 204}

func toolB(t *testing.T, name string) Tool {
	t.Helper()
	for _, tl := range GroupB() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return Tool{}
}

func runB(t *testing.T, name string, g Getter, args string) (Result, error) {
	t.Helper()
	return toolB(t, name).Handler(context.Background(), g, "key", json.RawMessage(args))
}

func TestGroupBToolNames(t *testing.T) {
	want := []string{"get_activity", "get_fitness_age", "get_hydration", "get_menstrual_cycle", "get_nutrition", "get_weight", "get_wellness_activities", "list_activities"}
	var got []string
	seen := map[string]bool{}
	for _, tl := range GroupB() {
		if seen[tl.Name] {
			t.Fatalf("duplicate %s", tl.Name)
		}
		seen[tl.Name] = true
		got = append(got, tl.Name)
		if tl.Description == "" || tl.Handler == nil {
			t.Errorf("%s: missing description or handler", tl.Name)
		}
		if tl.InputSchema["type"] != "object" || tl.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: bad schema", tl.Name)
		}
		if _, err := json.Marshal(tl.InputSchema); err != nil {
			t.Errorf("%s: schema not marshalable: %v", tl.Name, err)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGroupBMalformedArgsNeverCallGarmin(t *testing.T) {
	cases := map[string]string{
		"list_activities":         `{"limit":"x"}`,
		"get_activity":            `{}`,
		"get_weight":              `{"start_date":"2026-02-30","end_date":"2026-03-01"}`,
		"get_nutrition":           `{"date":"yesterday"}`,
		"get_hydration":           `{"start_date":"2026-01-01"}`,
		"get_fitness_age":         `{"date":"2026-01-01","bogus":1}`,
		"get_wellness_activities": `not json`,
		"get_menstrual_cycle":     `{"date":5}`,
	}
	for name, args := range cases {
		g := newFakeB(nil)
		res, err := runB(t, name, g, args)
		if err != nil || !res.IsError() {
			t.Errorf("%s: want error result, got %+v %v", name, res, err)
		}
		if len(g.calls) != 0 {
			t.Errorf("%s: Garmin was called", name)
		}
	}
}

func TestGroupBKeyRejected(t *testing.T) {
	rej := respB{err: garmin.ErrKeyRejected}
	for name, args := range map[string]string{
		"list_activities":     `{}`,
		"get_activity":        `{"activity_id":1}`,
		"get_weight":          `{"start_date":"2026-01-01","end_date":"2026-01-02"}`,
		"get_menstrual_cycle": `{"date":"2026-01-01"}`,
		"get_nutrition":       `{"date":"2026-01-01"}`,
	} {
		_, err := runB(t, name, newFakeB(map[string]respB{"": rej}), args)
		if err != ErrUnauthorized {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// second part rejected after first succeeded
	g := newFakeB(map[string]respB{"activity/7": okB(`{"a":1}`), "activity/7/workouts": rej})
	if _, err := runB(t, "get_activity", g, `{"activity_id":7}`); err != ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
}

func TestHydrationRangeLimit(t *testing.T) {
	g := newFakeB(map[string]respB{"hydration": okB(`[{"calendarDate":"2026-01-01","valueInML":1500}]`)})
	// 2026-01-01..2026-01-31 is 31 days
	res, err := runB(t, "get_hydration", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if err != nil || res.IsError() || res.Data == nil {
		t.Fatalf("31 days must pass: %+v %v", res, err)
	}
	if g.calls[0].path != "usersummary-service/stats/hydration/daily/2026-01-01/2026-01-31" {
		t.Fatalf("path %s", g.calls[0].path)
	}
	g = newFakeB(nil)
	res, _ = runB(t, "get_hydration", g, `{"start_date":"2026-01-01","end_date":"2026-02-01"}`)
	if !res.IsError() || len(g.calls) != 0 {
		t.Fatalf("32 days must be rejected without a call: %+v", res)
	}
}

func TestWellnessActivitiesPathAndLimit(t *testing.T) {
	g := newFakeB(map[string]respB{"summary/range": okB(`[{"x":1}]`)})
	res, err := runB(t, "get_wellness_activities", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if err != nil || res.Data == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if g.calls[0].path != "wellnessactivity-service/activity/summary/range/2026-01-01/2026-01-31" {
		t.Fatalf("path %s", g.calls[0].path)
	}
	res, _ = runB(t, "get_wellness_activities", newFakeB(nil), `{"start_date":"2026-01-01","end_date":"2026-02-01"}`)
	if !res.IsError() {
		t.Fatal("32 days must be rejected")
	}
}

func TestFitnessAgePathAndNoData(t *testing.T) {
	g := newFakeB(map[string]respB{"fitnessage": okB(`{"fitnessAge":31.5}`)})
	res, _ := runB(t, "get_fitness_age", g, `{"date":"2026-03-04"}`)
	if res.Data == nil || g.calls[0].path != "fitnessage-service/fitnessage/v2/2026-03-04" {
		t.Fatalf("%+v %v", res, g.calls)
	}
	res, err := runB(t, "get_fitness_age", newFakeB(map[string]respB{"fitnessage": noContentB}), `{"date":"2026-03-04"}`)
	if err != nil || res.IsError() || res.Data != nil || res.Note == "" {
		t.Fatalf("204 must be NoData: %+v %v", res, err)
	}
}

func TestGroupBGarminFailureIsErrorResult(t *testing.T) {
	g := newFakeB(map[string]respB{"fitnessage": {err: &garmin.StatusError{Status: 500}}})
	res, err := runB(t, "get_fitness_age", g, `{"date":"2026-03-04"}`)
	if err != nil || !res.IsError() || strings.Contains(strings.Join(res.Errors, " "), "key") {
		t.Fatalf("%+v %v", res, err)
	}
}
