package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// outputBudgetA is the approximate maximum size of a tool's default JSON output.
const outputBudgetA = 8000

// GroupA returns the daily and time-series wellness tools.
func GroupA() []Tool {
	return []Tool{
		profileTool(),
		dailySummaryTool(),
		sleepTool(),
		heartRateTool(),
		hrvTool(),
		bodyBatteryTool(),
		stressTool(),
		stepsTool(),
		spo2Tool(),
	}
}

type modeA int

const (
	singleA modeA = iota // only `date`
	rangeA               // only `start_date` + `end_date`
	eitherA              // `date` or `start_date` + `end_date`
)

// argsA is the argument set shared by every group A date tool.
type argsA struct {
	Date      *string `json:"date"`
	StartDate *string `json:"start_date"`
	EndDate   *string `json:"end_date"`
	Full      bool    `json:"full"`
}

// reqA is a validated request: either a single date or a from/to range.
type reqA struct {
	date, from, to string
	full           bool
}

func (r reqA) isRange() bool { return r.date == "" }

// what describes the requested period for "no data" messages.
func (r reqA) what() string {
	if r.isRange() {
		return r.from + " to " + r.to
	}
	return r.date
}

// specA describes one date-based tool: how to build the Garmin request and
// how to shape the answer.
type specA struct {
	name, label string
	description string
	mode        modeA
	maxDays     int // range limit in days (inclusive)
	single      func(date string) (path string, q url.Values)
	rng         func(from, to string) (path string, q url.Values)
	shape       func(data any, r reqA) (any, string)
}

func decodeA(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) && ute.Field != "" {
			return fmt.Errorf("invalid arguments: %q has the wrong type", ute.Field)
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return fmt.Errorf("invalid arguments: %s", strings.TrimPrefix(err.Error(), "json: "))
		}
		return errors.New("invalid arguments: expected a JSON object")
	}
	if dec.More() {
		return errors.New("invalid arguments: expected a single JSON object")
	}
	return nil
}

func (s specA) parse(raw json.RawMessage) (reqA, error) {
	var a argsA
	if err := decodeA(raw, &a); err != nil {
		return reqA{}, err
	}
	hasDate := a.Date != nil
	hasRange := a.StartDate != nil || a.EndDate != nil
	req := reqA{full: a.Full}
	switch {
	case hasDate && hasRange:
		return reqA{}, errors.New("provide either date or start_date and end_date, not both")
	case hasDate:
		if s.mode == rangeA {
			return reqA{}, errors.New("this tool takes start_date and end_date, not date")
		}
		if _, err := ParseDate(*a.Date); err != nil {
			return reqA{}, err
		}
		req.date = *a.Date
	case hasRange:
		if s.mode == singleA {
			return reqA{}, errors.New("this tool takes a single date, not a range")
		}
		if a.StartDate == nil || a.EndDate == nil {
			return reqA{}, errors.New("start_date and end_date must be given together")
		}
		if _, _, err := ParseRange(*a.StartDate, *a.EndDate, s.maxDays); err != nil {
			return reqA{}, err
		}
		req.from, req.to = *a.StartDate, *a.EndDate
	default:
		switch s.mode {
		case singleA:
			return reqA{}, errors.New("date is required (YYYY-MM-DD)")
		case rangeA:
			return reqA{}, errors.New("start_date and end_date are required (YYYY-MM-DD)")
		}
		return reqA{}, errors.New("provide date, or start_date and end_date (YYYY-MM-DD)")
	}
	return req, nil
}

func (s specA) tool() Tool {
	props := map[string]any{"full": map[string]any{
		"type":        "boolean",
		"description": "Return every sample instead of a downsampled series (large output). Default false.",
	}}
	dateProp := map[string]any{"type": "string", "description": "Date, YYYY-MM-DD.", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	fromProp := map[string]any{"type": "string", "description": fmt.Sprintf("First day of the range, YYYY-MM-DD (at most %d days, inclusive).", s.maxDays), "pattern": `^\d{4}-\d{2}-\d{2}$`}
	toProp := map[string]any{"type": "string", "description": "Last day of the range, YYYY-MM-DD.", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	var required []string
	switch s.mode {
	case singleA:
		props["date"] = dateProp
		required = []string{"date"}
	case rangeA:
		props["start_date"], props["end_date"] = fromProp, toProp
		required = []string{"start_date", "end_date"}
	default:
		props["date"], props["start_date"], props["end_date"] = dateProp, fromProp, toProp
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 { // draft-04: never an empty "required"
		schema["required"] = required
	}
	return Tool{
		Name:        s.name,
		Description: s.description,
		InputSchema: schema,
		Handler: func(ctx context.Context, c Getter, apiKey string, raw json.RawMessage) (Result, error) {
			req, err := s.parse(raw)
			if err != nil {
				return Result{Errors: []string{err.Error()}}, nil
			}
			var path string
			var q url.Values
			if req.isRange() {
				path, q = s.rng(req.from, req.to)
			} else {
				path, q = s.single(req.date)
			}
			data, noData, res, err := fetchJSON(ctx, c, apiKey, path, q)
			if err != nil {
				return Result{}, err
			}
			if len(res.Errors) > 0 {
				return res, nil
			}
			if noData {
				return NoData("no data for " + s.label + ", " + req.what()), nil
			}
			out, note := s.shape(data, req)
			return Result{Data: out, Note: note}, nil
		},
	}
}

// shrinkShapeA is the default shape: downsample large sample arrays unless
// the caller asked for the full series.
func shrinkShapeA(data any, r reqA) (any, string) { return shrinkA(data, r.full) }

func dateQueryA(key, val string) url.Values { return url.Values{key: {val}} }

func rangeQueryA(from, to string) url.Values {
	return url.Values{"startDate": {from}, "endDate": {to}}
}
