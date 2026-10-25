package tools

import "net/url"

func heartRateTool() Tool {
	return specA{
		name:  "get_heart_rate",
		label: "heart rate",
		description: "Get the heart rate for one day: resting, min and max heart rate plus the intraday samples. " +
			"Give date (YYYY-MM-DD); a single day only. Samples are downsampled with min/max/mean kept unless full is true.",
		mode: singleA,
		single: func(d string) (string, url.Values) {
			return "wellness-service/wellness/dailyHeartRate", dateQueryA("date", d)
		},
		shape: shrinkShapeA,
	}.tool()
}
