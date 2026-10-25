package tools

import (
	"encoding/json"
	"fmt"
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
	return Downsample(pts, per), true
}

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
			out["min"], out["max"], out["mean"] = minV, maxV, sum/float64(n)
		}
	}
	return out, true
}

// fitRowsA thins a list of per-day rows evenly so its JSON fits the budget.
// It returns the rows and a note when rows were dropped.
func fitRowsA(rows []any) ([]any, string) {
	b, err := json.Marshal(rows)
	if err != nil || len(b) <= outputBudgetA || len(rows) < 2 {
		return rows, ""
	}
	keep := len(rows) * outputBudgetA / len(b)
	if keep < 2 {
		keep = 2
	}
	out := pickA(rows, keep)
	return out, fmt.Sprintf("showing %d of %d days (evenly spaced) to keep the answer small; query a shorter range, or pass full=true for everything", len(out), len(rows))
}

// compactRowsA locates the per-day rows in a range response (a top-level array
// or the first array of objects in a top-level object), keeps only the given
// fields of each row (rows with none of them stay as they are) and thins the
// list to the output budget.
func compactRowsA(data any, keys []string) (any, string) {
	var rows []any
	var holder map[string]any
	var holderKey string
	switch t := data.(type) {
	case []any:
		rows = t
	case map[string]any:
		for k, v := range t {
			if arr, ok := v.([]any); ok && len(arr) > 0 {
				if _, isObj := arr[0].(map[string]any); isObj {
					rows, holder, holderKey = arr, t, k
					break
				}
			}
		}
	}
	if rows == nil {
		return shrinkA(data, false)
	}
	compact := make([]any, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			compact[i] = r
			continue
		}
		slim := map[string]any{}
		for _, k := range keys {
			if v, ok := m[k]; ok {
				slim[k] = v
			}
		}
		if len(slim) == 0 {
			compact[i], _ = shrinkA(r, false)
		} else {
			compact[i] = slim
		}
	}
	compact, note := fitRowsA(compact)
	if holder != nil {
		out := map[string]any{holderKey: compact}
		return out, note
	}
	return compact, note
}
