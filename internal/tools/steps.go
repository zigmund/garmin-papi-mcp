package tools

import "net/url"

func stepsTool() Tool {
	return specA{
		name:        "get_steps",
		label:       "steps",
		description: "Get daily step counts, distance and step goal. Give start_date and end_date (YYYY-MM-DD, at most 28 days).",
		mode:        rangeA,
		maxDays:     28,
		rng: func(f, t string) (string, url.Values) {
			return "usersummary-service/stats/steps/daily/" + f + "/" + t, nil
		},
		shape: shrinkShapeA,
	}.tool()
}
