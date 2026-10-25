package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
)

const (
	defaultActivityLimitB = 20
	maxActivityLimitB     = 100
)

// activityParamB maps a tool argument to a Garmin query parameter.
type activityParamB struct {
	arg, query, kind, desc string
}

// activityParamsB lists every filter of the list_activities tool in schema
// order. Garmin query names follow the spec; the pass-through block at the end
// holds the less common filters (elevation, position box, dive, power, ...).
var activityParamsB = []activityParamB{
	{"start", "start", "integer", "Offset of the first activity to return (default 0)."},
	{"limit", "limit", "integer", "Number of activities to return (default 20, maximum 100; larger values are capped)."},
	{"favorite", "favorite", "boolean", "Only favorite activities."},
	{"sort_order", "sortOrder", "string", "Sort direction, for example ASC or DESC."},
	{"exclude_children", "excludeChildren", "boolean", "Exclude child activities of multisport activities."},
	{"sort_by", "sortBy", "string", "Field to sort by."},
	{"search", "search", "string", "Free-text search over activity names."},
	{"activity_type", "activityType", "string", "Activity type key, for example running."},
	{"activity_sub_type", "activitySubType", "string", "Activity sub type key."},
	{"event_type", "eventType", "string", "Event type key, for example race."},
	{"min_distance", "minDistance", "number", "Minimum distance in meters."},
	{"max_distance", "maxDistance", "number", "Maximum distance in meters."},
	{"min_duration", "minDuration", "number", "Minimum duration in seconds."},
	{"max_duration", "maxDuration", "number", "Maximum duration in seconds."},
	{"start_date", "startDate", "string", "Only activities on or after this day, YYYY-MM-DD."},
	{"end_date", "endDate", "string", "Only activities on or before this day, YYYY-MM-DD."},
	{"min_elevation", "minElevation", "number", "Minimum elevation gain."},
	{"max_elevation", "maxElevation", "number", "Maximum elevation gain."},
	{"min_latitude", "minLatitude", "number", "Southern edge of the position box."},
	{"max_latitude", "maxLatitude", "number", "Northern edge of the position box."},
	{"min_longitude", "minLongitude", "number", "Western edge of the position box."},
	{"max_longitude", "maxLongitude", "number", "Eastern edge of the position box."},
	{"min_depth", "minDepth", "number", "Minimum dive depth."},
	{"max_depth", "maxDepth", "number", "Maximum dive depth."},
	{"min_deco_dive", "minDecoDive", "number", "Minimum deco dive value."},
	{"max_deco_dive", "maxDecoDive", "number", "Maximum deco dive value."},
	{"min_elapsed_duration", "minElapsedDuration", "number", "Minimum elapsed duration in seconds."},
	{"max_elapsed_duration", "maxElapsedDuration", "number", "Maximum elapsed duration in seconds."},
	{"min_bottom_time", "minBottomTime", "number", "Minimum dive bottom time."},
	{"max_bottom_time", "maxBottomTime", "number", "Maximum dive bottom time."},
	{"min_avg_power", "minAvgPower", "number", "Minimum average power."},
	{"max_avg_power", "maxAvgPower", "number", "Maximum average power."},
	{"excluded_activity_sub_type", "excludedActivitySubType", "string", "Activity sub type key to exclude."},
	{"required_field", "requiredField", "string", "Only activities that have this field."},
}

// activityFieldsB is the fixed set of fields kept per activity summary, so 20
// activities stay under about 8 KB. Fields: activityId, activityName,
// startTimeLocal, activityType (the typeKey), distance, duration,
// elapsedDuration, movingDuration, elevationGain, averageSpeed,
// averageHR, maxHR, calories, avgPower, aerobicTrainingEffect,
// locationName. Absent fields are omitted. Use get_activity for the full record.
var activityFieldsB = []string{
	"activityId", "activityName", "startTimeLocal", "activityType",
	"distance", "duration", "elapsedDuration", "movingDuration", "elevationGain",
	"averageSpeed", "averageHR", "maxHR", "calories",
	"avgPower", "aerobicTrainingEffect", "locationName",
}

func listActivitiesTool() Tool {
	props := map[string]any{}
	for _, p := range activityParamsB {
		props[p.arg] = map[string]any{"type": p.kind, "description": p.desc}
	}
	props["limit"].(map[string]any)["minimum"] = 1
	props["start"].(map[string]any)["minimum"] = 0
	return Tool{
		Name: "list_activities",
		Description: "List or search recorded activities (runs, rides, swims, workouts) with a trimmed summary of each: id, name, start time, type, distance, duration, heart rate, calories. " +
			"Returns 20 activities by default, at most 100 per call; page with start. Dates are YYYY-MM-DD. Use get_activity with an activityId for details.",
		InputSchema: schemaB(props),
		Handler:     listActivities,
	}
}

