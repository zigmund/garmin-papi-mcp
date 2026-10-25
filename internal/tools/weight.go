package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// maxWeightDaysB is a sanity bound; the Garmin spec documents no limit for
// weight/range, and the tool never fans out into several calls.
const maxWeightDaysB = 366

type weightArgsB struct {
	StartDate  string `json:"start_date"`
	EndDate    string `json:"end_date"`
	IncludeAll bool   `json:"include_all"`
}

func getWeightTool() Tool {
	return Tool{
		Name:        "get_weight",
		Description: "Weight and body composition entries plus the weight goal for a date range. Dates are YYYY-MM-DD; the range may span at most 366 days. By default only one entry per day is returned; set include_all to true for every measurement.",
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
			return combinePartsB(parts, fmt.Sprintf("no weight data for %s to %s", a.StartDate, a.EndDate)), nil
		},
	}
}
