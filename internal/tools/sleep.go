package tools

import "net/url"

func sleepTool() Tool {
	keys := []string{"calendarDate", "sleepTimeSeconds", "deepSleepSeconds", "lightSleepSeconds", "remSleepSeconds", "awakeSleepSeconds",
		"sleepStartTimestampGMT", "sleepEndTimestampGMT", "averageSpO2Value", "averageRespirationValue", "avgOvernightHrv", "restingHeartRate", "sleepScores", "overallScore"}
	return specA{
		name:  "get_sleep",
		label: "sleep",
		description: "Get Garmin sleep data (duration, stages, score) for one night, or sleep summaries for a range. " +
			"Give date (YYYY-MM-DD) or start_date and end_date (YYYY-MM-DD, at most 31 days); ranges return a compact set of fields per night.",
		mode:    eitherA,
		maxDays: 31,
		single:  func(d string) (string, url.Values) { return "sleep-service/sleep/dailySleep", dateQueryA("date", d) },
		rng: func(f, t string) (string, url.Values) {
			return "sleep-service/sleep/dailySleepSummariesByDate", rangeQueryA(f, t)
		},
		shape: func(data any, r reqA) (any, string) {
			if r.isRange() && !r.full {
				return compactRowsA(data, keys)
			}
			return shrinkA(data, r.full)
		},
	}.tool()
}
