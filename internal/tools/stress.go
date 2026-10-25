package tools

import "net/url"

func stressTool() Tool {
	return specA{
		name:  "get_stress",
		label: "stress",
		description: "Get daily stress statistics and stress level samples. " +
			"Give start_date and end_date (YYYY-MM-DD, at most 28 days). Samples are downsampled with min/max/mean kept unless full is true.",
		mode:    rangeA,
		maxDays: 28,
		rng: func(f, t string) (string, url.Values) {
			return "usersummary-service/stats/stress/daily/" + f + "/" + t, nil
		},
		shape: shrinkShapeA,
	}.tool()
}
