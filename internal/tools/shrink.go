package tools

import (
	"encoding/json"
	"fmt"
	"math"
)

// shrinkA bounds the size of a decoded Garmin payload. Large arrays of
// [timestamp, value, ...] pairs become a Series (stats over all points plus an
// evenly spaced subset); large arrays of objects keep an evenly spaced sample
// next to summary stats when a value field is recognised. The sample size is
// halved until the JSON fits the output budget. With full set, data is
// returned untouched.
func shrinkA(data any, full bool) (any, string) {
	if full {
		return data, ""
	}
	per := 96
	for {
		out := walkA(data, per)
		b, err := json.Marshal(out)
		if err != nil || len(b) <= outputBudgetA {
			return out, ""
		}
		if per <= 2 {
			return out, "output is larger than usual even when downsampled; narrow the date range"
		}
		per /= 2
	}
}

func walkA(v any, per int) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = walkA(e, per)
		}
		return out
	case []any:
		if len(t) > per {
			if s, ok := pairSeriesA(t, per); ok {
				return s
			}
			if o, ok := objectSampleA(t, per); ok {
				return o
			}
			if isScalarsA(t) {
				return map[string]any{"count": len(t), "samples": pickA(t, per)}
			}
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = walkA(e, per)
		}
		return out
	}
	return v
}

// pickA returns at most n evenly spaced elements, always including the first
// and last.
func pickA(s []any, n int) []any {
	if n < 2 {
		n = 2
	}
	if len(s) <= n {
		return s
	}
	out := make([]any, 0, n)
	last := len(s) - 1
	for i := 0; i < n; i++ {
		out = append(out, s[i*last/(n-1)])
	}
	return out
}

