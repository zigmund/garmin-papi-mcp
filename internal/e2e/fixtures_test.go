package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
)

// All fixtures are synthetic and generated in code; sizes imitate real
// responses (see the minimum sizes asserted by the budget test).

const baseMillis = 1760000000000

func dayStr(i int) string { return fmt.Sprintf("2026-01-%02d", i%28+1) }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func pairs(n, step int, status bool) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		if status {
			fmt.Fprintf(&sb, `[%d,"MEASURED",%d]`, baseMillis+i*step, 30+i%60)
		} else {
			fmt.Fprintf(&sb, "[%d,%d]", baseMillis+i*step, 40+i%60)
		}
	}
	sb.WriteString("]")
	return sb.String()
}

func objects(n int, key string) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"startGMT":"2026-10-24T01:%02d:00.0","endGMT":"2026-10-24T01:%02d:00.0","%s":%d}`, i%60, (i+5)%60, key, 30+i%50)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func heartRateDay(n int) string {
	return `{"calendarDate":"2026-10-24","restingHeartRate":48,"maxHeartRate":150,"minHeartRate":44,"heartRateValues":` + pairs(n, 120000, false) + `}`
}

func spo2Day(n int) string {
	return `{"calendarDate":"2026-10-24","averageSpO2":95,"lowestSpO2":90,"spO2SingleValues":` + objects(n, "spo2Reading") + `}`
}

func hrvDay(n int) string {
	return `{"hrvSummary":{"weeklyAvg":45,"lastNightAvg":47,"status":"BALANCED"},"hrvReadings":` + objects(n, "hrvValue") + `}`
}

func bodyBatteryDays(days, samples int) string {
	rows := make([]string, days)
	for d := range rows {
		rows[d] = fmt.Sprintf(`{"date":%q,"charged":%d,"drained":50,"bodyBatteryValuesArray":%s}`, dayStr(d), 40+d, pairs(samples, 180000, true))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func stressDays(days, samples int) string {
	rows := make([]string, days)
	for d := range rows {
		rows[d] = fmt.Sprintf(`{"calendarDate":%q,"values":{"overallStressLevel":31,"restStressDuration":9000},"stressValuesArray":%s}`, dayStr(d), pairs(samples, 180000, false))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func stepsDays(days int) string {
	rows := make([]string, days)
	for d := range rows {
		rows[d] = fmt.Sprintf(`{"calendarDate":%q,"totalSteps":%d,"totalDistance":%d,"stepGoal":8000}`, dayStr(d), 6000+d*37, 4500+d)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func sleepNights(n int) string {
	rows := make([]string, n)
	for i := range rows {
		q := func(extra string) string { return `{"value":70,"qualifierKey":"GOOD"` + extra + `}` }
		rows[i] = fmt.Sprintf(`{"id":%d,"userProfilePK":1,"calendarDate":%q,"sleepTimeSeconds":%d,"napTimeSeconds":0,"sleepWindowConfirmed":true,"sleepWindowConfirmationType":"enhanced_confirmed_final",`+
			`"sleepStartTimestampGMT":1760000000000,"sleepEndTimestampGMT":1760028000000,"sleepStartTimestampLocal":1,"sleepEndTimestampLocal":2,"autoSleepStartTimestampGMT":null,"autoSleepEndTimestampGMT":null,`+
			`"unmeasurableSleepSeconds":0,"deepSleepSeconds":%d,"lightSleepSeconds":14000,"remSleepSeconds":5000,"awakeSleepSeconds":600,"deviceRemCapable":true,"retro":false,"sleepFromDevice":true,`+
			`"averageRespirationValue":14.5,"lowestRespirationValue":10,"highestRespirationValue":20,"awakeCount":2,"avgSleepStress":18.5,"ageGroup":"ADULT",`+
			`"sleepScoreFeedback":"POSITIVE_LONG_AND_DEEP","sleepScoreInsight":"NONE","sleepScorePersonalizedInsight":"NOT_AVAILABLE",`+
			`"sleepScores":{"totalDuration":{"qualifierKey":"GOOD","optimalStart":28800,"optimalEnd":28800},"stress":{"qualifierKey":"FAIR","optimalStart":0,"optimalEnd":15},"awakeCount":{"qualifierKey":"GOOD","optimalStart":0,"optimalEnd":1},`+
			`"overall":{"value":%d,"qualifierKey":"GOOD"},"remPercentage":%s,"restlessness":{"qualifierKey":"FAIR","optimalStart":0,"optimalEnd":5},"lightPercentage":%s,"deepPercentage":%s},"sleepVersion":2}`,
			i, dayStr(i), 27000+i, 7000+i, 60+i%30,
			q(`,"optimalStart":21,"optimalEnd":31,"idealStartInSeconds":6000,"idealEndInSeconds":9000`),
			q(`,"optimalStart":30,"optimalEnd":64,"idealStartInSeconds":6000,"idealEndInSeconds":9000`),
			q(`,"optimalStart":16,"optimalEnd":33,"idealStartInSeconds":6000,"idealEndInSeconds":9000`))
	}
	if n == 1 {
		return rows[0]
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func dailySummaryRows(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"calendarDate":%q,"movement":{"steps":{"value":%d,"goal":8000,"distanceInMeters":%d},"floorsAscended":{"value":%d,"goal":10}},`+
			`"calories":{"burnedTotal":2400,"burnedActive":600,"burnedResting":1800},"heartRate":{"minValue":44,"maxValue":150,"restingValue":48},`+
			`"intensityMinutes":{"moderate":20,"vigorous":10,"goal":150},"stress":{"avgLevel":31,"maxLevel":88},`+
			`"bodyBattery":{"minValue":10,"maxValue":90,"chargedValue":60,"drainedValue":55},"respiration":{"avgValue":14.5,"minValue":9,"maxValue":22}}`,
			dayStr(i), 6000+i, 4500+i, i%12)
	}
	if n == 1 {
		return rows[0]
	}
	return `{"data":[` + strings.Join(rows, ",") + `]}`
}

