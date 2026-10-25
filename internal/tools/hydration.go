package tools

func getHydrationTool() Tool {
	return rangeToolB("get_hydration",
		"Daily hydration intake and goals for a date range. Dates are YYYY-MM-DD; the range may span at most 31 days.",
		"usersummary-service/stats/hydration/daily/%s/%s", 31, "hydration data")
}
