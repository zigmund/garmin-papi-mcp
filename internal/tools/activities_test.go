package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/zigmund/garmin-papi-mcp/internal/garmin"
)

func TestListActivitiesDefaultsAndCaps(t *testing.T) {
	g := newFakeB(map[string]respB{"activities/search": okB(`[{"activityId":1}]`)})
	if res, err := runB(t, "list_activities", g, `{}`); err != nil || res.IsError() {
		t.Fatalf("%+v %v", res, err)
	}
	q := g.calls[0].query
	if q.Get("limit") != "20" || q.Get("start") != "0" {
		t.Fatalf("defaults: %v", q)
	}
	g = newFakeB(map[string]respB{"activities/search": okB(`[{"activityId":1}]`)})
	res, err := runB(t, "list_activities", g, `{"limit":1000,"start":40}`)
	if err != nil || res.IsError() {
		t.Fatalf("cap must not be an error: %+v %v", res, err)
	}
	if q := g.calls[0].query; q.Get("limit") != "100" || q.Get("start") != "40" {
		t.Fatalf("cap: %v", q)
	}
	if res, _ := runB(t, "list_activities", newFakeB(nil), `{"limit":0}`); !res.IsError() {
		t.Fatal("limit 0 must be rejected")
	}
	if res, _ := runB(t, "list_activities", newFakeB(nil), `{"start":-1}`); !res.IsError() {
		t.Fatal("negative start must be rejected")
	}
}

func TestListActivitiesQueryMapping(t *testing.T) {
	g := newFakeB(map[string]respB{"activities/search": okB(`[{"activityId":1}]`)})
	args := `{"start":5,"limit":10,"favorite":true,"sort_order":"ASC","exclude_children":false,"sort_by":"startLocal","search":"morning","activity_type":"running","activity_sub_type":"trail","event_type":"race","min_distance":1000.5,"max_distance":20000,"min_duration":60,"max_duration":7200,"start_date":"2026-01-01","end_date":"2026-01-31",` +
		`"min_elevation":1,"max_elevation":2,"min_latitude":3,"max_latitude":4,"min_longitude":5,"max_longitude":6,"min_depth":7,"max_depth":8,"min_deco_dive":9,"max_deco_dive":10,"min_elapsed_duration":11,"max_elapsed_duration":12,"min_bottom_time":13,"max_bottom_time":14,"min_avg_power":15,"max_avg_power":16,"excluded_activity_sub_type":"x","required_field":"averageHR"}`
	if res, err := runB(t, "list_activities", g, args); err != nil || res.IsError() {
		t.Fatalf("%+v %v", res, err)
	}
	if g.calls[0].path != "activitylist-service/activities/search/activities" {
		t.Fatalf("path %s", g.calls[0].path)
	}
	want := url.Values{
		"start": {"5"}, "limit": {"10"}, "favorite": {"true"}, "sortOrder": {"ASC"}, "excludeChildren": {"false"},
		"sortBy": {"startLocal"}, "search": {"morning"}, "activityType": {"running"}, "activitySubType": {"trail"},
		"eventType": {"race"}, "minDistance": {"1000.5"}, "maxDistance": {"20000"}, "minDuration": {"60"},
		"maxDuration": {"7200"}, "startDate": {"2026-01-01"}, "endDate": {"2026-01-31"},
		"minElevation": {"1"}, "maxElevation": {"2"}, "minLatitude": {"3"}, "maxLatitude": {"4"},
		"minLongitude": {"5"}, "maxLongitude": {"6"}, "minDepth": {"7"}, "maxDepth": {"8"},
		"minDecoDive": {"9"}, "maxDecoDive": {"10"}, "minElapsedDuration": {"11"}, "maxElapsedDuration": {"12"},
		"minBottomTime": {"13"}, "maxBottomTime": {"14"}, "minAvgPower": {"15"}, "maxAvgPower": {"16"},
		"excludedActivitySubType": {"x"}, "requiredField": {"averageHR"},
	}
	got := g.calls[0].query
	for k, v := range want {
		if got.Get(k) != v[0] {
			t.Errorf("query %s = %q, want %q", k, got.Get(k), v[0])
		}
	}
	if len(got) != len(want) {
		t.Errorf("extra query params: %v", got)
	}
	// every schema property is covered by the mapping test
	props := toolB(t, "list_activities").InputSchema["properties"].(map[string]any)
	if len(props) != len(want) {
		t.Errorf("schema has %d properties, mapping test covers %d", len(props), len(want))
	}
}

func TestListActivitiesBadArgs(t *testing.T) {
	for _, a := range []string{`{"favorite":"yes"}`, `{"min_distance":"5"}`, `{"start_date":"2026-13-01"}`, `{"start":1.5}`, `{"nope":1}`, `{"search":""}`} {
		g := newFakeB(nil)
		if res, _ := runB(t, "list_activities", g, a); !res.IsError() || len(g.calls) != 0 {
			t.Errorf("%s: want error without call, got %+v", a, res)
		}
	}
}

