package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

func getMenstrualCycleTool() Tool {
	return Tool{
		Name:        "get_menstrual_cycle",
		Description: "Menstrual cycle summary and day view for one date. The date is YYYY-MM-DD; many accounts have no cycle data.",
		InputSchema: schemaB(map[string]any{"date": dateSchemaB("Day to read.")}, "date"),
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var a dateArgsB
			if err := decodeArgsB(raw, &a); err != nil {
				return badArgsB(err)
			}
			if _, err := ParseDate(a.Date); err != nil {
				return badArgsB(err)
			}
			parts, err := fetchPartsB(ctx, c, apiKey, []partB{
				{name: "summary", path: "periodichealth-service/menstrualcycle/summary/" + a.Date},
				{name: "dayview", path: "periodichealth-service/menstrualcycle/dayview/" + a.Date},
			})
			if err != nil {
				return Result{}, err
			}
			return combinePartsB(parts, fmt.Sprintf("no menstrual cycle data for %s", a.Date)), nil
		},
	}
}
