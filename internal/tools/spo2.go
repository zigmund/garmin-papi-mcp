package tools

import "net/url"

func spo2Tool() Tool {
	return specA{
		name:  "get_spo2",
		label: "SpO2",
		description: "Get blood oxygen (SpO2) for one day: average, lowest and the readings. " +
			"Give date (YYYY-MM-DD); a single day only. Readings are downsampled with min/max/mean kept unless full is true.",
		mode: singleA,
		single: func(d string) (string, url.Values) {
			return "wellness-service/wellness/daily/spo2acclimation/" + d, nil
		},
		shape: shrinkShapeA,
	}.tool()
}
