package tools

import (
	"testing"
	"time"
)

func TestParseDateRejectsFeb30(t *testing.T) {
	if _, err := ParseDate("2026-02-30"); err == nil {
		t.Fatal("expected error for 2026-02-30")
	}
	for _, s := range []string{"", "2026-2-3", "2026/02/03", "2026-02-03T00:00:00Z", " 2026-02-03"} {
		if _, err := ParseDate(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
	d, err := ParseDate("2026-02-28")
	if err != nil {
		t.Fatal(err)
	}
	if d.Year() != 2026 || d.Month() != time.February || d.Day() != 28 {
		t.Fatalf("got %v", d)
	}
}

func TestParseRangeTooLong(t *testing.T) {
	// 2026-01-01..2026-01-28 is 28 days inclusive.
	if _, _, err := ParseRange("2026-01-01", "2026-01-28", 28); err != nil {
		t.Fatalf("28 days must be accepted: %v", err)
	}
	if _, _, err := ParseRange("2026-01-01", "2026-01-29", 28); err == nil {
		t.Fatal("29 days must be rejected")
	}
	if _, _, err := ParseRange("bad", "2026-01-29", 28); err == nil {
		t.Fatal("malformed from must be rejected")
	}
	if _, _, err := ParseRange("2026-01-01", "bad", 28); err == nil {
		t.Fatal("malformed to must be rejected")
	}
}

func TestParseRangeReversed(t *testing.T) {
	if _, _, err := ParseRange("2026-01-10", "2026-01-09", 28); err == nil {
		t.Fatal("reversed range must be rejected")
	}
	f, to, err := ParseRange("2026-01-10", "2026-01-10", 28)
	if err != nil || !f.Equal(to) {
		t.Fatalf("single day: %v %v %v", f, to, err)
	}
}