func listActivities(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
	var args map[string]json.RawMessage
	if err := decodeArgsB(raw, &args); err != nil {
		return badArgsB(err)
	}
	known := map[string]activityParamB{}
	for _, p := range activityParamsB {
		known[p.arg] = p
	}
	q := url.Values{}
	start, limit := 0, defaultActivityLimitB
	for name, v := range args {
		p, ok := known[name]
		if !ok {
			return badArgsB(fmt.Errorf("invalid arguments: unknown argument %q", name))
		}
		s, n, err := queryValueB(p, v)
		if err != nil {
			return badArgsB(err)
		}
		switch name {
		case "start":
			if n < 0 {
				return badArgsB(fmt.Errorf("invalid arguments: start must not be negative"))
			}
			start = int(n)
			continue
		case "limit":
			if n < 1 {
				return badArgsB(fmt.Errorf("invalid arguments: limit must be at least 1"))
			}
			limit = int(min(n, maxActivityLimitB))
			continue
		case "start_date", "end_date":
			if _, err := ParseDate(s); err != nil {
				return badArgsB(err)
			}
		}
		q.Set(p.query, s)
	}
	q.Set("start", strconv.Itoa(start))
	q.Set("limit", strconv.Itoa(limit))

	data, noData, res, err := fetchJSON(ctx, c, apiKey, "activitylist-service/activities/search/activities", q)
	if err != nil {
		return Result{}, err
	}
	if len(res.Errors) > 0 {
		return res, nil
	}
	list, _ := data.([]any)
	if noData || len(list) == 0 {
		return NoData("no activities match the filters"), nil
	}
	out := make([]any, 0, len(list))
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			out = append(out, trimActivityB(m))
		}
	}
	return Result{Data: map[string]any{"start": start, "limit": limit, "count": len(out), "activities": out}}, nil
}

// queryValueB validates one argument against its declared kind and renders it
// as a query value. For integer kinds n carries the numeric value.
func queryValueB(p activityParamB, v json.RawMessage) (s string, n int64, err error) {
	bad := func() error { return fmt.Errorf("invalid arguments: %s must be %s", p.arg, p.kind) }
	switch p.kind {
	case "string":
		if err := json.Unmarshal(v, &s); err != nil || s == "" {
			return "", 0, bad()
		}
	case "boolean":
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return "", 0, bad()
		}
		s = strconv.FormatBool(b)
	case "integer", "number":
		f, ok := numberB(v)
		if !ok || (p.kind == "integer" && f != math.Trunc(f)) {
			return "", 0, bad()
		}
		if p.kind == "integer" {
			n = int64(f)
			s = strconv.FormatInt(n, 10)
		} else {
			s = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return s, n, nil
}

// numberB parses a JSON number (not a string) into a finite float64.
func numberB(v json.RawMessage) (float64, bool) {
	var f float64
	if bytes.HasPrefix(bytes.TrimSpace(v), []byte(`"`)) || json.Unmarshal(v, &f) != nil {
		return 0, false
	}
	return f, !math.IsInf(f, 0) && !math.IsNaN(f) && math.Abs(f) < 1<<53
}

func trimActivityB(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range activityFieldsB {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case map[string]any: // activityType: keep only the key
			if tk, ok := x["typeKey"]; ok {
				out[k] = tk
			}
		case float64:
			out[k] = math.Round(x*100) / 100
		default:
			out[k] = v
		}
	}
	return out
}

type activityArgsB struct {
	ActivityID json.RawMessage `json:"activity_id"`
}

func getActivityTool() Tool {
	return Tool{
		Name:        "get_activity",
		Description: "Full details of one activity and its workouts, by integer activity_id (the activityId from list_activities).",
		InputSchema: schemaB(map[string]any{
			"activity_id": map[string]any{"type": "integer", "description": "Garmin activity id, for example 12345678901."},
		}, "activity_id"),
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var a activityArgsB
			if err := decodeArgsB(raw, &a); err != nil {
				return badArgsB(err)
			}
			f, ok := numberB(a.ActivityID)
			if !ok || f != math.Trunc(f) || f < 1 {
				return badArgsB(fmt.Errorf("invalid arguments: activity_id must be a positive integer"))
			}
			id := strconv.FormatInt(int64(f), 10)
			parts, err := fetchPartsB(ctx, c, apiKey, []partB{
				{name: "activity", path: "activity-service/activity/" + id},
				{name: "workouts", path: "activity-service/activity/" + id + "/workouts"},
			})
			if err != nil {
				return Result{}, err
			}
			return combinePartsB(parts, "no data for activity "+id), nil
		},
	}
}
