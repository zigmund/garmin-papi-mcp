package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type dateArgsB struct {
	Date string `json:"date"`
}

func getFitnessAgeTool() Tool {
	return Tool{
		Name:        "get_fitness_age",
		Description: "Fitness age and its contributing factors for one date. The date is YYYY-MM-DD.",
		InputSchema: schemaB(map[string]any{"date": dateSchemaB("Day to read.")}, "date"),
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var a dateArgsB
			if err := decodeArgsB(raw, &a); err != nil {
				return badArgsB(err)
			}
			if _, err := ParseDate(a.Date); err != nil {
				return badArgsB(err)
			}
			data, noData, res, err := fetchJSON(ctx, c, apiKey, "fitnessage-service/fitnessage/v2/"+a.Date, nil)
			if err != nil {
				return Result{}, err
			}
			if len(res.Errors) > 0 {
				return res, nil
			}
			if noData {
				return NoData(fmt.Sprintf("no fitness age data for %s", a.Date)), nil
			}
			return Result{Data: data}, nil
		},
	}
}
