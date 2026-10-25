package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

func TestGetWeightQueryAndPaths(t *testing.T) {
	routes := map[string]respB{"weight/range": okB(`{"dateWeightList":[{"weight":70000}]}`), "weightgoal": okB(`[{"goal":1}]`)}
	g := newFakeB(routes)
	res, err := runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if err != nil || res.IsError() || len(res.Errors) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if g.calls[0].path != "weight-service/weight/range/2026-01-01/2026-01-31" || g.calls[0].query.Get("includeAll") != "false" {
		t.Fatalf("call0 %+v", g.calls[0])
	}
	if g.calls[1].path != "goal-service/goal/user/effective/weightgoal/2026-01-01/2026-01-31" {
		t.Fatalf("call1 %+v", g.calls[1])
	}
	g = newFakeB(routes)
	runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31","include_all":true}`)
	if g.calls[0].query.Get("includeAll") != "true" {
		t.Fatalf("include_all: %v", g.calls[0].query)
	}
	for _, c := range g.calls {
		if strings.Contains(c.path, "weight/all") {
			t.Fatal("weight/all must not be used")
		}
	}
}

func TestGetWeightPartialFailure(t *testing.T) {
	g := newFakeB(map[string]respB{"weight/range": okB(`{"a":1}`), "weightgoal": {err: &garmin.StatusError{Status: 500}}})
	res, err := runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if err != nil || res.IsError() || res.Data == nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "goal") {
		t.Fatalf("%+v %v", res, err)
	}
	g = newFakeB(map[string]respB{"weight/range": {err: &garmin.StatusError{Status: 500}}, "weightgoal": okB(`[{"g":1}]`)})
	res, _ = runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if res.Data == nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "weight") {
		t.Fatalf("%+v", res)
	}
	g = newFakeB(map[string]respB{"": {err: &garmin.StatusError{Status: 500}}})
	if res, _ = runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`); !res.IsError() {
		t.Fatalf("all failed must be error: %+v", res)
	}
	g = newFakeB(map[string]respB{"weight/range": okB(`{"dateWeightList":[]}`), "weightgoal": noContentB})
	if res, _ = runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`); res.IsError() {
		t.Fatalf("%+v", res)
	}
}

func TestMenstrualBothEmptyIsNoData(t *testing.T) {
	g := newFakeB(map[string]respB{"menstrualcycle": noContentB})
	res, err := runB(t, "get_menstrual_cycle", g, `{"date":"2026-02-01"}`)
	if err != nil || res.IsError() || res.Data != nil || res.Note == "" || len(res.Errors) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if dq := g.calls[1].query; dq.Get("reducedResponse") != "true" || dq.Get("skipbm") != "true" {
		t.Fatalf("dayview query %v", dq)
	}
	if len(g.calls) != 2 || g.calls[0].path != "periodichealth-service/menstrualcycle/summary/2026-02-01" || g.calls[1].path != "periodichealth-service/menstrualcycle/dayview/2026-02-01" {
		t.Fatalf("calls %v", g.calls)
	}
}

func TestMenstrualPartial(t *testing.T) {
	g := newFakeB(map[string]respB{"summary": okB(`{"cycleLength":28}`), "dayview": {err: &garmin.StatusError{Status: 500}}})
	res, err := runB(t, "get_menstrual_cycle", g, `{"date":"2026-02-01"}`)
	if err != nil || res.IsError() || res.Data == nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "dayview") {
		t.Fatalf("%+v %v", res, err)
	}
	g = newFakeB(map[string]respB{"summary": okB(`{"cycleLength":28}`), "dayview": noContentB})
	res, _ = runB(t, "get_menstrual_cycle", g, `{"date":"2026-02-01"}`)
	if res.IsError() || res.Data == nil || len(res.Errors) != 0 {
		t.Fatalf("%+v", res)
	}
	g = newFakeB(map[string]respB{"summary": {err: &garmin.StatusError{Status: 500}}, "dayview": noContentB})
	if res, _ = runB(t, "get_menstrual_cycle", g, `{"date":"2026-02-01"}`); !res.IsError() {
		t.Fatalf("failed + empty must be error: %+v", res)
	}
}

