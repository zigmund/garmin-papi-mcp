package tools

import "net/url"

func dailySummaryTool() Tool {
	// Fields kept per day when a range is requested; use a single date for the
	// full daily summary.
	keys := []string{"calendarDate", "totalSteps", "dailyStepGoal", "totalDistanceMeters", "totalKilocalories", "activeKilocalories",
		"floorsAscended", "moderateIntensityMinutes", "vigorousIntensityMinutes", "restingHeartRate", "minHeartRate", "maxHeartRate",
		"averageStressLevel", "bodyBatteryHighestValue", "bodyBatteryLowestValue"}
	return specA{
		name:  "get_daily_summary",
		label: "daily summary",
		description: "Get the Garmin daily summary (steps, calories, distance, floors, intensity minutes, heart rate, stress, Body Battery) for one date, or per-day totals for a range. " +
			"Give date (YYYY-MM-DD) or start_date and end_date (YYYY-MM-DD, at most 365 days); ranges return a compact set of fields per day.",
		mode:    eitherA,
		maxDays: 365,
		single: func(d string) (string, url.Values) {
			return "usersummary-service/usersummary/daily", dateQueryA("calendarDate", d)
		},
		rng: func(f, t string) (string, url.Values) {
			return "usersummary-service/v2/dailysummaries", rangeQueryA(f, t)
		},
		shape: func(data any, r reqA) (any, string) {
			if r.isRange() && !r.full {
				return compactRowsA(data, keys)
			}
			return shrinkA(data, r.full)
		},
	}.tool()
}
