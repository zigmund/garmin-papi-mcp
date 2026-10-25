package tools

import (
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
	if g.calls[0].path != "nutrition-service/food/logs/2026-02-01" || len(g.calls[0].query) != 0 {
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
	if q.Get("includeGoals") != "true" || q.Get("includeNutrition") != "false" {
		t.Fatalf("query %v", q)
	}
}
