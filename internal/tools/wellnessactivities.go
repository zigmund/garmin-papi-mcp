package tools

// The Garmin spec documents no range limit for the wellness activity summary;
// the server conservatively allows 31 days, like the other wellness ranges.
func getWellnessActivitiesTool() Tool {
	return rangeToolB("get_wellness_activities",
		"Summary of wellness activities (all-day movement and intensity minutes) for a date range. Dates are YYYY-MM-DD; the range may span at most 31 days.",
		"wellnessactivity-service/activity/summary/range/%s/%s", 31, "wellness activities")
}
