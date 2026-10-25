package tools

import "net/url"

func sleepTool() Tool {
	// Paths from the real dailySleepSummariesByDate shape. The score is
	// sleepScores.overall; the other sleepScores entries are too bulky.
	full := []fieldA{fa("calendarDate", "calendarDate"), fa("sleepTimeSeconds", "sleepTimeSeconds"), fa("deepSleepSeconds", "deepSleepSeconds"),
		fa("lightSleepSeconds", "lightSleepSeconds"), fa("remSleepSeconds", "remSleepSeconds"), fa("awakeSleepSeconds", "awakeSleepSeconds"),
		fa("awakeCount", "awakeCount"), fa("sleepScore", "sleepScores", "overall", "value"), fa("sleepScoreQualifier", "sleepScores", "overall", "qualifierKey"),
		fa("avgSleepStress", "avgSleepStress"), fa("averageRespirationValue", "averageRespirationValue"),
		fa("sleepStartTimestampGMT", "sleepStartTimestampGMT"), fa("sleepEndTimestampGMT", "sleepEndTimestampGMT")}
	lean := []fieldA{fa("calendarDate", "calendarDate"), fa("sleepTimeSeconds", "sleepTimeSeconds"), fa("deepSleepSeconds", "deepSleepSeconds"),
		fa("lightSleepSeconds", "lightSleepSeconds"), fa("remSleepSeconds", "remSleepSeconds"), fa("awakeSleepSeconds", "awakeSleepSeconds"),
		fa("sleepScore", "sleepScores", "overall", "value"), fa("avgSleepStress", "avgSleepStress"), fa("averageRespirationValue", "averageRespirationValue")}
	tiers := [][]fieldA{full, lean}
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
				return projectRowsA(data, tiers)
			}
			return shrinkA(data, r.full)
		},
	}.tool()
}
