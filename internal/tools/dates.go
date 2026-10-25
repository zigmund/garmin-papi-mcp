package tools

import (
	"fmt"
	"time"
)

const dateLayout = "2006-01-02"

// ParseDate parses a strict YYYY-MM-DD date (UTC midnight).
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil || t.Format(dateLayout) != s {
		return time.Time{}, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", s)
	}
	return t, nil
}

// ParseRange parses an inclusive date range and validates it spans at most
// maxDays days and that from is not after to.
func ParseRange(from, to string, maxDays int) (time.Time, time.Time, error) {
	f, err := ParseDate(from)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	t, err := ParseDate(to)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if f.After(t) {
		return time.Time{}, time.Time{}, fmt.Errorf("from %s is after to %s", from, to)
	}
	days := int(t.Sub(f).Hours()/24) + 1
	if days > maxDays {
		return time.Time{}, time.Time{}, fmt.Errorf("range of %d days exceeds the %d-day limit", days, maxDays)
	}
	return f, t, nil
}
