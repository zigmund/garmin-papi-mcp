package tools

import "net/url"

func bodyBatteryTool() Tool {
	return specA{
		name:  "get_body_battery",
		label: "Body Battery",
		description: "Get Body Battery (energy level) reports per day: charged, drained and the level samples. " +
			"Give start_date and end_date (YYYY-MM-DD, at most 31 days). Samples are downsampled with min/max/mean kept unless full is true.",
		mode:    rangeA,
		maxDays: 31,
		rng: func(f, t string) (string, url.Values) {
			return "wellness-service/wellness/bodyBattery/reports/daily", rangeQueryA(f, t)
		},
		shape: bodyBatteryShapeA,
	}.tool()
}
