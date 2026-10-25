package tools

import (
	"context"
	"encoding/json"
)

func profileTool() Tool {
	return Tool{
		Name:        "get_profile",
		Description: "Get the user's Garmin profile settings (units, time zone, preferences) and heart rate zones. Takes no arguments.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			var none struct{}
			if err := decodeA(raw, &none); err != nil {
				return Result{Errors: []string{err.Error()}}, nil
			}
			parts := []struct{ key, label, path string }{
				{"settings", "settings", "userprofile-service/userprofile/settings"},
				{"heart_rate_zones", "heart rate zones", "biometric-service/heartRateZones/v2"},
			}
			data := map[string]any{}
			var errs []string
			failed := 0
			for _, p := range parts {
				d, noData, res, err := fetchJSON(ctx, c, apiKey, p.path, nil)
				if err != nil {
					return Result{}, err
				}
				if len(res.Errors) > 0 {
					failed++
					for _, e := range res.Errors {
						errs = append(errs, p.label+": "+e)
					}
					continue
				}
				if !noData {
					data[p.key] = d
				}
			}
			switch {
			case failed == len(parts):
				return Result{Errors: errs}, nil
			case len(data) == 0:
				return NoData("no data for profile"), nil
			}
			return Result{Data: data, Errors: errs}, nil
		},
	}
}