func TestNutritionEmptyDayIsNoData(t *testing.T) {
	g := newFakeB(map[string]respB{"food/logs": okB(`{"mealDetails":[],"loggedFoodsWithServingSizes":[]}`)})
	res, err := runB(t, "get_nutrition", g, `{"date":"2026-02-01"}`)
	if err != nil || res.IsError() || res.Data != nil || res.Note == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if g.calls[0].path != "nutrition-service/food/logs/2026-02-01" || g.calls[0].query.Get("includeMedia") != "false" || len(g.calls[0].query) != 1 {
		t.Fatalf("call %+v", g.calls[0])
	}
	g = newFakeB(map[string]respB{"food/logs": noContentB})
	if res, _ = runB(t, "get_nutrition", g, `{"date":"2026-02-01"}`); res.IsError() || res.Data != nil || res.Note == "" {
		t.Fatalf("204: %+v", res)
	}
}

func TestNutritionWithFoodAndFlags(t *testing.T) {
	g := newFakeB(map[string]respB{"food/logs": okB(`{"mealDetails":[{"mealName":"Lunch"}],"loggedFoodsWithServingSizes":[]}`)})
	res, err := runB(t, "get_nutrition", g, `{"date":"2026-02-01","include_goals":true,"include_nutrition":false}`)
	if err != nil || res.Data == nil {
		t.Fatalf("%+v %v", res, err)
	}
	q := g.calls[0].query
	if q.Get("includeGoals") != "true" || q.Get("includeNutrition") != "false" || q.Get("includeMedia") != "false" {
		t.Fatalf("query %v", q)
	}
}

func TestGetWeightRangeCap31Days(t *testing.T) {
	g := newFakeB(map[string]respB{"weight/range": okB(`{"a":1}`), "weightgoal": noContentB})
	if res, _ := runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`); res.IsError() {
		t.Fatalf("31 days must pass: %+v", res)
	}
	g = newFakeB(nil)
	if res, _ := runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-02-01"}`); !res.IsError() || len(g.calls) != 0 {
		t.Fatalf("32 days must be rejected: %+v", res)
	}
}

func TestGetWeightBudget31Days(t *testing.T) {
	var days []any
	for i := 0; i < 31; i++ {
		metric := map[string]any{"samplePk": 1000 + i, "date": 1700000000000 + i, "calendarDate": fmt.Sprintf("2026-01-%02d", i+1),
			"weight": 70123.4, "bmi": 22.4, "bodyFat": 18.2, "bodyWater": 55.1, "boneMass": 3100, "muscleMass": 31000,
			"physiqueRating": nil, "visceralFat": nil, "metabolicAge": nil, "sourceType": "INDEX_SCALE",
			"timestampGMT": 1700000000000 + i, "weightDelta": 100.5}
		days = append(days, map[string]any{
			"summaryDate": fmt.Sprintf("2026-01-%02d", i+1), "numOfWeightEntries": 2,
			"minWeight": 70000, "maxWeight": 70300, "avgWeight": 70123.4,
			"latestWeight":     metric,
			"allWeightMetrics": []any{metric, metric},
		})
	}
	payload, _ := json.Marshal(map[string]any{
		"dailyWeightSummaries": days,
		"totalAverage":         map[string]any{"from": 1, "until": 2, "weight": 70123.4, "bmi": 22.4},
		"previousDateWeight":   map[string]any{"calendarDate": "2025-12-31", "weight": 70000},
		"nextDateWeight":       nil,
	})
	g := newFakeB(map[string]respB{"weight/range": okB(string(payload)), "weightgoal": okB(`[{"weightGoal":68000,"startDate":"2026-01-01"}]`)})
	res, err := runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if err != nil || res.IsError() {
		t.Fatalf("%+v %v", res, err)
	}
	out, _ := json.Marshal(res.Data)
	if len(out) > 8*1024 {
		t.Fatalf("output %d bytes exceeds 8 KB", len(out))
	}
	s := string(out)
	for _, bad := range []string{"previousDateWeight", "allWeightMetrics", "samplePk", "sourceType", "weightDelta"} {
		if strings.Contains(s, bad) {
			t.Errorf("untrimmed %s", bad)
		}
	}
	if !strings.Contains(s, `"bodyFat":18.2`) || !strings.Contains(s, "latestWeight") || !strings.Contains(s, "weightGoal") {
		t.Errorf("trimmed too much: %s", s[:300])
	}
	// include_all keeps the raw reply
	g = newFakeB(map[string]respB{"weight/range": okB(string(payload)), "weightgoal": noContentB})
	res, _ = runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31","include_all":true}`)
	if out, _ := json.Marshal(res.Data); !strings.Contains(string(out), "allWeightMetrics") {
		t.Error("include_all must keep all measurements")
	}
}

