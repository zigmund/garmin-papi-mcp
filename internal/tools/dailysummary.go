package tools

import "net/url"

func dailySummaryTool() Tool {
	// Range rows are nested; project them to a flat set of useful values
	// (paths from the real dailysummaries shape), richest tier first.
	full := []fieldA{fa("calendarDate", "calendarDate"), fa("steps", "movement", "steps", "value"), fa("stepGoal", "movement", "steps", "goal"),
		fa("distanceMeters", "movement", "steps", "distanceInMeters"), fa("floorsAscended", "movement", "floorsAscended", "value"),
		fa("caloriesTotal", "calories", "burnedTotal"), fa("caloriesActive", "calories", "burnedActive"),
		fa("hrMin", "heartRate", "minValue"), fa("hrMax", "heartRate", "maxValue"), fa("hrResting", "heartRate", "restingValue"),
		fa("intensityModerateMin", "intensityMinutes", "moderate"), fa("intensityVigorousMin", "intensityMinutes", "vigorous"),
		fa("stressAvg", "stress", "avgLevel"), fa("bodyBatteryMin", "bodyBattery", "minValue"), fa("bodyBatteryMax", "bodyBattery", "maxValue"),
		fa("bodyBatteryCharged", "bodyBattery", "chargedValue"), fa("bodyBatteryDrained", "bodyBattery", "drainedValue"),
		fa("respirationAvg", "respiration", "avgValue")}
	lean := []fieldA{fa("calendarDate", "calendarDate"), fa("steps", "movement", "steps", "value"), fa("caloriesTotal", "calories", "burnedTotal"),
		fa("hrResting", "heartRate", "restingValue"), fa("stressAvg", "stress", "avgLevel"), fa("bodyBatteryMax", "bodyBattery", "maxValue")}
	tiers := [][]fieldA{full, lean}
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
				return projectRowsA(data, tiers)
			}
			return shrinkA(data, r.full)
		},
	}.tool()
}
