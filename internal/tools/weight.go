package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
			var notes []string
			for i := range parts {
				if parts[i].name == "weight" && !a.IncludeAll && len(parts[i].errs) == 0 && !parts[i].noData {
					parts[i].data, notes = trimWeightB(parts[i].data)
				}
			}
			res := combinePartsB(parts, fmt.Sprintf("no weight data for %s to %s", a.StartDate, a.EndDate))
			if len(notes) > 0 && res.Data != nil {
				if res.Note != "" {
					notes = append([]string{res.Note}, notes...)
				}
				res.Note = strings.Join(notes, "; ")
			}
			return res, nil
		},
	}
}

// weightKeepB are the fields kept in trimmed weight entries; weightMeasureB is
// the subset that counts as a measurement (an entry is "recognised" only when
// its trimmed form still holds one of them).
var weightKeepB = map[string]bool{
	"date": true, "calendarDate": true, "summaryDate": true, "timestampGMT": true,
	"numOfWeightEntries": true,
	"weight":             true, "bmi": true, "bodyFat": true, "bodyWater": true, "muscleMass": true,
	"boneMass": true, "minWeight": true, "maxWeight": true, "avgWeight": true,
}

var weightMeasureB = map[string]bool{
	"weight": true, "bmi": true, "bodyFat": true, "bodyWater": true, "muscleMass": true,
	"boneMass": true, "minWeight": true, "maxWeight": true, "avgWeight": true,
}

// weightBudgetB is the size the weight part may take, leaving room for the goal
// within the ~8 KB output budget.
const weightBudgetB = 7000

// trimWeightB thins a weight/range reply to stay within the output budget.
// previousDateWeight and nextDateWeight are dropped. Each daily summary is
// reduced to its whitelisted fields (nested objects likewise, nested lists are
// dropped) only when that still leaves at least one measurement key; otherwise
// the entry is kept whole and a note says the layout was not recognised. If the
// result is still over weightBudgetB, entries are thinned evenly (first and
// last kept) and a note says how many were dropped. Notes are returned.
func trimWeightB(data any) (any, []string) {
	m, ok := data.(map[string]any)
	if !ok {
		return data, nil
	}
	var notes []string
	out := map[string]any{}
	for k, v := range m {
		switch k {
		case "previousDateWeight", "nextDateWeight":
			continue
		case "dailyWeightSummaries":
			if l, ok := v.([]any); ok {
				ts := make([]any, 0, len(l))
				unknown := false
				for _, e := range l {
					t, rec := trimWeightEntryB(e)
					if !rec {
						unknown = true
					}
					ts = append(ts, t)
				}
				if unknown {
					notes = append(notes, "weight entry layout not recognised, some entries are returned untrimmed")
				}
				out[k] = ts
				continue
			}
		}
		out[k] = v
	}
	if l, ok := out["dailyWeightSummaries"].([]any); ok {
		if kept := thinB(out, l, weightBudgetB); kept < len(l) {
			notes = append(notes, fmt.Sprintf("weight output thinned to %d of %d daily entries to fit the size budget", kept, len(l)))
		}
	}
	return out, notes
}

// thinB shrinks out["dailyWeightSummaries"] (orig) until out marshals within
// budget or two entries remain, keeping the first and last. It returns the
// number of entries kept.
func thinB(out map[string]any, orig []any, budget int) int {
	n := len(orig)
	for {
		b, _ := json.Marshal(out)
		if len(b) <= budget || n <= 2 {
			return n
		}
		n = max(2, n*3/4)
		picked := make([]any, 0, n)
		for i := 0; i < n; i++ {
			picked = append(picked, orig[i*(len(orig)-1)/(n-1)])
		}
		out["dailyWeightSummaries"] = picked
	}
}

func hasMeasureB(m map[string]any) bool {
	for k, v := range m {
		if weightMeasureB[k] && v != nil {
			return true
		}
	}
	return false
}

// trimWeightEntryB returns the trimmed entry and true, or the entry whole and
// false when trimming would leave no measurement key.
func trimWeightEntryB(e any) (any, bool) {
	m, ok := e.(map[string]any)
	if !ok {
		return e, false
	}
	out := map[string]any{}
	found := false
	for k, v := range m {
		switch x := v.(type) {
		case map[string]any:
			sub := map[string]any{}
			for sk, sv := range x {
				if weightKeepB[sk] {
					sub[sk] = sv
				}
			}
			if hasMeasureB(sub) {
				found = true
			}
			out[k] = sub
		case []any:
			continue
		default:
			if weightKeepB[k] && v != nil {
				out[k] = v
				if weightMeasureB[k] {
					found = true
				}
			}
		}
	}
	if !found {
		return e, false
	}
	return out, true
}
