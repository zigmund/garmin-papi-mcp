package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

type nutritionArgsB struct {
	Date             string `json:"date"`
	IncludeGoals     *bool  `json:"include_goals"`
	IncludeNutrition *bool  `json:"include_nutrition"`
}

func getNutritionTool() Tool {
	return Tool{
		Name:        "get_nutrition",
		Description: "Food log for one date: meals and logged foods. The date is YYYY-MM-DD. Optionally include nutrition goals and detailed nutrient values.",
		InputSchema: schemaB(map[string]any{
			"date":              dateSchemaB("Day to read."),
			"include_goals":     boolSchemaB("Include the user's nutrition goals."),
			"include_nutrition": boolSchemaB("Include detailed nutrient values."),
		}, "date"),
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var a nutritionArgsB
			if err := decodeArgsB(raw, &a); err != nil {
				return badArgsB(err)
			}
			if _, err := ParseDate(a.Date); err != nil {
				return badArgsB(err)
			}
			q := url.Values{}
			if a.IncludeGoals != nil {
				q.Set("includeGoals", fmt.Sprint(*a.IncludeGoals))
			}
			if a.IncludeNutrition != nil {
				q.Set("includeNutrition", fmt.Sprint(*a.IncludeNutrition))
			}
			data, noData, res, err := fetchJSON(ctx, c, apiKey, "nutrition-service/food/logs/"+a.Date, q)
			if err != nil {
				return Result{}, err
			}
			if len(res.Errors) > 0 {
				return res, nil
			}
			if noData || nutritionEmptyB(data) {
				return NoData(fmt.Sprintf("no food logged for %s", a.Date)), nil
			}
			return Result{Data: data}, nil
		},
	}
}

// nutritionEmptyB reports a day whose log object exists but holds no food:
// the meal and food lists are present and empty.
func nutritionEmptyB(data any) bool {
	m, ok := data.(map[string]any)
	if !ok {
		return false
	}
	seen := false
	for _, k := range []string{"mealDetails", "loggedFoodsWithServingSizes"} {
		v, present := m[k]
		if !present {
			continue
		}
		seen = true
		if l, isList := v.([]any); !isList || len(l) > 0 {
			return false
		}
	}
	return seen
}
