package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// GroupB returns the activity, body, nutrition and cycle tools.
func GroupB() []Tool {
	return []Tool{
		listActivitiesTool(),
		getActivityTool(),
		getWeightTool(),
		getNutritionTool(),
		getHydrationTool(),
		getFitnessAgeTool(),
		getWellnessActivitiesTool(),
		getMenstrualCycleTool(),
	}
}

// decodeArgsB strictly decodes tool arguments: unknown fields and wrong types
// are rejected with a readable message (never a Garmin call).
func decodeArgsB(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func badArgsB(err error) (Result, error) {
	return Result{Errors: []string{err.Error()}}, nil
}

func schemaB(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func dateSchemaB(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc + " Format YYYY-MM-DD."}
}

func boolSchemaB(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// partB is one Garmin call of a tool that combines several endpoints.
type partB struct {
	name string
	path string
	q    url.Values
}

type partResultB struct {
	name   string
	data   any
	noData bool
	errs   []string
}

// fetchPartsB calls every part in order. A rejected key aborts immediately.
func fetchPartsB(ctx context.Context, c Getter, apiKey string, parts []partB) ([]partResultB, error) {
	out := make([]partResultB, 0, len(parts))
	for _, p := range parts {
		data, noData, res, err := fetchJSON(ctx, c, apiKey, p.path, p.q)
		if err != nil {
			return nil, err
		}
		pr := partResultB{name: p.name, data: data, noData: noData}
		for _, e := range res.Errors {
			pr.errs = append(pr.errs, p.name+": "+e)
		}
		out = append(out, pr)
	}
	return out, nil
}

// combinePartsB merges part results: successful parts go into Data keyed by
// part name, failed parts become Errors entries naming the part. It is an
// error (Data nil) only when every part failed, and NoData when no part failed
// and none had data.
func combinePartsB(parts []partResultB, noDataMsg string) Result {
	data := map[string]any{}
	var errs, empty []string
	for _, p := range parts {
		switch {
		case len(p.errs) > 0:
			errs = append(errs, p.errs...)
		case p.noData:
			empty = append(empty, p.name)
		default:
			data[p.name] = p.data
		}
	}
	if len(data) == 0 {
		if len(errs) > 0 {
			return Result{Errors: errs}
		}
		return NoData(noDataMsg)
	}
	res := Result{Data: data, Errors: errs}
	if len(empty) > 0 {
		res.Note = "no data for: " + strings.Join(empty, ", ")
	}
	return res
}

type rangeArgsB struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

// rangeToolB builds a single-endpoint tool over a start_date..end_date range.
func rangeToolB(name, desc, pathFmt string, maxDays int, what string) Tool {
	return Tool{
		Name:        name,
		Description: desc,
		InputSchema: schemaB(map[string]any{
			"start_date": dateSchemaB("First day of the range."),
			"end_date":   dateSchemaB("Last day of the range (inclusive)."),
		}, "start_date", "end_date"),
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var a rangeArgsB
			if err := decodeArgsB(raw, &a); err != nil {
				return badArgsB(err)
			}
			if _, _, err := ParseRange(a.StartDate, a.EndDate, maxDays); err != nil {
				return badArgsB(err)
			}
			data, noData, res, err := fetchJSON(ctx, c, apiKey, fmt.Sprintf(pathFmt, a.StartDate, a.EndDate), nil)
			if err != nil {
				return Result{}, err
			}
			if len(res.Errors) > 0 {
				return res, nil
			}
			if noData {
				return NoData(fmt.Sprintf("no %s for %s to %s", what, a.StartDate, a.EndDate)), nil
			}
			return Result{Data: data}, nil
		},
	}
}