func TestListActivitiesTrimmedUnder8KB(t *testing.T) {
	var items []map[string]any
	for i := 0; i < 20; i++ {
		items = append(items, map[string]any{
			"activityId": 90000000000 + i, "activityName": fmt.Sprintf("Synthetic morning run %d", i),
			"startTimeLocal": "2026-03-04 07:15:00", "startTimeGMT": "2026-03-04 02:15:00",
			"activityType": map[string]any{"typeId": 1, "typeKey": "running", "parentTypeId": 17, "isHidden": false},
			"eventType":    map[string]any{"typeId": 9, "typeKey": "uncategorized"},
			"distance":     10234.5678912, "duration": 3123.4567, "elapsedDuration": 3200.1234, "movingDuration": 3100.9876,
			"elevationGain": 123.456, "elevationLoss": 120.1, "averageSpeed": 3.2765432, "maxSpeed": 5.123456,
			"averageHR": 150.0, "maxHR": 178.0, "calories": 650.0, "steps": 9000, "avgPower": 250.0,
			"aerobicTrainingEffect": 3.4, "locationName": "Synthetic City", "deviceId": 1234567890, "ownerId": 42,
			"splitSummaries": make([]any, 30), "summarizedDiveInfo": map[string]any{"a": 1},
		})
	}
	body, _ := json.Marshal(items)
	g := newFakeB(map[string]respB{"activities/search": okB(string(body))})
	res, err := runB(t, "list_activities", g, `{}`)
	if err != nil || res.IsError() {
		t.Fatalf("%+v %v", res, err)
	}
	out, _ := json.Marshal(res.Data)
	if len(out) > 8*1024 {
		t.Fatalf("output %d bytes exceeds 8 KB", len(out))
	}
	s := string(out)
	for _, bad := range []string{"deviceId", "ownerId", "splitSummaries", "startTimeGMT", "elevationLoss", "typeId"} {
		if strings.Contains(s, bad) {
			t.Errorf("untrimmed field %s", bad)
		}
	}
	for _, good := range []string{`"activityId":90000000000`, `"activityType":"running"`, `"averageHR":150`, `"count":20`} {
		if !strings.Contains(s, good) {
			t.Errorf("missing %s in %s", good, s[:300])
		}
	}
}

func TestListActivitiesEmptyIsNoData(t *testing.T) {
	for _, r := range []respB{noContentB, okB(`[]`)} {
		res, err := runB(t, "list_activities", newFakeB(map[string]respB{"activities/search": r}), `{}`)
		if err != nil || res.IsError() || res.Data != nil || res.Note == "" {
			t.Fatalf("%+v %v", res, err)
		}
	}
}

func TestGetActivityRejectsNonInteger(t *testing.T) {
	for _, a := range []string{`{"activity_id":"123"}`, `{"activity_id":1.5}`, `{"activity_id":0}`, `{"activity_id":-3}`, `{"activity_id":null}`, `{"activity_id":true}`, `{"activity_id":"1/../2"}`, `{}`} {
		g := newFakeB(nil)
		res, err := runB(t, "get_activity", g, a)
		if err != nil || !res.IsError() || len(g.calls) != 0 {
			t.Errorf("%s: want error result without call, got %+v %v", a, res, err)
		}
	}
	// integer-valued number is accepted
	g := newFakeB(map[string]respB{"activity/123": okB(`{"a":1}`), "activity/123/workouts": okB(`[{"w":1}]`)})
	res, err := runB(t, "get_activity", g, `{"activity_id":123.0}`)
	if err != nil || res.IsError() {
		t.Fatalf("%+v %v", res, err)
	}
	if g.calls[0].path != "activity-service/activity/123" || g.calls[1].path != "activity-service/activity/123/workouts" {
		t.Fatalf("paths %v", g.calls)
	}
}

func TestGetActivityPartialFailure(t *testing.T) {
	g := newFakeB(map[string]respB{"activity/5": okB(`{"activityId":5}`), "activity/5/workouts": {err: &garmin.StatusError{Status: 500}}})
	res, err := runB(t, "get_activity", g, `{"activity_id":5}`)
	if err != nil || res.IsError() || res.Data == nil {
		t.Fatalf("want data: %+v %v", res, err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "workouts") {
		t.Fatalf("errors %v", res.Errors)
	}
	if _, ok := res.Data.(map[string]any)["activity"]; !ok {
		t.Fatalf("data %v", res.Data)
	}
	// all failed
	g = newFakeB(map[string]respB{"activity/5": {err: &garmin.StatusError{Status: 500}}})
	res, _ = runB(t, "get_activity", g, `{"activity_id":5}`)
	if !res.IsError() || len(res.Errors) != 2 {
		t.Fatalf("%+v", res)
	}
	// both empty
	g = newFakeB(map[string]respB{"activity/5": noContentB})
	res, _ = runB(t, "get_activity", g, `{"activity_id":5}`)
	if res.IsError() || res.Data != nil || res.Note == "" {
		t.Fatalf("%+v", res)
	}
}