func weightPayloadB(entries []any) string {
	b, _ := json.Marshal(map[string]any{"dailyWeightSummaries": entries, "previousDateWeight": map[string]any{"weight": 1}})
	return string(b)
}

func runWeightB(t *testing.T, payload string) Result {
	t.Helper()
	g := newFakeB(map[string]respB{"weight/range": okB(payload), "weightgoal": noContentB})
	res, err := runB(t, "get_weight", g, `{"start_date":"2026-01-01","end_date":"2026-01-31"}`)
	if err != nil || res.IsError() {
		t.Fatalf("%+v %v", res, err)
	}
	return res
}

func TestGetWeightUnknownLayoutsKeptWhole(t *testing.T) {
	layouts := map[string]map[string]any{
		"measurements": {"date": 1, "measurements": []any{map[string]any{"valueKg": 70}}},
		"latest":       {"summaryDate": "2026-01-01", "latest": map[string]any{"valueKg": 70}},
		"mixed":        {"a": []any{1, 2}, "b": map[string]any{"x": 1}},
	}
	for name, e := range layouts {
		res := runWeightB(t, weightPayloadB([]any{e}))
		want, _ := json.Marshal([]any{e})
		got, _ := json.Marshal(res.Data.(map[string]any)["weight"].(map[string]any)["dailyWeightSummaries"])
		if string(got) != string(want) {
			t.Errorf("%s: entry changed: %s", name, got)
		}
		if !strings.Contains(res.Note, "not recognised") {
			t.Errorf("%s: missing note: %q", name, res.Note)
		}
	}
}

func TestGetWeightKnownLayoutTrimmedWithoutNote(t *testing.T) {
	e := map[string]any{"summaryDate": "2026-01-01", "junk": 5, "latestWeight": map[string]any{"weight": 70, "sourceType": "x", "timestampGMT": 7}, "allWeightMetrics": []any{1}}
	res := runWeightB(t, weightPayloadB([]any{e}))
	got, _ := json.Marshal(res.Data.(map[string]any)["weight"].(map[string]any)["dailyWeightSummaries"])
	if string(got) != `[{"latestWeight":{"timestampGMT":7,"weight":70},"summaryDate":"2026-01-01"}]` {
		t.Fatalf("got %s", got)
	}
	if strings.Contains(res.Note, "not recognised") {
		t.Fatalf("note %q", res.Note)
	}
}

func TestGetWeightOversizeUnknownLayoutThinned(t *testing.T) {
	var entries []any
	for i := 0; i < 31; i++ {
		e := map[string]any{"id": i}
		for j := 0; j < 30; j++ {
			e[fmt.Sprintf("field%02d", j)] = "synthetic value"
		}
		entries = append(entries, e)
	}
	res := runWeightB(t, weightPayloadB(entries))
	out, _ := json.Marshal(res.Data)
	if len(out) > 8*1024 {
		t.Fatalf("output %d bytes", len(out))
	}
	l := res.Data.(map[string]any)["weight"].(map[string]any)["dailyWeightSummaries"].([]any)
	if len(l) >= 31 || len(l) < 2 {
		t.Fatalf("kept %d", len(l))
	}
	if l[0].(map[string]any)["id"] != float64(0) || l[len(l)-1].(map[string]any)["id"] != float64(30) {
		t.Fatalf("first and last must be kept")
	}
	if !strings.Contains(res.Note, "thinned") || !strings.Contains(res.Note, "of 31") {
		t.Fatalf("note %q", res.Note)
	}
}