func isScalarsA(s []any) bool {
	for _, e := range s {
		switch e.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// pairSeriesA recognises [ts, value, ...] rows (value is the first number after
// the timestamp; status strings in between are ignored).
func pairSeriesA(s []any, per int) (Series, bool) {
	pts := make([][2]float64, 0, len(s))
	for _, e := range s {
		row, ok := e.([]any)
		if !ok || len(row) < 2 {
			return Series{}, false
		}
		ts, ok := row[0].(float64)
		if !ok {
			return Series{}, false
		}
		for _, x := range row[1:] {
			if val, ok := x.(float64); ok {
				pts = append(pts, [2]float64{ts, val})
				break
			}
		}
	}
	if len(pts) == 0 {
		return Series{}, false
	}
	out := Downsample(pts, per)
	out.Mean = round1A(out.Mean)
	return out, true
}

func round1A(f float64) float64 { return math.Round(f*10) / 10 }

var valueKeysA = []string{"hrvValue", "spo2Reading", "spO2Reading", "value", "stressLevel", "bodyBattery", "heartRate"}

// objectSampleA handles arrays of reading objects such as HRV and SpO2 samples.
func objectSampleA(s []any, per int) (any, bool) {
	var key string
	for _, e := range s {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		if key == "" {
			for _, k := range valueKeysA {
				if _, ok := m[k].(float64); ok {
					key = k
					break
				}
			}
		}
	}
	out := map[string]any{"count": len(s), "samples": pickA(s, per)}
	if key != "" {
		var n int
		var sum, minV, maxV float64
		for _, e := range s {
			if val, ok := e.(map[string]any)[key].(float64); ok {
				if n == 0 || val < minV {
					minV = val
				}
				if n == 0 || val > maxV {
					maxV = val
				}
				sum += val
				n++
			}
		}
		if n > 0 {
			out["field"] = key
			out["min"], out["max"], out["mean"] = minV, maxV, round1A(sum/float64(n))
		}
	}
	return out, true
}

// fieldA projects one value of a nested row (path) to a flat output key.
type fieldA struct {
	out  string
	path []string
}

func fa(out string, path ...string) fieldA { return fieldA{out: out, path: path} }

func projectA(row map[string]any, fs []fieldA) map[string]any {
	slim := map[string]any{}
	for _, f := range fs {
		var cur any = row
		for _, k := range f.path {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[k]
		}
		if cur != nil {
			slim[f.out] = cur
		}
	}
	return slim
}

// rowsOfA finds the per-day rows of a range response: a top-level array, or
// the "data" array (else the first array of objects) of a top-level object.
func rowsOfA(data any) ([]any, bool) {
	isObjs := func(v any) ([]any, bool) {
		arr, ok := v.([]any)
		if !ok || len(arr) == 0 {
			return nil, false
		}
		_, ok = arr[0].(map[string]any)
		return arr, ok
	}
	switch t := data.(type) {
	case []any:
		return isObjs(t)
	case map[string]any:
		if rows, ok := isObjs(t["data"]); ok {
			return rows, true
		}
		for _, v := range t {
			if rows, ok := isObjs(v); ok {
				return rows, true
			}
		}
	}
	return nil, false
}

func sizeOfA(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 1 << 30
	}
	return len(b)
}

// fitRowsA thins a list of per-day rows evenly so its JSON fits the budget.
func fitRowsA(rows []any) ([]any, string) {
	n := sizeOfA(rows)
	if n <= outputBudgetA || len(rows) < 2 {
		return rows, ""
	}
	keep := len(rows) * outputBudgetA / n
	if keep < 2 {
		keep = 2
	}
	out := pickA(rows, keep)
	return out, fmt.Sprintf("showing %d of %d days (evenly spaced) to keep the answer small; query a shorter range, or pass full=true for everything", len(out), len(rows))
}

// projectRowsA projects every per-day row onto the first tier of fields whose
// JSON fits the budget (tiers go from richest to leanest). When even the last
// tier is too big, rows are thinned evenly. The note says what was reduced.
func projectRowsA(data any, tiers [][]fieldA) (any, string) {
	rows, ok := rowsOfA(data)
	if !ok {
		return shrinkA(data, false)
	}
	var out []any
	for i, fs := range tiers {
		out = make([]any, len(rows))
		for j, r := range rows {
			m, _ := r.(map[string]any)
			slim := projectA(m, fs)
			if len(slim) == 0 {
				out[j], _ = shrinkA(r, false)
			} else {
				out[j] = slim
			}
		}
		if sizeOfA(out) <= outputBudgetA {
			if i == 0 {
				return out, ""
			}
			return out, fmt.Sprintf("per-day fields reduced to %d key values to keep the answer small; query a shorter range for more, or pass full=true for the raw data", len(fs))
		}
	}
	thinned, note := fitRowsA(out)
	return thinned, "per-day fields reduced; " + note
}

// bodyBatterySummaryA summarises each day's bodyBatteryValuesArray (stats plus
// a few points) and keeps date, charged and drained for every day.
func bodyBatteryShapeA(data any, r reqA) (any, string) {
	rows, ok := rowsOfA(data)
	if !ok || r.full {
		return shrinkA(data, r.full)
	}
	for per := 96; ; per /= 2 {
		out := make([]any, len(rows))
		for i, row := range rows {
			m, _ := row.(map[string]any)
			slim := map[string]any{}
			for _, k := range []string{"date", "charged", "drained"} {
				if v, ok := m[k]; ok {
					slim[k] = v
				}
			}
			if arr, ok := m["bodyBatteryValuesArray"].([]any); ok {
				if s, ok := pairSeriesA(arr, per); ok {
					if per < 2 {
						s.Points = [][2]float64{}
					}
					slim["bodyBatteryValuesArray"] = s
				}
			}
			out[i] = slim
		}
		if sizeOfA(out) <= outputBudgetA || per < 2 {
			note := ""
			if per < 96 {
				note = "Body Battery samples reduced per day (stats cover all samples); pass full=true for every sample"
			}
			return out, note
		}
	}
}
