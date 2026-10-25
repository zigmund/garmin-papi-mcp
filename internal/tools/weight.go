package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// maxWeightDaysB keeps the output within the ~8 KB budget; the Garmin spec
// documents no limit for weight/range.
const maxWeightDaysB = 31

type weightArgsB struct {
	StartDate  string `json:"start_date"`
	EndDate    string `json:"end_date"`
	IncludeAll bool   `json:"include_all"`
}

func getWeightTool() Tool {
	return Tool{
		Name:        "get_weight",
		Description: "Weight and body composition entries plus the weight goal for a date range. Dates are YYYY-MM-DD; the range may span at most 31 days. By default only one entry per day is returned; set include_all to true for every measurement.",
		InputSchema: schemaB(map[string]any{
			"start_date":  dateSchemaB("First day of the range."),
			"end_date":    dateSchemaB("Last day of the range (inclusive)."),
			"include_all": boolSchemaB("Return every measurement instead of one per day (default false)."),
		}, "start_date", "end_date"),
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var a weightArgsB
			if err := decodeArgsB(raw, &a); err != nil {
				return badArgsB(err)
			}
			if _, _, err := ParseRange(a.StartDate, a.EndDate, maxWeightDaysB); err != nil {
				return badArgsB(err)
			}
			q := url.Values{"includeAll": {fmt.Sprint(a.IncludeAll)}}
			parts, err := fetchPartsB(ctx, c, apiKey, []partB{
				{name: "weight", path: fmt.Sprintf("weight-service/weight/range/%s/%s", a.StartDate, a.EndDate), q: q},
				{name: "goal", path: fmt.Sprintf("goal-service/goal/user/effective/weightgoal/%s/%s", a.StartDate, a.EndDate)},
			})
			if err != nil {
				return Result{}, err
			}
			for i := range parts {
				if parts[i].name == "weight" && !a.IncludeAll {
					parts[i].data = trimWeightB(parts[i].data)
				}
			}
			return combinePartsB(parts, fmt.Sprintf("no weight data for %s to %s", a.StartDate, a.EndDate)), nil
		},
	}
}

// weightKeepB are the measurement fields kept in trimmed weight entries.
var weightKeepB = map[string]bool{
	"calendarDate": true, "summaryDate": true, "weight": true, "bmi": true,
	"bodyFat": true, "bodyWater": true, "muscleMass": true, "boneMass": true,
	"minWeight": true, "maxWeight": true, "avgWeight": true, "numOfWeightEntries": true,
}

// trimWeightB thins a weight/range reply to stay within the output budget:
// previousDateWeight and nextDateWeight are dropped; each daily summary keeps
// its scalar fields, and nested measurement objects (for example latestWeight)
// keep only weightKeepB fields; nested lists (all measurements of the day) are
// dropped. include_all skips this trimming. Real entries are not in the
// scrubbed shape sample (it has an empty list), so this follows the known
// Garmin layout and leaves unknown shapes intact.
func trimWeightB(data any) any {
	m, ok := data.(map[string]any)
	if !ok {
		return data
	}
	out := map[string]any{}
	for k, v := range m {
		switch k {
		case "previousDateWeight", "nextDateWeight":
			continue
		case "dailyWeightSummaries":
			if l, ok := v.([]any); ok {
				ts := make([]any, 0, len(l))
				for _, e := range l {
					ts = append(ts, trimWeightEntryB(e))
				}
				out[k] = ts
				continue
			}
		}
		out[k] = v
	}
	return out
}

func trimWeightEntryB(e any) any {
	m, ok := e.(map[string]any)
	if !ok {
		return e
	}
	out := map[string]any{}
	for k, v := range m {
		switch x := v.(type) {
		case []any:
			continue
		case map[string]any:
			sub := map[string]any{}
			for sk, sv := range x {
				if weightKeepB[sk] {
					sub[sk] = sv
				}
			}
			out[k] = sub
		default:
			if v != nil {
				out[k] = v
			}
		}
	}
	return out
}