func activityList(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"activityId":%d,"activityName":"Synthetic run %d","startTimeLocal":"2026-10-%02d 07:00:00","startTimeGMT":"2026-10-%02d 02:00:00",`+
			`"activityType":{"typeId":1,"typeKey":"running","parentTypeId":17,"isHidden":false,"trimmable":true},"eventType":{"typeId":9,"typeKey":"uncategorized"},`+
			`"distance":10012.5,"duration":3000.7,"elapsedDuration":3100.2,"movingDuration":2990.1,"elevationGain":120.5,"elevationLoss":118.2,"averageSpeed":3.33,`+
			`"maxSpeed":4.8,"averageHR":150,"maxHR":175,"calories":700,"avgPower":250,"aerobicTrainingEffect":3.4,"locationName":"Synthetic City",`+
			`"startLatitude":0,"startLongitude":0,"endLatitude":0,"endLongitude":0,"ownerId":1,"ownerDisplayName":"synthetic","ownerFullName":"Synthetic User",`+
			`"deviceId":3000000000,"manufacturer":"GARMIN","bmrCalories":90,"averageRunningCadenceInStepsPerMinute":170.5,"maxRunningCadenceInStepsPerMinute":190,`+
			`"steps":8400,"userRoles":["SCOPE_GLOBAL","ROLE_FITNESS_USER"],"privacy":{"typeId":2,"typeKey":"private"},"hasPolyline":true,"hasImages":false,`+
			`"hrTimeInZone_1":300.1,"hrTimeInZone_2":900.2,"hrTimeInZone_3":1200.3,"hrTimeInZone_4":500.4,"hrTimeInZone_5":100.5,`+
			`"description":%q,"splitSummaries":[{"noOfSplits":10,"totalAscent":120,"splitType":"INTERVAL_ACTIVE","numClimbSends":0,"maxElevationGain":12}]}`,
			12345678000+i, i, i%28+1, i%28+1, strings.Repeat("synthetic note ", 100))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func weightDays(n int) string {
	days := make([]any, n)
	for i := range days {
		metric := map[string]any{"samplePk": 1000 + i, "date": baseMillis + i, "calendarDate": dayStr(i),
			"weight": 70123.4, "bmi": 22.4, "bodyFat": 18.2, "bodyWater": 55.1, "boneMass": 3100, "muscleMass": 31000,
			"physiqueRating": nil, "visceralFat": nil, "metabolicAge": nil, "sourceType": "INDEX_SCALE",
			"timestampGMT": baseMillis + i, "weightDelta": 100.5}
		days[i] = map[string]any{"summaryDate": dayStr(i), "numOfWeightEntries": 2,
			"minWeight": 70000, "maxWeight": 70300, "avgWeight": 70123.4,
			"latestWeight": metric, "allWeightMetrics": []any{metric, metric}}
	}
	return mustJSON(map[string]any{
		"dailyWeightSummaries": days,
		"totalAverage":         map[string]any{"from": 1, "until": 2, "weight": 70123.4, "bmi": 22.4},
		"previousDateWeight":   map[string]any{"calendarDate": "2025-12-31", "weight": 70000},
		"nextDateWeight":       nil,
	})
}

func hydrationDays(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"calendarDate":%q,"valueInML":%d,"goalInML":2500,"dailyAverageinML":2100,"sweatLossInML":200,"activityIntakeInML":0}`, dayStr(i), 1800+i*10)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

func wellnessDays(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"calendarDate":%q,"activityType":"WALKING","steps":%d,"distance":%d,"moderateIntensityMinutes":10,"vigorousIntensityMinutes":5}`, dayStr(i), 6000+i, 4500+i)
	}
	return "[" + strings.Join(rows, ",") + "]"
}
