package tools

import "net/url"

func hrvTool() Tool {
	return specA{
		name:  "get_hrv",
		label: "HRV",
		description: "Get heart rate variability (HRV status, nightly averages and readings) for one date or a range. " +
			"Give date (YYYY-MM-DD) or start_date and end_date (YYYY-MM-DD, at most 28 days). Readings are downsampled unless full is true.",
		mode: eitherA,
		// Garmin documents no limit for hrv/daily/{from}/{to}; 28 days is our own
		// conservative cap to keep responses and rate-limit use reasonable.
		maxDays: 28,
		single:  func(d string) (string, url.Values) { return "hrv-service/hrv/" + d, nil },
		rng:     func(f, t string) (string, url.Values) { return "hrv-service/hrv/daily/" + f + "/" + t, nil },
		shape:   shrinkShapeA,
	}.tool()
}
