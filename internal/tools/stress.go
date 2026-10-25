package tools

import "net/url"

func stressTool() Tool {
	return specA{
		name:  "get_stress",
		label: "stress",
		description: "Get daily stress statistics: per day, the overall stress level and the time spent at rest and at low, medium and high stress. " +
			"Give start_date and end_date (YYYY-MM-DD, at most 28 days).",
		mode:    rangeA,
		maxDays: 28,
		rng: func(f, t string) (string, url.Values) {
			return "usersummary-service/stats/stress/daily/" + f + "/" + t, nil
		},
		shape: shrinkShapeA,
	}.tool()
}
